package digitalocean

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/digitalocean/godo"

	"github.com/lestex/vpncli/internal/provider"
)

// catalogPerPage is the page size for catalog lookups. DigitalOcean returns
// well under a page of regions, but the endpoint is paginated and a hardcoded
// single request would silently truncate the day that changes.
const catalogPerPage = 200

// ListRegions returns every region in the account's catalog, sorted by slug.
// Unavailable ones are included: whether to offer a region that accepts no new
// droplets is the caller's call, and hiding it here would make an existing
// config's region look as though it had disappeared.
func (p *Provider) ListRegions(ctx context.Context) ([]provider.Region, error) {
	opt := &godo.ListOptions{Page: 1, PerPage: catalogPerPage}

	var regions []provider.Region
	for {
		page, resp, err := call(ctx, p, transient, func() ([]godo.Region, *godo.Response, error) {
			return p.regions.List(ctx, opt)
		})
		if err != nil {
			return nil, fmt.Errorf("listing regions: %w", err)
		}

		for _, region := range page {
			regions = append(regions, provider.Region{
				Slug:      region.Slug,
				Name:      region.Name,
				Available: region.Available,
			})
		}

		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			slices.SortFunc(regions, func(a, b provider.Region) int {
				return strings.Compare(a.Slug, b.Slug)
			})
			return regions, nil
		}
		opt.Page++
	}
}

// ListSizes is not implemented.
func (p *Provider) ListSizes(context.Context) ([]provider.Size, error) {
	return nil, ErrNotImplemented
}

// ListImages is not implemented.
func (p *Provider) ListImages(context.Context) ([]provider.Image, error) {
	return nil, ErrNotImplemented
}
