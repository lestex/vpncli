package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/lestex/vpncli/internal/config"
	"github.com/lestex/vpncli/internal/ssh"
	"github.com/lestex/vpncli/internal/state"
)

// fakeSSH is a server that answers well enough for the bootstrap to finish.
// What the bootstrap actually runs is the bootstrap package's business; what
// matters here is that the command wires it up and records the result.
type fakeSSH struct {
	commands []string
	uploads  map[string]string
	hostKey  string

	failOn  string
	failErr error
	closed  bool
}

func newFakeSSH() *fakeSSH {
	return &fakeSSH{
		uploads: map[string]string{},
		hostKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeFakeFakeFakeFakeFakeFakeFakeFake",
	}
}

func (f *fakeSSH) Run(_ context.Context, command string) (string, error) {
	f.commands = append(f.commands, command)

	if f.failOn != "" && strings.Contains(command, f.failOn) {
		return "", f.failErr
	}
	switch {
	case strings.HasPrefix(command, "uname -m"):
		return "x86_64\n", nil
	case strings.HasPrefix(command, "sysctl -n"):
		return "bbr\n", nil
	case strings.HasPrefix(command, "systemctl is-active xray"):
		return "active\n", nil
	}
	return "", nil
}

func (f *fakeSSH) Upload(_ context.Context, path string, _ os.FileMode, content []byte) error {
	f.uploads[path] = string(content)
	return nil
}

func (f *fakeSSH) HostKey() string { return f.hostKey }

func (f *fakeSSH) Close() error {
	f.closed = true
	return nil
}

// dialing returns a dialFunc handing out this connection, and records what it
// was asked to connect to.
func dialing(f *fakeSSH, asked *ssh.Config) dialFunc {
	return func(_ context.Context, cfg ssh.Config) (bootstrapClient, error) {
		if asked != nil {
			*asked = cfg
		}
		return f, nil
	}
}

func failingDial(err error) dialFunc {
	return func(context.Context, ssh.Config) (bootstrapClient, error) { return nil, err }
}

// bootstrapReady is a config with every answer the bootstrap needs.
func bootstrapReady(t *testing.T) config.Config {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg := fullConfig()
	cfg.SSHKeyPath = "~/.ssh/id_ed25519"
	if err := cfg.Save(); err != nil {
		t.Fatalf("seeding config: %v", err)
	}
	return cfg
}

// captureBootstrap runs the command against a fake connection.
func captureBootstrap(t *testing.T, dial dialFunc, id int64) (string, error) {
	t.Helper()

	var out bytes.Buffer
	err := runBootstrapCommand(context.Background(), &out, dial, id)
	return out.String(), err
}

func TestBootstrapIsRegistered(t *testing.T) {
	out := run(t, "--help")
	if !strings.Contains(out, "bootstrap") {
		t.Errorf("bootstrap is missing from root help:\n%s", out)
	}
}

func TestBootstrapNeedsExactlyOneID(t *testing.T) {
	withStateDir(t)

	for _, args := range [][]string{{"bootstrap"}, {"bootstrap", "1", "2"}} {
		if _, err := execute(args...); err == nil {
			t.Errorf("%v was accepted, want an error", args)
		}
	}
}

func TestBootstrapConfiguresTheServerAndRecordsIt(t *testing.T) {
	withStateDir(t)
	cfg := bootstrapReady(t)
	seedServers(t, doomedServer())

	f := newFakeSSH()
	var asked ssh.Config
	out, err := captureBootstrap(t, dialing(f, &asked), 1)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if asked.Host != "203.0.113.10" {
		t.Errorf("connected to %q, want the server's address", asked.Host)
	}
	if asked.KeyPath != cfg.SSHKeyPath {
		t.Errorf("connected with key %q, want the configured one", asked.KeyPath)
	}
	// Nothing was known about this server before, so nothing is pinned yet.
	if asked.KnownHostKey != "" {
		t.Errorf("a first connection expected the host key %q", asked.KnownHostKey)
	}
	if !f.closed {
		t.Error("the connection was left open")
	}

	srv := storedServers(t)[0]
	if !srv.Bootstrapped() {
		t.Error("the server is not marked bootstrapped")
	}
	if !srv.Credentials.Complete() {
		t.Errorf("credentials %+v cannot build a client config", srv.Credentials)
	}
	if srv.Credentials.ServerName != cfg.Reality.Host() {
		t.Errorf("camouflage = %q, want the configured %q", srv.Credentials.ServerName, cfg.Reality.Host())
	}
	// The private key belongs on the server and in local state, and nowhere
	// else: it is what a client's public key is checked against.
	if !strings.Contains(f.uploads["/usr/local/etc/xray/config.json"], srv.Credentials.PrivateKey) {
		t.Error("the server was configured with different material than was recorded")
	}
	if srv.SSHHostKey != f.hostKey {
		t.Errorf("host key = %q, want the one the server presented", srv.SSHHostKey)
	}

	if !strings.Contains(out, "ready in") {
		t.Errorf("the command does not say it finished:\n%s", out)
	}
}

// The material is only worth recording once the server is actually serving it.
func TestBootstrapRecordsNothingWhenItFails(t *testing.T) {
	withStateDir(t)
	bootstrapReady(t)
	seedServers(t, doomedServer())

	f := newFakeSSH()
	f.failOn = "apt-get"
	f.failErr = errors.New("dpkg was interrupted")

	if _, err := captureBootstrap(t, dialing(f, nil), 1); err == nil {
		t.Fatal("expected the failure to come back")
	}

	srv := storedServers(t)[0]
	if srv.Bootstrapped() {
		t.Error("a failed bootstrap marked the server configured")
	}
	if srv.Credentials.UUID != "" {
		t.Errorf("a failed bootstrap recorded credentials: %+v", srv.Credentials)
	}
	// The host key is pinned as soon as it is seen, so a retry checks against
	// it rather than trusting again.
	if srv.SSHHostKey != f.hostKey {
		t.Errorf("host key = %q, want it recorded on the first connection", srv.SSHHostKey)
	}
}

// A second run has something to check the server against.
func TestBootstrapPinsTheHostKeyAfterTheFirstConnection(t *testing.T) {
	withStateDir(t)
	bootstrapReady(t)

	const pinned = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKnownKnownKnownKnownKnownKnownKnown"
	seedServers(t, doomedServer())
	pinHostKey(t, 1, pinned)

	var asked ssh.Config
	if _, err := captureBootstrap(t, dialing(newFakeSSH(), &asked), 1); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if asked.KnownHostKey != pinned {
		t.Errorf("connected expecting %q, want the pinned key %q", asked.KnownHostKey, pinned)
	}
}

// pinHostKey records a host key the way a first connection would have.
func pinHostKey(t *testing.T, id int64, hostKey string) {
	t.Helper()

	store, err := openStore()
	if err != nil {
		t.Fatalf("opening state: %v", err)
	}
	defer store.Close()

	if err := store.SaveHostKey(context.Background(), id, hostKey); err != nil {
		t.Fatalf("pinning the host key: %v", err)
	}
}

func TestBootstrapUnknownServer(t *testing.T) {
	withStateDir(t)
	bootstrapReady(t)

	if _, err := captureBootstrap(t, dialing(newFakeSSH(), nil), 42); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("got %v, want state.ErrNotFound", err)
	}
}

// A server with no address is one sync has not caught up with, not a broken
// bootstrap.
func TestBootstrapWithNoAddress(t *testing.T) {
	withStateDir(t)
	bootstrapReady(t)

	srv := doomedServer()
	srv.IPv4 = ""
	seedServers(t, srv)

	_, err := captureBootstrap(t, dialing(newFakeSSH(), nil), 1)
	if err == nil || !strings.Contains(err.Error(), "vpncli sync") {
		t.Fatalf("got %v, want an error pointing at sync", err)
	}
}

func TestBootstrapWithNoCamouflageConfigured(t *testing.T) {
	withStateDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedServers(t, doomedServer())

	_, err := captureBootstrap(t, dialing(newFakeSSH(), nil), 1)
	if err == nil || !strings.Contains(err.Error(), "vpncli init") {
		t.Fatalf("got %v, want an error pointing at the wizard", err)
	}
}

func TestBootstrapReportsAConnectionFailure(t *testing.T) {
	withStateDir(t)
	bootstrapReady(t)
	seedServers(t, doomedServer())

	_, err := captureBootstrap(t, failingDial(ssh.ErrNoAuth), 1)
	if !errors.Is(err, ssh.ErrNoAuth) {
		t.Fatalf("got %v, want the dial error", err)
	}
}

func TestBootstrapHelpExplainsWhenToUseIt(t *testing.T) {
	out := run(t, "bootstrap", "--help")
	for _, want := range []string{"vpncli provision", "failed", "fresh key material"} {
		if !strings.Contains(out, want) {
			t.Errorf("bootstrap help does not mention %q:\n%s", want, out)
		}
	}
}
