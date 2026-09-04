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

// fakeRegions stands in for godo.RegionsService. Like fakeDroplets it embeds
// the interface rather than stubbing it, so an unexpected call panics.
type fakeRegions struct {
	godo.RegionsService

	pages [][]godo.Region

	// failures are replies served before the pages are, which is how a 429
	// followed by a success is spelled out.
	failures []reply
	err      error

	requestedPages []int
	perPage        int

	slept []time.Duration
}

func (f *fakeRegions) List(_ context.Context, opt *godo.ListOptions) ([]godo.Region, *godo.Response, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	if len(f.failures) > 0 {
		r := f.failures[0]
		f.failures = f.failures[1:]
		return nil, r.resp, r.err
	}

	f.requestedPages = append(f.requestedPages, opt.Page)
	f.perPage = opt.PerPage

	idx := opt.Page - 1
	if idx < 0 || idx >= len(f.pages) {
		return nil, &godo.Response{Links: &godo.Links{}}, nil
	}

	links := &godo.Links{}
	if idx < len(f.pages)-1 {
		links.Pages = &godo.Pages{Next: "https://api.digitalocean.com/v2/regions?page=2"}
	}

	return f.pages[idx], &godo.Response{Links: links}, nil
}

func (f *fakeRegions) sleep(_ context.Context, d time.Duration) error {
	f.slept = append(f.slept, d)
	return nil
}

// newTestCatalogProvider wires a provider to the fake regions service, with
// waiting stubbed so a retry costs no real time.
func newTestCatalogProvider(f *fakeRegions) *Provider {
	p := newProvider(nil, f)
	p.sleep = f.sleep
	return p
}

func TestListRegions(t *testing.T) {
	f := &fakeRegions{pages: [][]godo.Region{{
		{Slug: "fra1", Name: "Frankfurt 1", Available: true},
		{Slug: "ams3", Name: "Amsterdam 3", Available: true},
		{Slug: "sfo1", Name: "San Francisco 1", Available: false},
	}}}

	got, err := newTestCatalogProvider(f).ListRegions(context.Background())
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

func TestListRegionsPaginates(t *testing.T) {
	f := &fakeRegions{pages: [][]godo.Region{
		{{Slug: "nyc1", Name: "New York 1", Available: true}},
		{{Slug: "ams3", Name: "Amsterdam 3", Available: true}},
	}}

	got, err := newTestCatalogProvider(f).ListRegions(context.Background())
	if err != nil {
		t.Fatalf("ListRegions: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d regions, want both pages: %+v", len(got), got)
	}
	if want := []int{1, 2}; !reflect.DeepEqual(f.requestedPages, want) {
		t.Errorf("requested pages %v, want %v", f.requestedPages, want)
	}
	if f.perPage != catalogPerPage {
		t.Errorf("per page = %d, want %d", f.perPage, catalogPerPage)
	}
}

func TestListRegionsEmpty(t *testing.T) {
	got, err := newTestCatalogProvider(&fakeRegions{}).ListRegions(context.Background())
	if err != nil {
		t.Fatalf("ListRegions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want no regions", got)
	}
}

// A catalog lookup is as rate limited as anything else, and it is the first
// call the wizard makes.
func TestListRegionsRetriesRateLimit(t *testing.T) {
	f := &fakeRegions{
		failures: []reply{failure(http.StatusTooManyRequests, "2")},
		pages:    [][]godo.Region{{{Slug: "fra1", Name: "Frankfurt 1", Available: true}}},
	}

	got, err := newTestCatalogProvider(f).ListRegions(context.Background())
	if err != nil {
		t.Fatalf("ListRegions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v, want the region from the second attempt", got)
	}
	if want := []time.Duration{2 * time.Second}; !reflect.DeepEqual(f.slept, want) {
		t.Errorf("slept %v, want %v (Retry-After)", f.slept, want)
	}
}

func TestListRegionsError(t *testing.T) {
	f := &fakeRegions{err: errors.New("no route to host")}

	_, err := newTestCatalogProvider(f).ListRegions(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "listing regions") {
		t.Errorf("error %q does not say what failed", err)
	}
}

// Sizes and images have no wizard step yet, and saying so beats a nil slice
// that looks like an account with nothing in it.
func TestCatalogLookupsNotImplemented(t *testing.T) {
	p := newTestCatalogProvider(&fakeRegions{})

	if _, err := p.ListSizes(context.Background()); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("ListSizes error = %v, want ErrNotImplemented", err)
	}
	if _, err := p.ListImages(context.Background()); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("ListImages error = %v, want ErrNotImplemented", err)
	}
}
