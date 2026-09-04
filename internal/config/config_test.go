package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	// Not having run the wizard yet is a normal state, not a failure.
	cfg, err := LoadFrom(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatalf("LoadFrom on missing file: %v", err)
	}
	if !reflect.DeepEqual(cfg, Config{}) {
		t.Errorf("got %+v, want zero Config", cfg)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")

	want := &Config{
		Provider:   "digitalocean",
		Region:     "fra1",
		Size:       "s-1vcpu-1gb",
		Image:      "ubuntu-24-04-x64",
		SSHKeyPath: "~/.ssh/id_ed25519",
		Reality: Reality{
			Dest:        "www.cloudflare.com:443",
			ServerNames: []string{"www.cloudflare.com"},
		},
	}
	if err := want.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	got, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got.Provider != want.Provider || got.Region != want.Region ||
		got.Size != want.Size || got.Image != want.Image || got.SSHKeyPath != want.SSHKeyPath {
		t.Errorf("scalar fields mismatch:\ngot  %+v\nwant %+v", got, want)
	}
	if got.Reality.Dest != want.Reality.Dest {
		t.Errorf("reality dest: got %q, want %q", got.Reality.Dest, want.Reality.Dest)
	}
	if len(got.Reality.ServerNames) != 1 || got.Reality.ServerNames[0] != "www.cloudflare.com" {
		t.Errorf("reality server_names: got %v", got.Reality.ServerNames)
	}
}

func TestSavePartialConfig(t *testing.T) {
	// The wizard writes after each stage, so a half-filled config must
	// survive a round trip with the unset fields simply absent.
	path := filepath.Join(t.TempDir(), "config.yaml")
	partial := &Config{Provider: "digitalocean", Region: "fra1"}
	if err := partial.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	got, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got.Provider != "digitalocean" || got.Region != "fra1" {
		t.Errorf("got %+v, want provider and region set", got)
	}
	if got.Size != "" || got.Image != "" || got.Reality.Dest != "" {
		t.Errorf("unset fields should stay empty, got %+v", got)
	}
}

func TestSaveUsesRestrictivePermissions(t *testing.T) {
	// v0.8.0 puts REALITY key material here; the mode should already be right.
	dir := filepath.Join(t.TempDir(), "vpncli")
	path := filepath.Join(dir, "config.yaml")
	if err := (&Config{Provider: "digitalocean"}).SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("config mode = %o, want 600", perm)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir mode = %o, want 700", perm)
	}
}

func TestSaveDoesNotLeaveTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := (&Config{Provider: "digitalocean"}).SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.yaml" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("leftover files after save: %v", names)
	}
}

func TestPathsFollowXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	t.Setenv("XDG_DATA_HOME", "/xdg/data")

	got, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if want := "/xdg/config/vpncli/config.yaml"; got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}

	gotDB, err := DatabasePath()
	if err != nil {
		t.Fatalf("DatabasePath: %v", err)
	}
	if want := "/xdg/data/vpncli/state.db"; gotDB != want {
		t.Errorf("DatabasePath() = %q, want %q", gotDB, want)
	}
}

func TestPathsFallBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}

	got, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if want := filepath.Join(home, ".config", "vpncli", "config.yaml"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}

	gotDB, err := DatabasePath()
	if err != nil {
		t.Fatalf("DatabasePath: %v", err)
	}
	if want := filepath.Join(home, ".local", "share", "vpncli", "state.db"); gotDB != want {
		t.Errorf("DatabasePath() = %q, want %q", gotDB, want)
	}
}

func TestCamouflage(t *testing.T) {
	got := Camouflage("www.apple.com")

	if got.Dest != "www.apple.com:443" {
		t.Errorf("dest = %q, want the host on 443", got.Dest)
	}
	if len(got.ServerNames) != 1 || got.ServerNames[0] != "www.apple.com" {
		t.Errorf("server names = %v, want the host: a client presents this as its SNI", got.ServerNames)
	}
}

func TestRealityHost(t *testing.T) {
	tests := []struct {
		dest string
		want string
	}{
		{dest: "www.apple.com:443", want: "www.apple.com"},
		// A config edited by hand may leave the port off.
		{dest: "www.apple.com", want: "www.apple.com"},
		{dest: "", want: ""},
	}

	for _, tt := range tests {
		if got := (Reality{Dest: tt.dest}).Host(); got != tt.want {
			t.Errorf("Reality{Dest: %q}.Host() = %q, want %q", tt.dest, got, tt.want)
		}
	}
}
