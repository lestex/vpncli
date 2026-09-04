// Package digitalocean implements provider.VPSProvider for DigitalOcean. The
// create, inspect and delete path is complete, as are the catalog lookups the
// `vpncli providers init` wizard reads.
package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/digitalocean/godo"

	"github.com/lestex/vpncli/internal/provider"
)

// Name is the provider slug used in config and state.
const Name = "digitalocean"

const (
	// dropletsPerPage is DigitalOcean's maximum page size.
	dropletsPerPage = 200

	// sshPort is what WaitReady probes. A droplet is not usable until sshd
	// answers, whatever the API says about it.
	sshPort = "22"

	// defaultPollInterval is how often WaitReady re-checks a droplet. Boot
	// takes roughly 30-60s, so a wait costs a handful of calls against an
	// hourly budget of 5000.
	defaultPollInterval = 5 * time.Second

	// dialTimeout bounds one SSH probe. Kept under defaultPollInterval so a
	// filtered port fails fast instead of stretching the polling cadence.
	dialTimeout = 3 * time.Second

	// defaultMaxAttempts caps how many times one API call is tried.
	defaultMaxAttempts = 5
)

// ErrNoToken is returned when no API token is present in the environment.
var ErrNoToken = errors.New("no DigitalOcean API token: set DIGITALOCEAN_TOKEN or DIGITALOCEAN_ACCESS_TOKEN")

// ErrInvalidOptions is returned when CreateOptions is missing something the
// API requires. It is reported before any request goes out.
var ErrInvalidOptions = errors.New("invalid create options")

// Provider talks to the DigitalOcean API. It holds only the services it calls,
// plus the waiting and dialing it does around them, so tests can substitute
// each and neither touch the network nor spend real time.
type Provider struct {
	droplets godo.DropletsService
	catalog  catalog

	sleep   func(ctx context.Context, d time.Duration) error
	dialSSH func(ctx context.Context, ip string) error

	pollInterval time.Duration
	maxAttempts  int
}

var _ provider.VPSProvider = (*Provider)(nil)

// New returns a Provider authenticated with the given API token.
func New(token string) (*Provider, error) {
	if token == "" {
		return nil, ErrNoToken
	}
	client := godo.NewFromToken(token)
	return newProvider(client.Droplets, catalog{
		regions: client.Regions,
		sizes:   client.Sizes,
		images:  client.Images,
		keys:    client.Keys,
	}), nil
}

// newProvider applies the defaults shared by New and the tests.
func newProvider(droplets godo.DropletsService, catalog catalog) *Provider {
	return &Provider{
		droplets:     droplets,
		catalog:      catalog,
		sleep:        sleep,
		dialSSH:      dialSSH,
		pollInterval: defaultPollInterval,
		maxAttempts:  defaultMaxAttempts,
	}
}

// TokenFromEnv reads the API token from DIGITALOCEAN_TOKEN, then
// DIGITALOCEAN_ACCESS_TOKEN, which is the variable doctl uses.
func TokenFromEnv() (string, error) {
	for _, key := range []string{"DIGITALOCEAN_TOKEN", "DIGITALOCEAN_ACCESS_TOKEN"} {
		if token := os.Getenv(key); token != "" {
			return token, nil
		}
	}
	return "", ErrNoToken
}

// Name returns the provider slug.
func (p *Provider) Name() string { return Name }

// ListInstances returns every droplet in the account, unfiltered.
func (p *Provider) ListInstances(ctx context.Context) ([]provider.VPSInstance, error) {
	opt := &godo.ListOptions{Page: 1, PerPage: dropletsPerPage}

	var instances []provider.VPSInstance
	for {
		droplets, resp, err := call(ctx, p, transient, func() ([]godo.Droplet, *godo.Response, error) {
			return p.droplets.List(ctx, opt)
		})
		if err != nil {
			return nil, fmt.Errorf("listing droplets: %w", err)
		}

		for _, droplet := range droplets {
			instances = append(instances, toInstance(droplet))
		}

		// Track pages locally rather than parsing them out of response links.
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			return instances, nil
		}
		opt.Page++
	}
}

// GetInstance returns one droplet, or provider.ErrNotFound if the account no
// longer has it.
func (p *Provider) GetInstance(ctx context.Context, id string) (provider.VPSInstance, error) {
	dropletID, err := parseID(id)
	if err != nil {
		return provider.VPSInstance{}, err
	}

	droplet, resp, err := call(ctx, p, transient, func() (*godo.Droplet, *godo.Response, error) {
		return p.droplets.Get(ctx, dropletID)
	})
	switch {
	case err != nil && status(resp, err) == http.StatusNotFound:
		return provider.VPSInstance{}, fmt.Errorf("droplet %s: %w", id, provider.ErrNotFound)
	case err != nil:
		return provider.VPSInstance{}, fmt.Errorf("getting droplet %s: %w", id, err)
	case droplet == nil:
		// A 200 with no droplet in it should not happen, but returning a zero
		// instance would look like a real server with no address.
		return provider.VPSInstance{}, fmt.Errorf("droplet %s: %w", id, provider.ErrNotFound)
	}

	return toInstance(*droplet), nil
}

// CreateInstance stands up one droplet. It returns as soon as DigitalOcean
// accepts the request, which is well before the droplet is usable - callers
// hand the ID to WaitReady.
func (p *Provider) CreateInstance(ctx context.Context, opts provider.CreateOptions) (provider.VPSInstance, error) {
	req, err := createRequest(opts)
	if err != nil {
		return provider.VPSInstance{}, err
	}

	// rateLimited, not transient: a 5xx here may mean the droplet was created
	// and only the reply went missing, and retrying would strand a second one.
	droplet, _, err := call(ctx, p, rateLimited, func() (*godo.Droplet, *godo.Response, error) {
		return p.droplets.Create(ctx, req)
	})
	if err != nil {
		return provider.VPSInstance{}, fmt.Errorf("creating droplet %q: %w", opts.Name, err)
	}
	if droplet == nil {
		return provider.VPSInstance{}, fmt.Errorf("creating droplet %q: DigitalOcean returned no droplet", opts.Name)
	}

	return toInstance(*droplet), nil
}

// DeleteInstance destroys one droplet, reporting provider.ErrNotFound if it is
// already gone.
func (p *Provider) DeleteInstance(ctx context.Context, id string) error {
	dropletID, err := parseID(id)
	if err != nil {
		return err
	}

	_, resp, err := call(ctx, p, transient, func() (struct{}, *godo.Response, error) {
		resp, err := p.droplets.Delete(ctx, dropletID)
		return struct{}{}, resp, err
	})
	if err != nil {
		if status(resp, err) == http.StatusNotFound {
			return fmt.Errorf("droplet %s: %w", id, provider.ErrNotFound)
		}
		return fmt.Errorf("deleting droplet %s: %w", id, err)
	}

	return nil
}

// WaitReady polls until the droplet is active with a public address and sshd
// is answering on it. "active" on its own is not enough: DigitalOcean reports
// it once the hypervisor has the VM, seconds before the guest finishes
// booting, and bootstrap would connect into a refused port.
func (p *Provider) WaitReady(ctx context.Context, id string) (provider.VPSInstance, error) {
	for {
		inst, err := p.GetInstance(ctx, id)
		if err != nil {
			return provider.VPSInstance{}, err
		}

		switch inst.Status {
		case provider.StatusError, provider.StatusDeleting:
			return provider.VPSInstance{}, fmt.Errorf("droplet %s went to %q while booting", id, inst.Status)
		case provider.StatusActive:
			if inst.IPv4 != "" && p.probeSSH(ctx, inst.IPv4) == nil {
				return inst, nil
			}
		}

		if err := p.sleep(ctx, p.pollInterval); err != nil {
			return provider.VPSInstance{}, fmt.Errorf("waiting for droplet %s: %w", id, err)
		}
	}
}

// probeSSH opens and drops a connection to the SSH port, bounded so an
// unreachable host fails fast rather than hanging until ctx expires.
func (p *Provider) probeSSH(ctx context.Context, ip string) error {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	return p.dialSSH(ctx, ip)
}

// dialSSH reports whether ip is accepting TCP connections on the SSH port.
func dialSSH(ctx context.Context, ip string) error {
	var dialer net.Dialer

	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, sshPort))
	if err != nil {
		return err
	}
	return conn.Close()
}

// createRequest validates CreateOptions and translates it for godo. Missing
// fields are caught here so a typo in the wizard costs a message rather than a
// round trip and an opaque 422.
func createRequest(opts provider.CreateOptions) (*godo.DropletCreateRequest, error) {
	var missing []string
	for _, field := range []struct {
		name  string
		empty bool
	}{
		{"name", opts.Name == ""},
		{"region", opts.Region == ""},
		{"size", opts.Size == ""},
		{"image", opts.Image == ""},
		// Without a key DigitalOcean mails a root password instead, which is
		// both worse and useless to a bootstrap that connects over SSH.
		{"ssh key", len(opts.SSHKeyIDs) == 0},
	} {
		if field.empty {
			missing = append(missing, field.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: missing %s", ErrInvalidOptions, strings.Join(missing, ", "))
	}

	req := &godo.DropletCreateRequest{
		Name:   opts.Name,
		Region: opts.Region,
		Size:   opts.Size,
		Image:  createImage(opts.Image),
		Tags:   opts.Tags,
		// The metrics agent is extra code running as root on a box whose whole
		// point is to be unremarkable, and nothing here reads its data.
		WithDropletAgent: godo.PtrTo(false),
	}
	for _, key := range opts.SSHKeyIDs {
		req.SSHKeys = append(req.SSHKeys, createSSHKey(key))
	}

	return req, nil
}

// createImage sends a numeric image as an ID and anything else as a slug,
// which is how a snapshot is told apart from "ubuntu-24-04-x64".
func createImage(image string) godo.DropletCreateImage {
	if id, err := strconv.Atoi(image); err == nil {
		return godo.DropletCreateImage{ID: id}
	}
	return godo.DropletCreateImage{Slug: image}
}

// createSSHKey does the same for keys: numeric values are DigitalOcean key
// IDs, anything else is a fingerprint.
func createSSHKey(key string) godo.DropletCreateSSHKey {
	if id, err := strconv.Atoi(key); err == nil {
		return godo.DropletCreateSSHKey{ID: id}
	}
	return godo.DropletCreateSSHKey{Fingerprint: key}
}

// parseID converts a stored ID back to a droplet ID. A value that was never
// one cannot name a live droplet, so it reports ErrNotFound and `vpncli sync`
// retires the row instead of stalling on it.
func parseID(id string) (int, error) {
	dropletID, err := strconv.Atoi(id)
	if err != nil || dropletID <= 0 {
		return 0, fmt.Errorf("%q is not a droplet ID: %w", id, provider.ErrNotFound)
	}
	return dropletID, nil
}

// toInstance converts a droplet to the provider-independent shape. Missing
// fields are left zero: a droplet that is still building has no address yet.
func toInstance(d godo.Droplet) provider.VPSInstance {
	inst := provider.VPSInstance{
		ID:       strconv.Itoa(d.ID),
		Name:     d.Name,
		Provider: Name,
		Size:     d.SizeSlug,
		Status:   toStatus(d.Status),
		Tags:     d.Tags,
	}

	if d.Region != nil {
		inst.Region = d.Region.Slug
	}
	if d.Image != nil {
		inst.Image = imageRef(d.Image)
	}
	if ip, err := d.PublicIPv4(); err == nil {
		inst.IPv4 = ip
	}
	if created, err := time.Parse(time.RFC3339, d.Created); err == nil {
		inst.CreatedAt = created
	}

	return inst
}

// imageRef prefers the slug, which is what a create call takes. Custom and
// snapshot images have none.
func imageRef(img *godo.Image) string {
	switch {
	case img.Slug != "":
		return img.Slug
	case img.Name != "":
		return img.Name
	default:
		return strconv.Itoa(img.ID)
	}
}

// toStatus maps droplet states onto the normalized set.
func toStatus(status string) provider.Status {
	switch status {
	case "new":
		return provider.StatusProvisioning
	case "active":
		return provider.StatusActive
	case "off":
		return provider.StatusStopped
	case "archive":
		return provider.StatusDeleting
	default:
		return provider.StatusUnknown
	}
}
