package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeRunner is the system without the parts that need root. What is worth
// testing here is which command would run and what is recorded, not that the
// kernel does what sudo asks.
type fakeRunner struct {
	missing bool

	ran     []string
	started []string
	stopped []int

	pid     int
	alive   map[int]bool
	runErr  error
	stopErr error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{pid: 4242, alive: map[int]bool{}}
}

func (f *fakeRunner) Look(name string) (string, error) {
	if f.missing {
		return "", ErrNoSingBox
	}
	return "/usr/local/bin/" + name, nil
}

func (f *fakeRunner) Run(_ context.Context, _ io.Reader, _ io.Writer, name string, args ...string) error {
	f.ran = append(f.ran, name+" "+strings.Join(args, " "))
	return f.runErr
}

func (f *fakeRunner) Start(_ context.Context, _ io.Writer, name string, args ...string) (int, error) {
	f.started = append(f.started, name+" "+strings.Join(args, " "))
	if f.runErr != nil {
		return 0, f.runErr
	}
	f.alive[f.pid] = true
	return f.pid, nil
}

func (f *fakeRunner) Alive(pid int) bool { return f.alive[pid] }

func (f *fakeRunner) Stop(_ context.Context, _ io.Writer, pid int) error {
	if f.stopErr != nil {
		return f.stopErr
	}
	f.stopped = append(f.stopped, pid)
	delete(f.alive, pid)
	return nil
}

// tunneling returns a tunnel over a fake system, with a configured server in
// state to point it at.
func tunneling(t *testing.T) (*tunnel, *fakeRunner) {
	t.Helper()
	connectable(t)

	f := newFakeRunner()
	return &tunnel{run: f, dir: t.TempDir()}, f
}

func up(t *testing.T, tn *tunnel, id int64, detach bool) (string, error) {
	t.Helper()

	var out bytes.Buffer
	err := runTunUp(context.Background(), strings.NewReader(""), &out, tn, id, detach)
	return out.String(), err
}

func TestTunIsRegistered(t *testing.T) {
	out := run(t, "--help")
	if !strings.Contains(out, "tun") {
		t.Errorf("tun is missing from root help:\n%s", out)
	}
	for _, sub := range []string{"up", "down", "status"} {
		if help := run(t, "tun", "--help"); !strings.Contains(help, sub) {
			t.Errorf("`vpncli tun` help does not offer %q:\n%s", sub, help)
		}
	}
}

// The config is the whole point of the command: written from local state, and
// unreadable by anyone else on the machine, because it is the key to a server.
func TestTunUpWritesTheConfigAndRunsSingBox(t *testing.T) {
	tn, f := tunneling(t)

	out, err := up(t, tn, 1, false)
	if err != nil {
		t.Fatalf("tun up: %v", err)
	}

	info, err := os.Stat(tn.configPath())
	if err != nil {
		t.Fatalf("no config was written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config is %04o, want 0600", perm)
	}

	written, err := os.ReadFile(tn.configPath())
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	var config struct {
		Inbounds []struct {
			Type string `json:"type"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(written, &config); err != nil {
		t.Fatalf("the config is not valid JSON: %v", err)
	}
	if len(config.Inbounds) != 1 || config.Inbounds[0].Type != "tun" {
		t.Errorf("config is not a tun one: %s", written)
	}

	if len(f.ran) != 1 {
		t.Fatalf("ran %v, want one command", f.ran)
	}
	// Root, because creating an interface and rewriting routes needs it.
	if !strings.HasPrefix(f.ran[0], "sudo sing-box run -c ") {
		t.Errorf("ran %q, want sing-box under sudo", f.ran[0])
	}
	if !strings.Contains(f.ran[0], tn.configPath()) {
		t.Errorf("ran %q, want it pointed at the config just written", f.ran[0])
	}
	if !strings.Contains(out, "Ctrl-C") {
		t.Errorf("nothing says how to bring it down:\n%s", out)
	}
}

// A foreground tunnel is over when the command returns, so there is nothing to
// remember and nothing to leave behind.
func TestTunUpInTheForegroundRecordsNothing(t *testing.T) {
	tn, _ := tunneling(t)

	if _, err := up(t, tn, 1, false); err != nil {
		t.Fatalf("tun up: %v", err)
	}
	if _, err := os.Stat(tn.recordPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("a foreground tunnel left a record behind")
	}
}

func TestTunUpDetached(t *testing.T) {
	tn, f := tunneling(t)

	out, err := up(t, tn, 1, true)
	if err != nil {
		t.Fatalf("tun up --detach: %v", err)
	}
	if len(f.started) != 1 || len(f.ran) != 0 {
		t.Fatalf("started %v and ran %v, want it started in the background", f.started, f.ran)
	}

	running, err := tn.running()
	if err != nil {
		t.Fatalf("nothing was recorded: %v", err)
	}
	if running.PID != f.pid || running.Server != 1 {
		t.Errorf("recorded %+v, want pid %d for server 1", running, f.pid)
	}
	if !strings.Contains(out, "vpncli tun down") {
		t.Errorf("nothing says how to stop it:\n%s", out)
	}
}

func TestTunDown(t *testing.T) {
	tn, f := tunneling(t)

	if _, err := up(t, tn, 1, true); err != nil {
		t.Fatalf("tun up: %v", err)
	}

	var out bytes.Buffer
	if err := runTunDown(context.Background(), &out, tn); err != nil {
		t.Fatalf("tun down: %v", err)
	}

	if len(f.stopped) != 1 || f.stopped[0] != f.pid {
		t.Errorf("stopped %v, want the recorded process", f.stopped)
	}
	if _, err := os.Stat(tn.recordPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("the record outlived the tunnel")
	}
	if !strings.Contains(out.String(), "tunnel down") {
		t.Errorf("nothing says it stopped:\n%s", out.String())
	}
}

func TestTunDownWithNothingRunning(t *testing.T) {
	tn, f := tunneling(t)

	var out bytes.Buffer
	if err := runTunDown(context.Background(), &out, tn); err != nil {
		t.Fatalf("tun down: %v", err)
	}
	if len(f.stopped) != 0 {
		t.Errorf("stopped %v with nothing running", f.stopped)
	}
	if !strings.Contains(out.String(), "no tunnel is running") {
		t.Errorf("got %q, want it to say nothing is up", out.String())
	}
}

func TestTunStatus(t *testing.T) {
	tn, _ := tunneling(t)

	var down bytes.Buffer
	if err := runTunStatus(context.Background(), &down, tn); err != nil {
		t.Fatalf("tun status: %v", err)
	}
	if strings.TrimSpace(down.String()) != "down" {
		t.Errorf("status = %q, want down", down.String())
	}

	if _, err := up(t, tn, 1, true); err != nil {
		t.Fatalf("tun up: %v", err)
	}

	var upNow bytes.Buffer
	if err := runTunStatus(context.Background(), &upNow, tn); err != nil {
		t.Fatalf("tun status: %v", err)
	}
	for _, want := range []string{"up through", "vpncli-fra1-a1b2c3", "203.0.113.10"} {
		if !strings.Contains(upNow.String(), want) {
			t.Errorf("status %q does not mention %q", upNow.String(), want)
		}
	}
}

// sing-box killed by hand, or a machine that rebooted, must not leave status
// insisting a tunnel is up.
func TestTunStatusForgetsAProcessThatIsGone(t *testing.T) {
	tn, f := tunneling(t)

	if _, err := up(t, tn, 1, true); err != nil {
		t.Fatalf("tun up: %v", err)
	}
	delete(f.alive, f.pid) // killed from somewhere else

	var out bytes.Buffer
	if err := runTunStatus(context.Background(), &out, tn); err != nil {
		t.Fatalf("tun status: %v", err)
	}
	if strings.TrimSpace(out.String()) != "down" {
		t.Errorf("status = %q, want down", out.String())
	}
	if _, err := os.Stat(tn.recordPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("the stale record was kept")
	}
}

// Two tunnels at once would fight over the routing table.
func TestTunUpRefusesASecondTunnel(t *testing.T) {
	tn, _ := tunneling(t)

	if _, err := up(t, tn, 1, true); err != nil {
		t.Fatalf("tun up: %v", err)
	}

	_, err := up(t, tn, 1, true)
	if err == nil || !strings.Contains(err.Error(), "already up") {
		t.Fatalf("got %v, want it to refuse a second tunnel", err)
	}
}

// A config written for a client that is not installed is a file nobody asked
// for, and the error has to name what to install.
func TestTunUpWithoutSingBox(t *testing.T) {
	tn, f := tunneling(t)
	f.missing = true

	_, err := up(t, tn, 1, false)
	if !errors.Is(err, ErrNoSingBox) {
		t.Fatalf("got %v, want ErrNoSingBox", err)
	}
	if _, err := os.Stat(tn.configPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("a config was written for a client that is not there")
	}
}

func TestTunUpOnAnUnconfiguredServer(t *testing.T) {
	withStateDir(t)
	seedServers(t, doomedServer())
	tn := &tunnel{run: newFakeRunner(), dir: t.TempDir()}

	if _, err := up(t, tn, 1, false); err == nil {
		t.Fatal("expected an error for a server that was never bootstrapped")
	}
}

// After a provision or a rotation the newest server is the one meant, and
// typing its id is a lookup nobody should have to do.
func TestTunUpWithNoIDTakesTheNewestConfiguredServer(t *testing.T) {
	tn, _ := tunneling(t)

	out, err := up(t, tn, 0, false)
	if err != nil {
		t.Fatalf("tun up: %v", err)
	}
	if !strings.Contains(out, "vpncli-fra1-a1b2c3") {
		t.Errorf("did not choose the configured server:\n%s", out)
	}
}

func TestTunUpWithNothingToConnectTo(t *testing.T) {
	withStateDir(t)
	tn := &tunnel{run: newFakeRunner(), dir: t.TempDir()}

	_, err := up(t, tn, 0, false)
	if err == nil || !strings.Contains(err.Error(), "vpncli server provision") {
		t.Fatalf("got %v, want it to say how to get a server", err)
	}
}

// A record for a server that has since been destroyed still says something
// useful rather than failing.
func TestTunStatusWithTheServerGone(t *testing.T) {
	tn, _ := tunneling(t)

	if err := tn.save(record{PID: 4242, Server: 99, Started: time.Now()}); err != nil {
		t.Fatalf("save: %v", err)
	}
	tn.run.(*fakeRunner).alive[4242] = true

	var out bytes.Buffer
	if err := runTunStatus(context.Background(), &out, tn); err != nil {
		t.Fatalf("tun status: %v", err)
	}
	if !strings.Contains(out.String(), "no longer in local state") {
		t.Errorf("status = %q, want it to say the server is gone", out.String())
	}
}

func TestTunRecordRoundTrip(t *testing.T) {
	tn := &tunnel{run: newFakeRunner(), dir: t.TempDir()}

	if _, err := tn.load(); !errors.Is(err, ErrNotRunning) {
		t.Errorf("got %v for a missing record, want ErrNotRunning", err)
	}

	want := record{PID: 17, Server: 3, Started: time.Now().Truncate(time.Second)}
	if err := tn.save(want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := tn.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.PID != want.PID || got.Server != want.Server || !got.Started.Equal(want.Started) {
		t.Errorf("loaded %+v, want %+v", got, want)
	}
}
