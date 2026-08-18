package digitalocean

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/digitalocean/godo"

	"github.com/lestex/vpncli/internal/provider"
)

// fakeDroplets stands in for godo.DropletsService. The interface is embedded
// rather than stubbed, so an unexpected call panics instead of returning a
// zero value.
type fakeDroplets struct {
	godo.DropletsService

	pages [][]godo.Droplet
	err   error

	// requestedPages records the Page of every call.
	requestedPages []int
	perPage        int
}

func (f *fakeDroplets) List(_ context.Context, opt *godo.ListOptions) ([]godo.Droplet, *godo.Response, error) {
	if f.err != nil {
		return nil, nil, f.err
	}

	f.requestedPages = append(f.requestedPages, opt.Page)
	f.perPage = opt.PerPage

	idx := opt.Page - 1
	if idx < 0 || idx >= len(f.pages) {
		return nil, &godo.Response{Links: &godo.Links{}}, nil
	}

	// A non-empty Next makes godo report "not the last page".
	links := &godo.Links{}
	if idx < len(f.pages)-1 {
		links.Pages = &godo.Pages{Next: "https://api.digitalocean.com/v2/droplets?page=2"}
	}

	return f.pages[idx], &godo.Response{Links: links}, nil
}

func newTestProvider(f *fakeDroplets) *Provider {
	return &Provider{droplets: f}
}

func droplet(id int, name, status string) godo.Droplet {
	return godo.Droplet{
		ID:       id,
		Name:     name,
		Status:   status,
		SizeSlug: "s-1vcpu-1gb",
		Region:   &godo.Region{Slug: "fra1"},
		Image:    &godo.Image{Slug: "ubuntu-24-04-x64"},
		Created:  "2026-08-17T10:30:00Z",
		Networks: &godo.Networks{
			V4: []godo.NetworkV4{
				{Type: "private", IPAddress: "10.0.0.2"},
				{Type: "public", IPAddress: "203.0.113.10"},
			},
		},
	}
}

func TestName(t *testing.T) {
	if got := newTestProvider(&fakeDroplets{}).Name(); got != "digitalocean" {
		t.Errorf("Name() = %q, want %q", got, "digitalocean")
	}
}

func TestListInstances(t *testing.T) {
	ctx := context.Background()
	f := &fakeDroplets{pages: [][]godo.Droplet{{
		droplet(1001, "vpncli-fra1-a1b2", "active"),
	}}}

	got, err := newTestProvider(f).ListInstances(ctx)
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d instances, want 1", len(got))
	}

	want := provider.VPSInstance{
		ID:        "1001",
		Name:      "vpncli-fra1-a1b2",
		Provider:  "digitalocean",
		Region:    "fra1",
		Size:      "s-1vcpu-1gb",
		Image:     "ubuntu-24-04-x64",
		IPv4:      "203.0.113.10",
		Status:    provider.StatusActive,
		CreatedAt: time.Date(2026, 8, 17, 10, 30, 0, 0, time.UTC),
	}
	if got[0] != want {
		t.Errorf("conversion mismatch:\ngot  %+v\nwant %+v", got[0], want)
	}
}

func TestListInstancesEmptyAccount(t *testing.T) {
	got, err := newTestProvider(&fakeDroplets{}).ListInstances(context.Background())
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d instances, want 0", len(got))
	}
}

func TestListInstancesPaginates(t *testing.T) {
	f := &fakeDroplets{pages: [][]godo.Droplet{
		{droplet(1, "a", "active"), droplet(2, "b", "active")},
		{droplet(3, "c", "active")},
		{droplet(4, "d", "off")},
	}}

	got, err := newTestProvider(f).ListInstances(context.Background())
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d instances across 3 pages, want 4", len(got))
	}
	if got[0].ID != "1" || got[3].ID != "4" {
		t.Errorf("pages assembled out of order: %s ... %s", got[0].ID, got[3].ID)
	}

	wantPages := []int{1, 2, 3}
	if len(f.requestedPages) != len(wantPages) {
		t.Fatalf("requested pages %v, want %v", f.requestedPages, wantPages)
	}
	for i, page := range wantPages {
		if f.requestedPages[i] != page {
			t.Errorf("request %d asked for page %d, want %d", i, f.requestedPages[i], page)
		}
	}
	if f.perPage != dropletsPerPage {
		t.Errorf("per_page = %d, want %d", f.perPage, dropletsPerPage)
	}
}

func TestListInstancesPropagatesError(t *testing.T) {
	apiErr := errors.New("401 unauthorized")
	_, err := newTestProvider(&fakeDroplets{err: apiErr}).ListInstances(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, apiErr) {
		t.Errorf("error does not wrap the API error: %v", err)
	}
}

func TestToStatus(t *testing.T) {
	tests := []struct {
		droplet string
		want    provider.Status
	}{
		{"new", provider.StatusProvisioning},
		{"active", provider.StatusActive},
		{"off", provider.StatusStopped},
		{"archive", provider.StatusDeleting},
		{"", provider.StatusUnknown},
		{"something-new-from-the-api", provider.StatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.droplet, func(t *testing.T) {
			if got := toStatus(tt.droplet); got != tt.want {
				t.Errorf("toStatus(%q) = %q, want %q", tt.droplet, got, tt.want)
			}
		})
	}
}

func TestToInstanceHandlesIncompleteDroplets(t *testing.T) {
	// Incomplete droplets must still convert, not be dropped from the listing.
	tests := []struct {
		name    string
		droplet godo.Droplet
		check   func(*testing.T, provider.VPSInstance)
	}{
		{
			name:    "no networks yet",
			droplet: godo.Droplet{ID: 7, Status: "new"},
			check: func(t *testing.T, got provider.VPSInstance) {
				if got.IPv4 != "" {
					t.Errorf("IPv4 = %q, want empty", got.IPv4)
				}
				if got.Status != provider.StatusProvisioning {
					t.Errorf("Status = %q, want provisioning", got.Status)
				}
			},
		},
		{
			name:    "no public address among networks",
			droplet: godo.Droplet{ID: 8, Networks: &godo.Networks{V4: []godo.NetworkV4{{Type: "private", IPAddress: "10.0.0.5"}}}},
			check: func(t *testing.T, got provider.VPSInstance) {
				if got.IPv4 != "" {
					t.Errorf("IPv4 = %q, want empty, not the private address", got.IPv4)
				}
			},
		},
		{
			name:    "custom image has no slug",
			droplet: godo.Droplet{ID: 9, Image: &godo.Image{ID: 42, Name: "my-snapshot"}},
			check: func(t *testing.T, got provider.VPSInstance) {
				if got.Image != "my-snapshot" {
					t.Errorf("Image = %q, want the name as fallback", got.Image)
				}
			},
		},
		{
			name:    "image with neither slug nor name",
			droplet: godo.Droplet{ID: 10, Image: &godo.Image{ID: 42}},
			check: func(t *testing.T, got provider.VPSInstance) {
				if got.Image != "42" {
					t.Errorf("Image = %q, want the id as fallback", got.Image)
				}
			},
		},
		{
			name:    "unparsable creation time",
			droplet: godo.Droplet{ID: 11, Created: "not a timestamp"},
			check: func(t *testing.T, got provider.VPSInstance) {
				if !got.CreatedAt.IsZero() {
					t.Errorf("CreatedAt = %v, want zero", got.CreatedAt)
				}
			},
		},
		{
			name:    "no region",
			droplet: godo.Droplet{ID: 12},
			check: func(t *testing.T, got provider.VPSInstance) {
				if got.Region != "" {
					t.Errorf("Region = %q, want empty", got.Region)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toInstance(tt.droplet)
			if got.ID == "" {
				t.Error("ID was not set")
			}
			if got.Provider != Name {
				t.Errorf("Provider = %q, want %q", got.Provider, Name)
			}
			tt.check(t, got)
		})
	}
}

func TestNewRejectsEmptyToken(t *testing.T) {
	if _, err := New(""); !errors.Is(err, ErrNoToken) {
		t.Errorf("New(\"\") = %v, want ErrNoToken", err)
	}
	if _, err := New("dop_v1_token"); err != nil {
		t.Errorf("New with a token: %v", err)
	}
}

func TestTokenFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr bool
	}{
		{
			name: "primary variable",
			env:  map[string]string{"DIGITALOCEAN_TOKEN": "primary"},
			want: "primary",
		},
		{
			name: "doctl variable",
			env:  map[string]string{"DIGITALOCEAN_ACCESS_TOKEN": "doctl"},
			want: "doctl",
		},
		{
			name: "primary wins when both are set",
			env:  map[string]string{"DIGITALOCEAN_TOKEN": "primary", "DIGITALOCEAN_ACCESS_TOKEN": "doctl"},
			want: "primary",
		},
		{
			name:    "neither set",
			env:     map[string]string{},
			wantErr: true,
		},
		{
			name:    "set but empty is treated as unset",
			env:     map[string]string{"DIGITALOCEAN_TOKEN": ""},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DIGITALOCEAN_TOKEN", "")
			t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, err := TokenFromEnv()
			if tt.wantErr {
				if !errors.Is(err, ErrNoToken) {
					t.Fatalf("got %v, want ErrNoToken", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("TokenFromEnv: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnimplementedMethodsReportSo(t *testing.T) {
	ctx := context.Background()
	p := newTestProvider(&fakeDroplets{})

	tests := []struct {
		name string
		call func() error
	}{
		{"GetInstance", func() error { _, err := p.GetInstance(ctx, "1"); return err }},
		{"CreateInstance", func() error { _, err := p.CreateInstance(ctx, provider.CreateOptions{}); return err }},
		{"DeleteInstance", func() error { return p.DeleteInstance(ctx, "1") }},
		{"WaitReady", func() error { _, err := p.WaitReady(ctx, "1"); return err }},
		{"ListRegions", func() error { _, err := p.ListRegions(ctx); return err }},
		{"ListSizes", func() error { _, err := p.ListSizes(ctx); return err }},
		{"ListImages", func() error { _, err := p.ListImages(ctx); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, ErrNotImplemented) {
				t.Errorf("got %v, want ErrNotImplemented", err)
			}
		})
	}
}
