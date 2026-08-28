// Package manager joins a provider API to the local state store.
//
// Commands talk to this rather than to either half, so one rule lives in one
// place: the provider is the source of truth and state follows it, never the
// other way round. That is what makes `vpncli sync` a reconciliation and not a
// merge, and it is why a row is written before a server is waited on - an
// untracked server is one that keeps billing where nobody can see it.
package manager

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/lestex/vpncli/internal/provider"
	"github.com/lestex/vpncli/internal/state"
)

// Manager owns the pairing of one provider with the state store.
type Manager struct {
	provider provider.VPSProvider
	store    *state.Store
}

// New returns a Manager over the given provider and store.
//
// Every method here needs the provider. Reading local state does not, so
// `vpncli list` goes to the store directly rather than through a Manager -
// that is what keeps it instant and usable with no API token.
func New(p provider.VPSProvider, store *state.Store) *Manager {
	return &Manager{provider: p, store: store}
}

// SyncResult is what one reconciliation changed. Every field holds the rows as
// they are after the pass, so a caller can report them without another read.
type SyncResult struct {
	// Adopted are tagged servers the state store had never seen - created by
	// another machine, or by a run that died between the API call and the
	// insert.
	Adopted []state.Server
	// Updated are rows whose address or lifecycle state moved.
	Updated []state.Server
	// Removed are rows whose server is gone from the provider.
	Removed []state.Server
	// Unchanged counts rows that already matched.
	Unchanged int
}

// Changed reports whether the pass touched anything.
func (r SyncResult) Changed() bool {
	return len(r.Adopted) > 0 || len(r.Updated) > 0 || len(r.Removed) > 0
}

// Sync reconciles local state against the provider API.
//
// Rows whose server is gone are dropped, rows that drifted are corrected, and
// servers carrying provider.ManagedTag that state has never seen are adopted.
// Untagged servers are left alone: the listing is account-wide and most of
// what it returns may have nothing to do with vpncli.
func (m *Manager) Sync(ctx context.Context) (SyncResult, error) {
	live, err := m.provider.ListInstances(ctx)
	if err != nil {
		return SyncResult{}, fmt.Errorf("listing servers from %s: %w", m.provider.Name(), err)
	}

	rows, err := m.store.List(ctx)
	if err != nil {
		return SyncResult{}, err
	}

	byProviderID := make(map[string]provider.VPSInstance, len(live))
	for _, inst := range live {
		byProviderID[inst.ID] = inst
	}

	var result SyncResult
	seen := make(map[string]bool, len(rows))

	for _, row := range rows {
		// Rows belonging to another provider are not this pass's business.
		if row.Provider != m.provider.Name() {
			continue
		}
		seen[row.ProviderID] = true

		inst, stillThere := byProviderID[row.ProviderID]
		if !stillThere {
			if err := m.store.Delete(ctx, row.ID); err != nil {
				return result, err
			}
			result.Removed = append(result.Removed, row)
			continue
		}

		if row.IPv4 == inst.IPv4 && row.Status == string(inst.Status) {
			result.Unchanged++
			continue
		}

		row.IPv4 = inst.IPv4
		row.Status = string(inst.Status)
		if err := m.store.Update(ctx, row); err != nil {
			return result, err
		}
		result.Updated = append(result.Updated, row)
	}

	for _, inst := range live {
		if seen[inst.ID] || !inst.Managed() {
			continue
		}

		adopted, err := m.store.Insert(ctx, toServer(inst))
		if err != nil {
			return result, err
		}
		result.Adopted = append(result.Adopted, adopted)
	}

	return result, nil
}

// Provision creates a server, records it, and waits for it to become usable.
//
// The row is written as soon as the provider accepts the request and before
// the wait begins. A server that exists but is not in state is invisible and
// still billed, so the ordering is deliberate: if the wait is interrupted, the
// returned server still names something `vpncli destroy` can clean up.
func (m *Manager) Provision(ctx context.Context, opts provider.CreateOptions) (state.Server, error) {
	// Tagging is what makes a server ours as far as Sync is concerned, so it
	// is applied here rather than trusted to the caller.
	if !slices.Contains(opts.Tags, provider.ManagedTag) {
		opts.Tags = append(slices.Clone(opts.Tags), provider.ManagedTag)
	}

	inst, err := m.provider.CreateInstance(ctx, opts)
	if err != nil {
		return state.Server{}, err
	}

	srv, err := m.store.Insert(ctx, toServer(inst))
	if err != nil {
		return state.Server{}, fmt.Errorf("recording server %s: %w", inst.ID, err)
	}

	ready, err := m.provider.WaitReady(ctx, inst.ID)
	if err != nil {
		return srv, err
	}

	srv.IPv4 = ready.IPv4
	srv.Status = string(ready.Status)
	if err := m.store.Update(ctx, srv); err != nil {
		return srv, err
	}

	return srv, nil
}

// Destroy deletes the server behind a local id, provider first and state
// second. A server already gone from the provider is not an error - the row is
// exactly what needs clearing - but a failed delete leaves the row in place so
// the server cannot be lost track of.
func (m *Manager) Destroy(ctx context.Context, id int64) (state.Server, error) {
	srv, err := m.store.Get(ctx, id)
	if err != nil {
		return state.Server{}, err
	}

	if err := m.provider.DeleteInstance(ctx, srv.ProviderID); err != nil && !errors.Is(err, provider.ErrNotFound) {
		return srv, err
	}

	if err := m.store.Delete(ctx, srv.ID); err != nil {
		return srv, err
	}

	return srv, nil
}

// toServer converts the live view of a server into a persistable row.
func toServer(inst provider.VPSInstance) state.Server {
	return state.Server{
		Provider:   inst.Provider,
		ProviderID: inst.ID,
		Name:       inst.Name,
		Region:     inst.Region,
		Size:       inst.Size,
		Image:      inst.Image,
		IPv4:       inst.IPv4,
		Status:     string(inst.Status),
		CreatedAt:  inst.CreatedAt,
	}
}
