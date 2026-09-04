package manager

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lestex/vpncli/internal/provider"
	"github.com/lestex/vpncli/internal/state"
)

// fakeProvider stands in for a cloud. Only the methods the manager uses are
// implemented; the interface is embedded so anything else panics rather than
// quietly returning a zero value.
type fakeProvider struct {
	provider.VPSProvider

	instances []provider.VPSInstance
	listErr   error

	created   provider.CreateOptions
	createErr error

	ready    provider.VPSInstance
	readyErr error

	deleted   []string
	deleteErr error
}

func (f *fakeProvider) Name() string { return "digitalocean" }

func (f *fakeProvider) ListInstances(context.Context) ([]provider.VPSInstance, error) {
	return f.instances, f.listErr
}

func (f *fakeProvider) CreateInstance(_ context.Context, opts provider.CreateOptions) (provider.VPSInstance, error) {
	f.created = opts
	if f.createErr != nil {
		return provider.VPSInstance{}, f.createErr
	}
	return provider.VPSInstance{
		ID:        "1001",
		Name:      opts.Name,
		Provider:  f.Name(),
		Region:    opts.Region,
		Size:      opts.Size,
		Image:     opts.Image,
		Status:    provider.StatusProvisioning,
		CreatedAt: time.Now().UTC(),
		Tags:      opts.Tags,
	}, nil
}

func (f *fakeProvider) WaitReady(context.Context, string) (provider.VPSInstance, error) {
	return f.ready, f.readyErr
}

func (f *fakeProvider) DeleteInstance(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return f.deleteErr
}

// newTestManager pairs a fake provider with a real in-memory store, so the SQL
// under test is the SQL that ships.
func newTestManager(t *testing.T, f *fakeProvider) *Manager {
	t.Helper()

	store, err := state.Open(":memory:")
	if err != nil {
		t.Fatalf("opening state: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	return New(f, store)
}

func instance(id, ipv4 string, status provider.Status, tags ...string) provider.VPSInstance {
	return provider.VPSInstance{
		ID:        id,
		Name:      "vpncli-fra1-" + id,
		Provider:  "digitalocean",
		Region:    "fra1",
		Size:      "s-1vcpu-1gb",
		Image:     "ubuntu-24-04-x64",
		IPv4:      ipv4,
		Status:    status,
		CreatedAt: time.Now().UTC().Add(-time.Hour),
		Tags:      tags,
	}
}

// The address a droplet gets after boot is the drift that matters most: it is
// what `connect` dials.
func TestSyncUpdatesDriftedRows(t *testing.T) {
	ctx := context.Background()
	f := &fakeProvider{instances: []provider.VPSInstance{
		instance("1001", "203.0.113.99", provider.StatusActive, provider.ManagedTag),
	}}
	m := newTestManager(t, f)

	seeded, err := m.store.Insert(ctx, toServer(instance("1001", "", provider.StatusProvisioning, provider.ManagedTag)))
	if err != nil {
		t.Fatalf("seeding state: %v", err)
	}

	result, err := m.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(result.Updated) != 1 || result.Unchanged != 0 {
		t.Fatalf("got %+v, want one updated row", result)
	}
	if result.Updated[0].IPv4 != "203.0.113.99" {
		t.Errorf("reported IPv4 %q, want the live one", result.Updated[0].IPv4)
	}

	stored, err := m.store.Get(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.IPv4 != "203.0.113.99" || stored.Status != string(provider.StatusActive) {
		t.Errorf("stored row is %+v, want the live address and status", stored)
	}
}

func TestSyncLeavesMatchingRowsAlone(t *testing.T) {
	ctx := context.Background()
	live := instance("1001", "203.0.113.10", provider.StatusActive, provider.ManagedTag)
	f := &fakeProvider{instances: []provider.VPSInstance{live}}
	m := newTestManager(t, f)

	if _, err := m.store.Insert(ctx, toServer(live)); err != nil {
		t.Fatalf("seeding state: %v", err)
	}

	result, err := m.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Unchanged != 1 || result.Changed() {
		t.Errorf("got %+v, want one unchanged row and nothing touched", result)
	}
}

// A server destroyed from the DigitalOcean console leaves a row behind. Sync
// is what retires it.
func TestSyncRemovesRowsForVanishedServers(t *testing.T) {
	ctx := context.Background()
	f := &fakeProvider{}
	m := newTestManager(t, f)

	if _, err := m.store.Insert(ctx, toServer(instance("1001", "203.0.113.10", provider.StatusActive, provider.ManagedTag))); err != nil {
		t.Fatalf("seeding state: %v", err)
	}

	result, err := m.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0].ProviderID != "1001" {
		t.Fatalf("got %+v, want the stale row removed", result)
	}

	rows, err := m.store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("state still holds %+v", rows)
	}
}

func TestSyncAdoptsTaggedServers(t *testing.T) {
	ctx := context.Background()
	f := &fakeProvider{instances: []provider.VPSInstance{
		instance("1001", "203.0.113.10", provider.StatusActive, provider.ManagedTag),
	}}
	m := newTestManager(t, f)

	result, err := m.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(result.Adopted) != 1 {
		t.Fatalf("got %+v, want one adopted server", result)
	}
	if result.Adopted[0].ID == 0 {
		t.Error("the adopted row has no local id")
	}

	rows, err := m.store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].IPv4 != "203.0.113.10" {
		t.Errorf("state holds %+v, want the adopted server", rows)
	}
}

// The listing is account-wide. Adopting everything in it would put someone's
// unrelated production box under `vpncli destroy`.
func TestSyncIgnoresUntaggedServers(t *testing.T) {
	ctx := context.Background()
	f := &fakeProvider{instances: []provider.VPSInstance{
		instance("2002", "203.0.113.20", provider.StatusActive),
		instance("2003", "203.0.113.30", provider.StatusActive, "production", "web"),
	}}
	m := newTestManager(t, f)

	result, err := m.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Changed() {
		t.Errorf("got %+v, want nothing adopted", result)
	}

	rows, err := m.store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("state adopted %+v", rows)
	}
}

// Rows for a provider this manager does not speak for must survive a pass,
// since its listing says nothing about them.
func TestSyncLeavesOtherProvidersAlone(t *testing.T) {
	ctx := context.Background()
	f := &fakeProvider{}
	m := newTestManager(t, f)

	other := toServer(instance("h-77", "203.0.113.77", provider.StatusActive, provider.ManagedTag))
	other.Provider = "hetzner"
	if _, err := m.store.Insert(ctx, other); err != nil {
		t.Fatalf("seeding state: %v", err)
	}

	result, err := m.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Changed() || result.Unchanged != 0 {
		t.Errorf("got %+v, want the hetzner row untouched and uncounted", result)
	}

	rows, err := m.store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("state holds %+v, want the hetzner row kept", rows)
	}
}

func TestSyncPropagatesAPIErrors(t *testing.T) {
	apiErr := errors.New("401 unauthorized")
	m := newTestManager(t, &fakeProvider{listErr: apiErr})

	if _, err := m.Sync(context.Background()); !errors.Is(err, apiErr) {
		t.Errorf("got %v, want it to wrap the API error", err)
	}
}

func TestProvisionRecordsAndWaits(t *testing.T) {
	ctx := context.Background()
	f := &fakeProvider{ready: instance("1001", "203.0.113.10", provider.StatusActive, provider.ManagedTag)}
	m := newTestManager(t, f)

	srv, err := m.Provision(ctx, provider.CreateOptions{
		Name:      "vpncli-fra1-a1b2",
		Region:    "fra1",
		Size:      "s-1vcpu-1gb",
		Image:     "ubuntu-24-04-x64",
		SSHKeyIDs: []string{"aa:bb:cc"},
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if srv.ID == 0 {
		t.Error("the provisioned server has no local id")
	}
	if srv.IPv4 != "203.0.113.10" || srv.Status != string(provider.StatusActive) {
		t.Errorf("got %+v, want the ready address and status", srv)
	}

	stored, err := m.store.Get(ctx, srv.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.IPv4 != "203.0.113.10" {
		t.Errorf("stored row is %+v, want it updated after the wait", stored)
	}
}

// Tagging is the manager's job, not the caller's: an untagged server would be
// invisible to Sync.
func TestProvisionTags(t *testing.T) {
	ctx := context.Background()
	f := &fakeProvider{ready: instance("1001", "203.0.113.10", provider.StatusActive, provider.ManagedTag)}
	m := newTestManager(t, f)

	if _, err := m.Provision(ctx, provider.CreateOptions{Name: "vpncli-fra1-a1b2"}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if !slices.Contains(f.created.Tags, provider.ManagedTag) {
		t.Errorf("created with tags %v, want %q among them", f.created.Tags, provider.ManagedTag)
	}
}

func TestProvisionDoesNotDuplicateTheTag(t *testing.T) {
	ctx := context.Background()
	f := &fakeProvider{ready: instance("1001", "203.0.113.10", provider.StatusActive, provider.ManagedTag)}
	m := newTestManager(t, f)

	if _, err := m.Provision(ctx, provider.CreateOptions{Tags: []string{provider.ManagedTag}}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(f.created.Tags) != 1 {
		t.Errorf("created with tags %v, want just the one", f.created.Tags)
	}
}

// A caller's slice must not grow a tag behind its back.
func TestProvisionDoesNotMutateCallerTags(t *testing.T) {
	ctx := context.Background()
	f := &fakeProvider{ready: instance("1001", "203.0.113.10", provider.StatusActive, provider.ManagedTag)}
	m := newTestManager(t, f)

	tags := make([]string, 1, 4)
	tags[0] = "experiment"

	if _, err := m.Provision(ctx, provider.CreateOptions{Tags: tags}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(tags) != 1 || tags[0] != "experiment" {
		t.Errorf("caller's slice became %v", tags)
	}
}

// The whole point of inserting before waiting: a wait that fails must still
// leave something `destroy` can find.
func TestProvisionKeepsTheRowWhenTheWaitFails(t *testing.T) {
	ctx := context.Background()
	bootFailed := errors.New("droplet never came up")
	f := &fakeProvider{readyErr: bootFailed}
	m := newTestManager(t, f)

	srv, err := m.Provision(ctx, provider.CreateOptions{Name: "vpncli-fra1-a1b2"})
	if !errors.Is(err, bootFailed) {
		t.Fatalf("got %v, want the boot failure", err)
	}
	if srv.ID == 0 {
		t.Fatal("no server returned, so nothing names what to clean up")
	}

	rows, err := m.store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].ProviderID != "1001" {
		t.Errorf("state holds %+v, want the half-provisioned server kept", rows)
	}
}

// Nothing was created, so nothing should be recorded.
func TestProvisionRecordsNothingWhenCreateFails(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t, &fakeProvider{createErr: errors.New("422 unprocessable")})

	if _, err := m.Provision(ctx, provider.CreateOptions{Name: "vpncli-fra1-a1b2"}); err == nil {
		t.Fatal("expected an error")
	}

	rows, err := m.store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("state holds %+v after a failed create", rows)
	}
}

func TestDestroy(t *testing.T) {
	ctx := context.Background()
	f := &fakeProvider{}
	m := newTestManager(t, f)

	seeded, err := m.store.Insert(ctx, toServer(instance("1001", "203.0.113.10", provider.StatusActive, provider.ManagedTag)))
	if err != nil {
		t.Fatalf("seeding state: %v", err)
	}

	srv, err := m.Destroy(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if srv.ProviderID != "1001" {
		t.Errorf("returned %+v, want the destroyed server", srv)
	}
	if !slices.Equal(f.deleted, []string{"1001"}) {
		t.Errorf("deleted %v, want [1001]", f.deleted)
	}

	rows, err := m.store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("state still holds %+v", rows)
	}
}

// Already gone provider-side is the case the row most needs clearing.
func TestDestroyClearsTheRowWhenTheServerIsAlreadyGone(t *testing.T) {
	ctx := context.Background()
	f := &fakeProvider{deleteErr: provider.ErrNotFound}
	m := newTestManager(t, f)

	seeded, err := m.store.Insert(ctx, toServer(instance("1001", "203.0.113.10", provider.StatusActive, provider.ManagedTag)))
	if err != nil {
		t.Fatalf("seeding state: %v", err)
	}

	if _, err := m.Destroy(ctx, seeded.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	rows, err := m.store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("state still holds %+v", rows)
	}
}

// A delete that genuinely failed must leave the row, or the server is lost.
func TestDestroyKeepsTheRowWhenTheProviderFails(t *testing.T) {
	ctx := context.Background()
	apiErr := errors.New("500 internal server error")
	f := &fakeProvider{deleteErr: apiErr}
	m := newTestManager(t, f)

	seeded, err := m.store.Insert(ctx, toServer(instance("1001", "203.0.113.10", provider.StatusActive, provider.ManagedTag)))
	if err != nil {
		t.Fatalf("seeding state: %v", err)
	}

	if _, err := m.Destroy(ctx, seeded.ID); !errors.Is(err, apiErr) {
		t.Fatalf("got %v, want the API error", err)
	}

	rows, err := m.store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("state holds %+v, want the row kept so the server is not lost", rows)
	}
}

// Provider ids collide across providers, so a row from another one must not
// be turned into a delete against the configured provider.
func TestDestroyRefusesARowFromAnotherProvider(t *testing.T) {
	ctx := context.Background()
	f := &fakeProvider{}
	m := newTestManager(t, f)

	row := toServer(instance("1001", "203.0.113.10", provider.StatusActive, provider.ManagedTag))
	row.Provider = "hetzner"
	seeded, err := m.store.Insert(ctx, row)
	if err != nil {
		t.Fatalf("seeding state: %v", err)
	}

	if _, err := m.Destroy(ctx, seeded.ID); !errors.Is(err, ErrWrongProvider) {
		t.Fatalf("got %v, want ErrWrongProvider", err)
	}
	if len(f.deleted) != 0 {
		t.Errorf("deleted %v for a row belonging to another provider", f.deleted)
	}

	rows, err := m.store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("state holds %+v, want the row kept", rows)
	}
}

func TestDestroyUnknownID(t *testing.T) {
	f := &fakeProvider{}
	m := newTestManager(t, f)

	if _, err := m.Destroy(context.Background(), 42); !errors.Is(err, state.ErrNotFound) {
		t.Errorf("got %v, want state.ErrNotFound", err)
	}
	if len(f.deleted) != 0 {
		t.Errorf("deleted %v for an unknown row", f.deleted)
	}
}
