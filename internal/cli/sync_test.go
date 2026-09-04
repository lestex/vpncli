package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/lestex/vpncli/internal/config"
	"github.com/lestex/vpncli/internal/manager"
	"github.com/lestex/vpncli/internal/provider"
	"github.com/lestex/vpncli/internal/provider/digitalocean"
	"github.com/lestex/vpncli/internal/state"
)

func TestSyncIsRegistered(t *testing.T) {
	out := run(t, "--help")
	if !strings.Contains(out, "sync") {
		t.Errorf("sync is missing from root help:\n%s", out)
	}
}

// Unlike `list`, sync talks to the API, so it should say which variables it
// wants rather than fail somewhere inside a request.
func TestSyncRequiresToken(t *testing.T) {
	withStateDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")

	_, err := execute("sync")
	if !errors.Is(err, digitalocean.ErrNoToken) {
		t.Fatalf("got %v, want ErrNoToken", err)
	}
}

func TestSyncRejectsArgs(t *testing.T) {
	withStateDir(t)

	if _, err := execute("sync", "unexpected"); err == nil {
		t.Error("expected an error for an unexpected positional argument")
	}
}

func TestPrintSyncResult(t *testing.T) {
	tests := []struct {
		name   string
		result manager.SyncResult
		want   []string
		absent []string
	}{
		{
			name:   "nothing to do",
			result: manager.SyncResult{Unchanged: 3},
			want:   []string{"already up to date", "3 servers"},
		},
		{
			name:   "one unchanged server is singular",
			result: manager.SyncResult{Unchanged: 1},
			want:   []string{"1 server"},
			absent: []string{"1 servers"},
		},
		{
			name:   "nothing at all",
			result: manager.SyncResult{},
			want:   []string{"already up to date", "0 servers"},
		},
		{
			name: "every kind of change",
			result: manager.SyncResult{
				Adopted:   []state.Server{{ID: 1}},
				Updated:   []state.Server{{ID: 2}, {ID: 3}},
				Removed:   []state.Server{{ID: 4}},
				Unchanged: 5,
			},
			want: []string{"1 adopted", "2 updated", "1 removed", "5 unchanged"},
		},
		{
			// Zero counts are noise; only what happened is worth a line.
			name:   "only what changed is listed",
			result: manager.SyncResult{Removed: []state.Server{{ID: 1}}},
			want:   []string{"1 removed"},
			absent: []string{"adopted", "updated", "unchanged"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := printSyncResult(&buf, tt.result); err != nil {
				t.Fatalf("printSyncResult: %v", err)
			}

			out := buf.String()
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("summary is missing %q: %q", want, out)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(out, absent) {
					t.Errorf("summary should not mention %q: %q", absent, out)
				}
			}
		})
	}
}

func TestOpenProvider(t *testing.T) {
	t.Setenv("DIGITALOCEAN_TOKEN", "dop_v1_token")

	tests := []struct {
		name    string
		cfg     config.Config
		wantErr string
	}{
		// An unset field means `vpncli init` has not been run yet.
		{name: "unset falls back to the only implementation"},
		{name: "named explicitly", cfg: config.Config{Provider: digitalocean.Name}},
		{name: "not implemented yet", cfg: config.Config{Provider: "hetzner"}, wantErr: "hetzner"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vps, err := openProvider(tt.cfg)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("got provider %v, want an error", vps)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not name %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("openProvider: %v", err)
			}
			if vps.Name() != digitalocean.Name {
				t.Errorf("got provider %q, want %q", vps.Name(), digitalocean.Name)
			}
		})
	}
}

// The sync help should tell the user which servers it will and will not touch,
// since adopting the wrong one would put it under `vpncli destroy`.
func TestSyncHelpExplainsTagging(t *testing.T) {
	out := run(t, "sync", "--help")
	for _, want := range []string{provider.ManagedTag, "adopted", "tag"} {
		if !strings.Contains(out, want) {
			t.Errorf("sync help does not mention %q:\n%s", want, out)
		}
	}
}
