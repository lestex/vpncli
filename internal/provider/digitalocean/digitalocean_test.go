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

// reply is one scripted answer from the fake. Tests queue several to spell out
// a sequence like "429, then success".
type reply struct {
	droplet *godo.Droplet
	resp    *godo.Response
	err     error
}

// ok is a successful reply carrying d.
func ok(d *godo.Droplet) reply {
	return reply{droplet: d, resp: &godo.Response{Response: &http.Response{StatusCode: http.StatusOK}}}
}

// failure is the response and error pair godo hands back for an API error.
// retryAfter is written into the header when non-empty.
func failure(code int, retryAfter string) reply {
	httpResp := &http.Response{StatusCode: code, Header: http.Header{}}
	if retryAfter != "" {
		httpResp.Header.Set("Retry-After", retryAfter)
	}
	return reply{
		resp: &godo.Response{Response: httpResp},
		err:  &godo.ErrorResponse{Response: httpResp, Message: http.StatusText(code)},
	}
}

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

	// Scripted replies, consumed in order by the matching method.
	gets    []reply
	creates []reply
	deletes []reply

	// What each method was actually asked for.
	getIDs     []int
	createReqs []*godo.DropletCreateRequest
	deleteIDs  []int

	// slept records what every backoff and poll would have cost, without
	// spending it. sleepErr stands in for a context that ended mid-wait.
	slept    []time.Duration
	sleepErr error

	// sshRefusals is how many SSH probes are refused before one connects,
	// which is how a droplet behaves between "active" and a listening sshd.
	sshRefusals int
	sshDials    int
}

// next pops the script's next reply. Running out is a bug in the test, not
// something to paper over with a zero value.
func (f *fakeDroplets) next(script *[]reply) reply {
	if len(*script) == 0 {
		panic("fakeDroplets: unscripted call")
	}

	r := (*script)[0]
	*script = (*script)[1:]
	return r
}

func (f *fakeDroplets) Get(_ context.Context, id int) (*godo.Droplet, *godo.Response, error) {
	f.getIDs = append(f.getIDs, id)
	r := f.next(&f.gets)
	return r.droplet, r.resp, r.err
}

func (f *fakeDroplets) Create(_ context.Context, req *godo.DropletCreateRequest) (*godo.Droplet, *godo.Response, error) {
	f.createReqs = append(f.createReqs, req)
	r := f.next(&f.creates)
	return r.droplet, r.resp, r.err
}

func (f *fakeDroplets) Delete(_ context.Context, id int) (*godo.Response, error) {
	f.deleteIDs = append(f.deleteIDs, id)
	r := f.next(&f.deletes)
	return r.resp, r.err
}

func (f *fakeDroplets) sleep(_ context.Context, d time.Duration) error {
	f.slept = append(f.slept, d)
	return f.sleepErr
}

func (f *fakeDroplets) dial(context.Context, string) error {
	f.sshDials++
	if f.sshDials <= f.sshRefusals {
		return errors.New("connection refused")
	}
	return nil
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

// newTestProvider wires a provider to the fake droplets service, with waiting
// and dialing stubbed: a test must spend no real time and open no real
// sockets. The catalog is left empty, since nothing on this path reads it -
// catalog_test.go wires that half.
func newTestProvider(f *fakeDroplets) *Provider {
	p := newProvider(f, catalog{})
	p.sleep = f.sleep
	p.dialSSH = f.dial
	return p
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
		Tags:     []string{provider.ManagedTag},
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
		Tags:      []string{provider.ManagedTag},
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("conversion mismatch:\ngot  %+v\nwant %+v", got[0], want)
	}
	if !got[0].Managed() {
		t.Error("a tagged droplet should read as managed")
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

func TestGetInstance(t *testing.T) {
	d := droplet(1001, "vpncli-fra1-a1b2", "active")
	f := &fakeDroplets{gets: []reply{ok(&d)}}

	got, err := newTestProvider(f).GetInstance(context.Background(), "1001")
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if got.ID != "1001" || got.IPv4 != "203.0.113.10" {
		t.Errorf("got %+v, want droplet 1001 at 203.0.113.10", got)
	}
	if len(f.getIDs) != 1 || f.getIDs[0] != 1001 {
		t.Errorf("asked for %v, want [1001]", f.getIDs)
	}
}

func TestGetInstanceNotFound(t *testing.T) {
	f := &fakeDroplets{gets: []reply{failure(http.StatusNotFound, "")}}

	_, err := newTestProvider(f).GetInstance(context.Background(), "1001")
	if !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("got %v, want provider.ErrNotFound", err)
	}
}

// A 200 carrying no droplet must not read as a server with no address.
func TestGetInstanceEmptyBody(t *testing.T) {
	f := &fakeDroplets{gets: []reply{ok(nil)}}

	if _, err := newTestProvider(f).GetInstance(context.Background(), "1001"); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("got %v, want provider.ErrNotFound", err)
	}
}

// An ID that was never a droplet ID cannot name a live droplet, so `sync` gets
// the same answer it would for a deleted one - and no request goes out.
func TestGetInstanceRejectsMalformedIDs(t *testing.T) {
	for _, id := range []string{"", "abc", "0", "-3", "10.5", " 1001"} {
		t.Run(id, func(t *testing.T) {
			f := &fakeDroplets{}
			if _, err := newTestProvider(f).GetInstance(context.Background(), id); !errors.Is(err, provider.ErrNotFound) {
				t.Errorf("GetInstance(%q) = %v, want provider.ErrNotFound", id, err)
			}
			if len(f.getIDs) != 0 {
				t.Errorf("%q reached the API as %v", id, f.getIDs)
			}
		})
	}
}

func TestCreateInstance(t *testing.T) {
	d := droplet(1001, "vpncli-fra1-a1b2", "new")
	f := &fakeDroplets{creates: []reply{ok(&d)}}

	got, err := newTestProvider(f).CreateInstance(context.Background(), provider.CreateOptions{
		Name:      "vpncli-fra1-a1b2",
		Region:    "fra1",
		Size:      "s-1vcpu-1gb",
		Image:     "ubuntu-24-04-x64",
		SSHKeyIDs: []string{"aa:bb:cc"},
		Tags:      []string{"vpncli"},
	})
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	if got.ID != "1001" || got.Status != provider.StatusProvisioning {
		t.Errorf("got %+v, want droplet 1001 provisioning", got)
	}

	if len(f.createReqs) != 1 {
		t.Fatalf("made %d create calls, want 1", len(f.createReqs))
	}
	req := f.createReqs[0]
	if req.Name != "vpncli-fra1-a1b2" || req.Region != "fra1" || req.Size != "s-1vcpu-1gb" {
		t.Errorf("request = %+v, want the options passed through", req)
	}
	if req.Image.Slug != "ubuntu-24-04-x64" || req.Image.ID != 0 {
		t.Errorf("Image = %+v, want the slug", req.Image)
	}
	if len(req.SSHKeys) != 1 || req.SSHKeys[0].Fingerprint != "aa:bb:cc" {
		t.Errorf("SSHKeys = %+v, want one fingerprint", req.SSHKeys)
	}
	if len(req.Tags) != 1 || req.Tags[0] != "vpncli" {
		t.Errorf("Tags = %v, want [vpncli]", req.Tags)
	}
	if req.WithDropletAgent == nil || *req.WithDropletAgent {
		t.Error("the metrics agent should be turned off explicitly")
	}
	// Nothing is enabled that was not asked for - backups cost money, and
	// IPv6 is the caller's decision because a client config has to match it.
	if req.Backups || req.IPv6 {
		t.Errorf("unrequested extras enabled: backups=%v ipv6=%v", req.Backups, req.IPv6)
	}
}

// A server without IPv6 makes every client's IPv6 attempt a round trip that
// ends in a refusal, so asking for it has to reach the request.
func TestCreateInstanceAsksForIPv6(t *testing.T) {
	d := droplet(1001, "vpncli-fra1-a1b2", "new")
	f := &fakeDroplets{creates: []reply{ok(&d)}}

	_, err := newTestProvider(f).CreateInstance(context.Background(), provider.CreateOptions{
		Name:      "vpncli-fra1-a1b2",
		Region:    "fra1",
		Size:      "s-1vcpu-1gb",
		Image:     "ubuntu-24-04-x64",
		SSHKeyIDs: []string{"aa:bb:cc"},
		IPv6:      true,
	})
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	if !f.createReqs[0].IPv6 {
		t.Error("IPv6 was requested and the create call does not ask for it")
	}
}

// Numeric values name DigitalOcean's own IDs; anything else is a slug or a
// fingerprint.
func TestCreateInstanceNumericImageAndKey(t *testing.T) {
	d := droplet(1001, "vpncli-fra1-a1b2", "new")
	f := &fakeDroplets{creates: []reply{ok(&d)}}

	_, err := newTestProvider(f).CreateInstance(context.Background(), provider.CreateOptions{
		Name:      "vpncli-fra1-a1b2",
		Region:    "fra1",
		Size:      "s-1vcpu-1gb",
		Image:     "1234567",
		SSHKeyIDs: []string{"7654321"},
	})
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	req := f.createReqs[0]
	if req.Image.ID != 1234567 || req.Image.Slug != "" {
		t.Errorf("Image = %+v, want ID 1234567", req.Image)
	}
	if req.SSHKeys[0].ID != 7654321 || req.SSHKeys[0].Fingerprint != "" {
		t.Errorf("SSHKeys[0] = %+v, want ID 7654321", req.SSHKeys[0])
	}
}

func TestCreateInstanceValidatesOptions(t *testing.T) {
	complete := provider.CreateOptions{
		Name:      "vpncli-fra1-a1b2",
		Region:    "fra1",
		Size:      "s-1vcpu-1gb",
		Image:     "ubuntu-24-04-x64",
		SSHKeyIDs: []string{"aa:bb:cc"},
	}

	tests := []struct {
		name  string
		strip func(*provider.CreateOptions)
		want  string
	}{
		{"no name", func(o *provider.CreateOptions) { o.Name = "" }, "name"},
		{"no region", func(o *provider.CreateOptions) { o.Region = "" }, "region"},
		{"no size", func(o *provider.CreateOptions) { o.Size = "" }, "size"},
		{"no image", func(o *provider.CreateOptions) { o.Image = "" }, "image"},
		{"no ssh key", func(o *provider.CreateOptions) { o.SSHKeyIDs = nil }, "ssh key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := complete
			tt.strip(&opts)

			f := &fakeDroplets{}
			_, err := newTestProvider(f).CreateInstance(context.Background(), opts)
			if !errors.Is(err, ErrInvalidOptions) {
				t.Fatalf("got %v, want ErrInvalidOptions", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not name the missing %q", err, tt.want)
			}
			if len(f.createReqs) != 0 {
				t.Error("an incomplete request still reached the API")
			}
		})
	}
}

// A 429 was rejected before it reached anything, so the create can be repeated.
func TestCreateInstanceRetriesRateLimit(t *testing.T) {
	d := droplet(1001, "vpncli-fra1-a1b2", "new")
	f := &fakeDroplets{creates: []reply{failure(http.StatusTooManyRequests, ""), ok(&d)}}

	_, err := newTestProvider(f).CreateInstance(context.Background(), validOptions())
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	if len(f.createReqs) != 2 {
		t.Errorf("made %d attempts, want 2", len(f.createReqs))
	}
}

// A 500 may mean the droplet exists and only the reply was lost. Retrying
// would strand a second one, billed and untracked.
func TestCreateInstanceDoesNotRetryServerErrors(t *testing.T) {
	f := &fakeDroplets{creates: []reply{failure(http.StatusInternalServerError, "")}}

	if _, err := newTestProvider(f).CreateInstance(context.Background(), validOptions()); err == nil {
		t.Fatal("expected an error")
	}
	if len(f.createReqs) != 1 {
		t.Errorf("made %d attempts, want 1", len(f.createReqs))
	}
}

func validOptions() provider.CreateOptions {
	return provider.CreateOptions{
		Name:      "vpncli-fra1-a1b2",
		Region:    "fra1",
		Size:      "s-1vcpu-1gb",
		Image:     "ubuntu-24-04-x64",
		SSHKeyIDs: []string{"aa:bb:cc"},
	}
}

func TestDeleteInstance(t *testing.T) {
	f := &fakeDroplets{deletes: []reply{{resp: &godo.Response{Response: &http.Response{StatusCode: http.StatusNoContent}}}}}

	if err := newTestProvider(f).DeleteInstance(context.Background(), "1001"); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if len(f.deleteIDs) != 1 || f.deleteIDs[0] != 1001 {
		t.Errorf("deleted %v, want [1001]", f.deleteIDs)
	}
}

func TestDeleteInstanceNotFound(t *testing.T) {
	f := &fakeDroplets{deletes: []reply{failure(http.StatusNotFound, "")}}

	if err := newTestProvider(f).DeleteInstance(context.Background(), "1001"); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("got %v, want provider.ErrNotFound", err)
	}
}

func TestDeleteInstanceRejectsMalformedIDs(t *testing.T) {
	f := &fakeDroplets{}
	if err := newTestProvider(f).DeleteInstance(context.Background(), "not-an-id"); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("got %v, want provider.ErrNotFound", err)
	}
	if len(f.deleteIDs) != 0 {
		t.Errorf("a malformed ID reached the API as %v", f.deleteIDs)
	}
}

// The full boot sequence: building, then active but addressless, then
// addressed but with sshd not up yet, then usable.
func TestWaitReady(t *testing.T) {
	building := droplet(1001, "vpncli-fra1-a1b2", "new")
	building.Networks = nil

	addressless := droplet(1001, "vpncli-fra1-a1b2", "active")
	addressless.Networks = nil

	ready := droplet(1001, "vpncli-fra1-a1b2", "active")

	f := &fakeDroplets{
		gets:        []reply{ok(&building), ok(&addressless), ok(&ready), ok(&ready)},
		sshRefusals: 1,
	}

	got, err := newTestProvider(f).WaitReady(context.Background(), "1001")
	if err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if got.IPv4 != "203.0.113.10" || got.Status != provider.StatusActive {
		t.Errorf("got %+v, want an active droplet with an address", got)
	}
	if len(f.getIDs) != 4 {
		t.Errorf("polled %d times, want 4", len(f.getIDs))
	}
	if f.sshDials != 2 {
		t.Errorf("dialed %d times, want 2 - the addressless poll must not dial", f.sshDials)
	}
	if len(f.slept) != 3 {
		t.Errorf("slept %d times, want one per unready poll", len(f.slept))
	}
	for _, d := range f.slept {
		if d != defaultPollInterval {
			t.Errorf("slept %v between polls, want %v", d, defaultPollInterval)
		}
	}
}

// A droplet on its way out is not something more polling will fix.
func TestWaitReadyFailsOnTerminalState(t *testing.T) {
	d := droplet(1001, "vpncli-fra1-a1b2", "archive")
	f := &fakeDroplets{gets: []reply{ok(&d)}}

	if _, err := newTestProvider(f).WaitReady(context.Background(), "1001"); err == nil {
		t.Fatal("a droplet being torn down should not read as ready")
	}
	if f.sshDials != 0 {
		t.Error("a doomed droplet should not be dialed")
	}
}

func TestWaitReadyStopsWhenContextEnds(t *testing.T) {
	building := droplet(1001, "vpncli-fra1-a1b2", "new")
	f := &fakeDroplets{
		gets:     []reply{ok(&building)},
		sleepErr: context.DeadlineExceeded,
	}

	_, err := newTestProvider(f).WaitReady(context.Background(), "1001")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want context.DeadlineExceeded", err)
	}
}

// A droplet that stays active without ever answering on 22 is a real failure -
// and one that bills for as long as it is waited on, so the wait ends.
func TestWaitReadyGivesUp(t *testing.T) {
	d := droplet(1001, "vpncli-fra1-a1b2", "active")
	f := &fakeDroplets{
		gets:        []reply{ok(&d), ok(&d)},
		sshRefusals: 100,
	}

	p := newTestProvider(f)
	// Two polls' worth of readyTimeout, so the cap is reached without
	// scripting the whole ten minutes.
	p.pollInterval = readyTimeout / 2

	_, err := p.WaitReady(context.Background(), "1001")
	if err == nil {
		t.Fatal("a droplet that never answers should not be waited on forever")
	}
	if !strings.Contains(err.Error(), "destroy") {
		t.Errorf("got %v, want an error that says how to get rid of the droplet", err)
	}
	if len(f.gets) != 0 {
		t.Errorf("gave up with %d polls left unspent", len(f.gets))
	}
}

func TestWaitReadyPropagatesNotFound(t *testing.T) {
	f := &fakeDroplets{gets: []reply{failure(http.StatusNotFound, "")}}

	if _, err := newTestProvider(f).WaitReady(context.Background(), "1001"); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("got %v, want provider.ErrNotFound", err)
	}
}
