// Package digitalocean implements provider.VPSProvider for DigitalOcean.
// Currently read-only: only ListInstances is implemented.
package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/digitalocean/godo"

	"github.com/lestex/vpncli/internal/provider"
)

// Name is the provider slug used in config and state.
const Name = "digitalocean"

// dropletsPerPage is DigitalOcean's maximum page size.
const dropletsPerPage = 200

// ErrNotImplemented is returned by the unimplemented methods.
var ErrNotImplemented = errors.New("not implemented: the DigitalOcean provider is read-only")

// ErrNoToken is returned when no API token is present in the environment.
var ErrNoToken = errors.New("no DigitalOcean API token: set DIGITALOCEAN_TOKEN or DIGITALOCEAN_ACCESS_TOKEN")

// Provider talks to the DigitalOcean API. It holds only the droplets service
// so tests can inject a fake.
type Provider struct {
	droplets godo.DropletsService
}

var _ provider.VPSProvider = (*Provider)(nil)

// New returns a Provider authenticated with the given API token.
func New(token string) (*Provider, error) {
	if token == "" {
		return nil, ErrNoToken
	}
	return &Provider{droplets: godo.NewFromToken(token).Droplets}, nil
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
		droplets, resp, err := p.droplets.List(ctx, opt)
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

// GetInstance is not implemented.
func (p *Provider) GetInstance(context.Context, string) (provider.VPSInstance, error) {
	return provider.VPSInstance{}, ErrNotImplemented
}

// CreateInstance is not implemented.
func (p *Provider) CreateInstance(context.Context, provider.CreateOptions) (provider.VPSInstance, error) {
	return provider.VPSInstance{}, ErrNotImplemented
}

// DeleteInstance is not implemented.
func (p *Provider) DeleteInstance(context.Context, string) error {
	return ErrNotImplemented
}

// WaitReady is not implemented.
func (p *Provider) WaitReady(context.Context, string) (provider.VPSInstance, error) {
	return provider.VPSInstance{}, ErrNotImplemented
}

// ListRegions is not implemented.
func (p *Provider) ListRegions(context.Context) ([]provider.Region, error) {
	return nil, ErrNotImplemented
}

// ListSizes is not implemented.
func (p *Provider) ListSizes(context.Context) ([]provider.Size, error) {
	return nil, ErrNotImplemented
}

// ListImages is not implemented.
func (p *Provider) ListImages(context.Context) ([]provider.Image, error) {
	return nil, ErrNotImplemented
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
