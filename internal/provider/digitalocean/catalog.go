package digitalocean

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/digitalocean/godo"

	"github.com/lestex/vpncli/internal/provider"
)

// catalogPerPage is the page size for catalog lookups. DigitalOcean returns
// well under a page of regions but rather more sizes, and the endpoints are
// paginated, so a hardcoded single request would silently truncate.
const catalogPerPage = 200

// catalog groups the read-only lookups the `vpncli init` wizard makes. They
// are separated from the droplets service because the create, inspect and
// delete path never touches them.
type catalog struct {
	regions godo.RegionsService
	sizes   godo.SizesService
	images  godo.ImagesService
	keys    godo.KeysService
}

// ListRegions returns every region in the account's catalog, sorted by slug.
// Unavailable ones are included: whether to offer a region that accepts no new
// droplets is the caller's call, and hiding it here would make an existing
// config's region look as though it had disappeared. The same goes for sizes.
func (p *Provider) ListRegions(ctx context.Context) ([]provider.Region, error) {
	regions, err := listAll(ctx, p, "regions", func(opt *godo.ListOptions) ([]godo.Region, *godo.Response, error) {
		return p.catalog.regions.List(ctx, opt)
	}, func(r godo.Region) provider.Region {
		return provider.Region{Slug: r.Slug, Name: r.Name, Available: r.Available}
	})
	if err != nil {
		return nil, err
	}

	slices.SortFunc(regions, func(a, b provider.Region) int {
		return strings.Compare(a.Slug, b.Slug)
	})
	return regions, nil
}

// ListSizes returns the account's droplet sizes, cheapest first. Which region
// a size can be created in is on the Size itself rather than filtered here,
// since the catalog is not told which region the caller has in mind.
func (p *Provider) ListSizes(ctx context.Context) ([]provider.Size, error) {
	sizes, err := listAll(ctx, p, "sizes", func(opt *godo.ListOptions) ([]godo.Size, *godo.Response, error) {
		return p.catalog.sizes.List(ctx, opt)
	}, func(s godo.Size) provider.Size {
		return provider.Size{
			Slug:         s.Slug,
			VCPUs:        s.Vcpus,
			MemoryMB:     s.Memory,
			DiskGB:       s.Disk,
			PriceMonthly: s.PriceMonthly,
			Available:    s.Available,
			Regions:      s.Regions,
		}
	})
	if err != nil {
		return nil, err
	}

	slices.SortFunc(sizes, func(a, b provider.Size) int {
		if a.PriceMonthly != b.PriceMonthly {
			return cmp.Compare(a.PriceMonthly, b.PriceMonthly)
		}
		// Sizes at the same price differ in what they spend it on. Ordering
		// them by slug is arbitrary but stable, which is what a numbered menu
		// needs.
		return strings.Compare(a.Slug, b.Slug)
	})
	return sizes, nil
}

// ListImages returns the public distribution images, grouped by distribution
// and newest first within one - the slugs carry the version, so ordering them
// backwards puts the current release at the top.
//
// Snapshots and one-click application images are not listed. A VPN server is
// bootstrapped from a stock OS, and an image that arrives with its own nginx
// is a worse starting point than one that does not.
func (p *Provider) ListImages(ctx context.Context) ([]provider.Image, error) {
	images, err := listAll(ctx, p, "images", func(opt *godo.ListOptions) ([]godo.Image, *godo.Response, error) {
		return p.catalog.images.ListDistribution(ctx, opt)
	}, func(i godo.Image) provider.Image {
		return provider.Image{Slug: i.Slug, Name: i.Name, Distribution: i.Distribution}
	})
	if err != nil {
		return nil, err
	}

	slices.SortFunc(images, func(a, b provider.Image) int {
		if a.Distribution != b.Distribution {
			return strings.Compare(a.Distribution, b.Distribution)
		}
		return strings.Compare(b.Slug, a.Slug)
	})
	return images, nil
}

// ListSSHKeys returns the public keys registered on the account, by name.
//
// Only keys already uploaded are offered. Creating one here would mean writing
// a private key to disk on the user's behalf, and a key they generated is one
// whose private half is already where their SSH agent expects it.
func (p *Provider) ListSSHKeys(ctx context.Context) ([]provider.SSHKey, error) {
	keys, err := listAll(ctx, p, "ssh keys", func(opt *godo.ListOptions) ([]godo.Key, *godo.Response, error) {
		return p.catalog.keys.List(ctx, opt)
	}, func(k godo.Key) provider.SSHKey {
		return provider.SSHKey{
			ID:          strconv.Itoa(k.ID),
			Name:        k.Name,
			Fingerprint: k.Fingerprint,
		}
	})
	if err != nil {
		return nil, err
	}

	slices.SortFunc(keys, func(a, b provider.SSHKey) int {
		return strings.Compare(a.Name, b.Name)
	})
	return keys, nil
}

// listAll walks every page of a catalog endpoint, converting each entry as it
// goes. The three lookups differ only in which service they call and what they
// convert to, and page handling is exactly the part worth writing once.
func listAll[T, R any](
	ctx context.Context,
	p *Provider,
	what string,
	list func(*godo.ListOptions) ([]T, *godo.Response, error),
	convert func(T) R,
) ([]R, error) {
	opt := &godo.ListOptions{Page: 1, PerPage: catalogPerPage}

	var out []R
	for {
		page, resp, err := call(ctx, p, transient, func() ([]T, *godo.Response, error) {
			return list(opt)
		})
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", what, err)
		}

		for _, entry := range page {
			out = append(out, convert(entry))
		}

		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			return out, nil
		}
		opt.Page++
	}
}
