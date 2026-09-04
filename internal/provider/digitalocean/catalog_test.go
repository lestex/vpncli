package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/godo"

	"github.com/lestex/vpncli/internal/provider"
)

// pager serves scripted pages to any of the catalog fakes. It records what was
// asked for, and serves failures ahead of the pages so "429, then success" can
// be spelled out.
type pager[T any] struct {
	pages    [][]T
	failures []reply
	err      error

	requestedPages []int
	perPage        int
}

func (p *pager[T]) next(opt *godo.ListOptions, path string) ([]T, *godo.Response, error) {
	if p.err != nil {
		return nil, nil, p.err
	}
	if len(p.failures) > 0 {
		r := p.failures[0]
		p.failures = p.failures[1:]
		return nil, r.resp, r.err
	}

	p.requestedPages = append(p.requestedPages, opt.Page)
	p.perPage = opt.PerPage

	idx := opt.Page - 1
	if idx < 0 || idx >= len(p.pages) {
		return nil, &godo.Response{Links: &godo.Links{}}, nil
	}

	// A non-empty Next makes godo report "not the last page".
	links := &godo.Links{}
	if idx < len(p.pages)-1 {
		links.Pages = &godo.Pages{Next: "https://api.digitalocean.com/v2/" + path + "?page=2"}
	}

	return p.pages[idx], &godo.Response{Links: links}, nil
}

// The three fakes embed their godo interface rather than stubbing it, so a
// method nothing should be calling panics instead of returning a zero value.
type fakeRegions struct {
	godo.RegionsService
	pager[godo.Region]
}

func (f *fakeRegions) List(_ context.Context, opt *godo.ListOptions) ([]godo.Region, *godo.Response, error) {
	return f.next(opt, "regions")
}

type fakeSizes struct {
	godo.SizesService
	pager[godo.Size]
}

func (f *fakeSizes) List(_ context.Context, opt *godo.ListOptions) ([]godo.Size, *godo.Response, error) {
	return f.next(opt, "sizes")
}

type fakeImages struct {
	godo.ImagesService
	pager[godo.Image]

	// listedAll records a call to List, which returns snapshots and one-click
	// images alongside the distributions.
	listedAll bool
}

func (f *fakeImages) ListDistribution(_ context.Context, opt *godo.ListOptions) ([]godo.Image, *godo.Response, error) {
	return f.next(opt, "images")
}

func (f *fakeImages) List(_ context.Context, opt *godo.ListOptions) ([]godo.Image, *godo.Response, error) {
	f.listedAll = true
	return f.next(opt, "images")
}

// catalogProvider wires a provider to whichever catalog fakes a test supplies,
// with waiting stubbed so a retry costs no real time.
func catalogProvider(t *testing.T, c catalog) *Provider {
	t.Helper()

	p := newProvider(nil, c)
	p.sleep = func(context.Context, time.Duration) error { return nil }
	return p
}

func regionPages(pages ...[]godo.Region) *fakeRegions {
	return &fakeRegions{pager: pager[godo.Region]{pages: pages}}
}

func sizePages(pages ...[]godo.Size) *fakeSizes {
	return &fakeSizes{pager: pager[godo.Size]{pages: pages}}
}

func imagePages(pages ...[]godo.Image) *fakeImages {
	return &fakeImages{pager: pager[godo.Image]{pages: pages}}
}

type fakeKeys struct {
	godo.KeysService
	pager[godo.Key]
}

func (f *fakeKeys) List(_ context.Context, opt *godo.ListOptions) ([]godo.Key, *godo.Response, error) {
	return f.next(opt, "account/keys")
}

func keyPages(pages ...[]godo.Key) *fakeKeys {
	return &fakeKeys{pager: pager[godo.Key]{pages: pages}}
}

func TestListRegions(t *testing.T) {
	f := regionPages([]godo.Region{
		{Slug: "fra1", Name: "Frankfurt 1", Available: true},
		{Slug: "ams3", Name: "Amsterdam 3", Available: true},
		{Slug: "sfo1", Name: "San Francisco 1", Available: false},
	})

	got, err := catalogProvider(t, catalog{regions: f}).ListRegions(context.Background())
	if err != nil {
		t.Fatalf("ListRegions: %v", err)
	}

	// Sorted by slug, and the unavailable one is kept: filtering it is the
	// caller's decision, not the provider's.
	want := []provider.Region{
		{Slug: "ams3", Name: "Amsterdam 3", Available: true},
		{Slug: "fra1", Name: "Frankfurt 1", Available: true},
		{Slug: "sfo1", Name: "San Francisco 1", Available: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListRegions() = %+v, want %+v", got, want)
	}
}

func TestListSizes(t *testing.T) {
	f := sizePages([]godo.Size{
		{Slug: "s-1vcpu-2gb", Memory: 2048, Vcpus: 1, Disk: 50, PriceMonthly: 12, Available: true, Regions: []string{"fra1"}},
		{Slug: "s-1vcpu-512mb-10gb", Memory: 512, Vcpus: 1, Disk: 10, PriceMonthly: 4, Available: true, Regions: []string{"fra1", "ams3"}},
		{Slug: "s-1vcpu-1gb", Memory: 1024, Vcpus: 1, Disk: 25, PriceMonthly: 6, Available: false, Regions: []string{"fra1"}},
	})

	got, err := catalogProvider(t, catalog{sizes: f}).ListSizes(context.Background())
	if err != nil {
		t.Fatalf("ListSizes: %v", err)
	}

	want := []provider.Size{
		{Slug: "s-1vcpu-512mb-10gb", VCPUs: 1, MemoryMB: 512, DiskGB: 10, PriceMonthly: 4, Available: true, Regions: []string{"fra1", "ams3"}},
		{Slug: "s-1vcpu-1gb", VCPUs: 1, MemoryMB: 1024, DiskGB: 25, PriceMonthly: 6, Regions: []string{"fra1"}},
		{Slug: "s-1vcpu-2gb", VCPUs: 1, MemoryMB: 2048, DiskGB: 50, PriceMonthly: 12, Available: true, Regions: []string{"fra1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListSizes() = %+v, want cheapest first: %+v", got, want)
	}
}

// A menu whose order moves between runs would move the answers with it.
func TestListSizesOrdersTiesBySlug(t *testing.T) {
	f := sizePages([]godo.Size{
		{Slug: "s-2vcpu-2gb", PriceMonthly: 18},
		{Slug: "c-2", PriceMonthly: 18},
		{Slug: "m-1vcpu-8gb", PriceMonthly: 18},
	})

	got, err := catalogProvider(t, catalog{sizes: f}).ListSizes(context.Background())
	if err != nil {
		t.Fatalf("ListSizes: %v", err)
	}

	want := []string{"c-2", "m-1vcpu-8gb", "s-2vcpu-2gb"}
	for i, slug := range want {
		if got[i].Slug != slug {
			t.Errorf("size %d = %q, want %q (%+v)", i, got[i].Slug, slug, got)
		}
	}
}

func TestListImages(t *testing.T) {
	f := imagePages([]godo.Image{
		{Slug: "ubuntu-22-04-x64", Name: "22.04 (LTS) x64", Distribution: "Ubuntu"},
		{Slug: "debian-12-x64", Name: "12 x64", Distribution: "Debian"},
		{Slug: "ubuntu-24-04-x64", Name: "24.04 (LTS) x64", Distribution: "Ubuntu"},
		{Slug: "debian-13-x64", Name: "13 x64", Distribution: "Debian"},
	})

	got, err := catalogProvider(t, catalog{images: f}).ListImages(context.Background())
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}

	// Grouped by distribution, newest first inside one.
	want := []provider.Image{
		{Slug: "debian-13-x64", Name: "13 x64", Distribution: "Debian"},
		{Slug: "debian-12-x64", Name: "12 x64", Distribution: "Debian"},
		{Slug: "ubuntu-24-04-x64", Name: "24.04 (LTS) x64", Distribution: "Ubuntu"},
		{Slug: "ubuntu-22-04-x64", Name: "22.04 (LTS) x64", Distribution: "Ubuntu"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListImages() = %+v, want %+v", got, want)
	}
}

// Snapshots and one-click images are a worse starting point than a stock OS,
// so only the distribution listing is asked for.
func TestListImagesAsksForDistributionsOnly(t *testing.T) {
	f := imagePages([]godo.Image{{Slug: "ubuntu-24-04-x64", Distribution: "Ubuntu"}})

	if _, err := catalogProvider(t, catalog{images: f}).ListImages(context.Background()); err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if f.listedAll {
		t.Error("ListImages asked for every image, want the distributions only")
	}
}

func TestCatalogPaginates(t *testing.T) {
	regions := regionPages(
		[]godo.Region{{Slug: "nyc1", Available: true}},
		[]godo.Region{{Slug: "ams3", Available: true}},
	)
	sizes := sizePages(
		[]godo.Size{{Slug: "s-1vcpu-1gb", PriceMonthly: 6}},
		[]godo.Size{{Slug: "s-1vcpu-512mb-10gb", PriceMonthly: 4}},
	)
	images := imagePages(
		[]godo.Image{{Slug: "ubuntu-24-04-x64", Distribution: "Ubuntu"}},
		[]godo.Image{{Slug: "debian-13-x64", Distribution: "Debian"}},
	)
	p := catalogProvider(t, catalog{regions: regions, sizes: sizes, images: images})
	ctx := context.Background()

	gotRegions, err := p.ListRegions(ctx)
	if err != nil {
		t.Fatalf("ListRegions: %v", err)
	}
	gotSizes, err := p.ListSizes(ctx)
	if err != nil {
		t.Fatalf("ListSizes: %v", err)
	}
	gotImages, err := p.ListImages(ctx)
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}

	if len(gotRegions) != 2 || len(gotSizes) != 2 || len(gotImages) != 2 {
		t.Fatalf("got %d regions, %d sizes, %d images, want both pages of each",
			len(gotRegions), len(gotSizes), len(gotImages))
	}

	// The second page is only reachable if sorting happens after the walk.
	if gotSizes[0].Slug != "s-1vcpu-512mb-10gb" {
		t.Errorf("sizes = %+v, want the cheapest across both pages first", gotSizes)
	}

	want := []int{1, 2}
	for _, tt := range []struct {
		what  string
		pages []int
		per   int
	}{
		{"regions", regions.requestedPages, regions.perPage},
		{"sizes", sizes.requestedPages, sizes.perPage},
		{"images", images.requestedPages, images.perPage},
	} {
		if !reflect.DeepEqual(tt.pages, want) {
			t.Errorf("%s requested pages %v, want %v", tt.what, tt.pages, want)
		}
		if tt.per != catalogPerPage {
			t.Errorf("%s per page = %d, want %d", tt.what, tt.per, catalogPerPage)
		}
	}
}

func TestCatalogEmpty(t *testing.T) {
	p := catalogProvider(t, catalog{regions: regionPages(), sizes: sizePages(), images: imagePages()})
	ctx := context.Background()

	regions, err := p.ListRegions(ctx)
	if err != nil || len(regions) != 0 {
		t.Errorf("ListRegions() = %+v, %v, want none", regions, err)
	}
	sizes, err := p.ListSizes(ctx)
	if err != nil || len(sizes) != 0 {
		t.Errorf("ListSizes() = %+v, %v, want none", sizes, err)
	}
	images, err := p.ListImages(ctx)
	if err != nil || len(images) != 0 {
		t.Errorf("ListImages() = %+v, %v, want none", images, err)
	}
}

// A catalog lookup is as rate limited as anything else, and it is the first
// call the wizard makes.
func TestListRegionsRetriesRateLimit(t *testing.T) {
	f := regionPages([]godo.Region{{Slug: "fra1", Name: "Frankfurt 1", Available: true}})
	f.failures = []reply{failure(http.StatusTooManyRequests, "2")}

	var slept []time.Duration
	p := catalogProvider(t, catalog{regions: f})
	p.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}

	got, err := p.ListRegions(context.Background())
	if err != nil {
		t.Fatalf("ListRegions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v, want the region from the second attempt", got)
	}
	if want := []time.Duration{2 * time.Second}; !reflect.DeepEqual(slept, want) {
		t.Errorf("slept %v, want %v (Retry-After)", slept, want)
	}
}

// Whichever lookup failed, the error has to say which one it was: the wizard
// makes three in a row.
func TestCatalogErrorsNameTheLookup(t *testing.T) {
	boom := errors.New("no route to host")
	ctx := context.Background()

	regions := regionPages()
	regions.err = boom
	sizes := sizePages()
	sizes.err = boom
	images := imagePages()
	images.err = boom
	p := catalogProvider(t, catalog{regions: regions, sizes: sizes, images: images})

	tests := []struct {
		what string
		call func() error
	}{
		{"regions", func() error { _, err := p.ListRegions(ctx); return err }},
		{"sizes", func() error { _, err := p.ListSizes(ctx); return err }},
		{"images", func() error { _, err := p.ListImages(ctx); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.what, func(t *testing.T) {
			err := tt.call()
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "listing "+tt.what) {
				t.Errorf("error %q does not say which lookup failed", err)
			}
			if !errors.Is(err, boom) {
				t.Errorf("error %q does not wrap the cause", err)
			}
		})
	}
}

func TestListSSHKeys(t *testing.T) {
	f := keyPages([]godo.Key{
		{ID: 22, Name: "workstation", Fingerprint: "dd:ee:ff"},
		{ID: 11, Name: "laptop", Fingerprint: "aa:bb:cc"},
	})

	got, err := catalogProvider(t, catalog{keys: f}).ListSSHKeys(context.Background())
	if err != nil {
		t.Fatalf("ListSSHKeys: %v", err)
	}

	// By name, because that is what the wizard offers them by, and the ID is a
	// string because providers disagree on the type.
	want := []provider.SSHKey{
		{ID: "11", Name: "laptop", Fingerprint: "aa:bb:cc"},
		{ID: "22", Name: "workstation", Fingerprint: "dd:ee:ff"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListSSHKeys() = %+v, want %+v", got, want)
	}
}

// An account with no keys is a question the wizard has to answer, not an error
// to report from here.
func TestListSSHKeysEmpty(t *testing.T) {
	got, err := catalogProvider(t, catalog{keys: keyPages()}).ListSSHKeys(context.Background())
	if err != nil || len(got) != 0 {
		t.Errorf("ListSSHKeys() = %+v, %v, want none", got, err)
	}
}
