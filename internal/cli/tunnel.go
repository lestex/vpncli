package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lestex/vpncli/internal/config"
)

// singBox is the client this drives. It is not vendored or installed by
// vpncli: it is a VPN client in its own right, packaged everywhere, and
// wrapping someone else's binary is the whole of what `vpncli tun` does.
const singBox = "sing-box"

// minSingBox is the oldest client a generated config works on.
//
// The tun config uses route rule actions, which arrived in 1.11, and the
// typed DNS server format, which arrived in 1.12. On anything older it does
// not fail to connect, it fails to parse - with a message about an unknown
// field that says nothing about the version being the problem.
const minSingBox = "1.12.0"

// ErrNoSingBox is returned when the client is not installed.
var ErrNoSingBox = errors.New("sing-box is not installed")

// ErrOldSingBox is returned when it is installed but too old to read what
// this generates.
var ErrOldSingBox = errors.New("sing-box is too old")

// ErrNotRunning is returned by down and status when no tunnel is up.
var ErrNotRunning = errors.New("no tunnel is running")

// runner is how the tunnel starts and stops processes. The real one shells
// out; tests substitute their own, because a test that needs root to pass is
// a test nobody runs.
type runner interface {
	// Look reports where a binary is, or an error if it is not installed.
	Look(name string) (string, error)
	// Run starts a command and waits for it, wired to the terminal.
	Run(ctx context.Context, in io.Reader, out io.Writer, name string, args ...string) error
	// Start runs a command in the background.
	Start(ctx context.Context, out io.Writer, name string, args ...string) error
	// Version reports the client's version, as it prints it.
	Version(ctx context.Context) (string, error)
	// Matching returns the processes whose command line contains needle.
	//
	// The tunnel is found this way rather than by a remembered process id,
	// because the id of what gets started is not the id of what survives:
	// sudo forks a monitor and the process we spawned exits within
	// milliseconds, leaving a recorded id that belongs to nothing.
	Matching(ctx context.Context, needle string) ([]int, error)
	// Stop ends processes that are running as root.
	Stop(ctx context.Context, out io.Writer, pids []int) error
}

// tunnel is the client process and the files that describe it.
type tunnel struct {
	run runner
	// dir is where the config and the record of a running tunnel live, beside
	// the state database.
	dir string
}

// newTunnel builds a tunnel over the real system.
func newTunnel() (*tunnel, error) {
	dir, err := config.DataDir()
	if err != nil {
		return nil, err
	}
	return &tunnel{run: system{}, dir: dir}, nil
}

// configPath is where the generated client config is written. It is one file
// per machine rather than one per server: two tunnels at once would fight over
// the routing table, so there is only ever one to describe.
func (t *tunnel) configPath() string { return filepath.Join(t.dir, "tun.json") }

// recordPath holds what is running: the process id and the server it is for.
func (t *tunnel) recordPath() string { return filepath.Join(t.dir, "tun.pid") }

// record is what a running tunnel left behind. It holds what cannot be
// recovered by looking at the process list: which server the tunnel is to, and
// when it went up.
type record struct {
	Server  int64
	Started time.Time
}

// save writes the record of a running tunnel.
func (t *tunnel) save(r record) error {
	line := fmt.Sprintf("%d %d\n", r.Server, r.Started.Unix())
	if err := os.WriteFile(t.recordPath(), []byte(line), 0o600); err != nil {
		return fmt.Errorf("recording the tunnel: %w", err)
	}
	return nil
}

// load reads the record, if there is one.
func (t *tunnel) load() (record, error) {
	raw, err := os.ReadFile(t.recordPath())
	if err != nil {
		return record{}, ErrNotRunning
	}

	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return record{}, fmt.Errorf("%s is not a tunnel record", t.recordPath())
	}

	server, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return record{}, fmt.Errorf("%s is not a tunnel record: %w", t.recordPath(), err)
	}
	started, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return record{}, fmt.Errorf("%s is not a tunnel record: %w", t.recordPath(), err)
	}

	return record{Server: server, Started: time.Unix(started, 0)}, nil
}

// forget removes the record.
func (t *tunnel) forget() error {
	err := os.Remove(t.recordPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// running reports the live tunnel: the processes carrying it, and what is
// known about it from the record.
//
// The processes are the truth. A record left by a tunnel that has since been
// killed by hand, or by a machine that rebooted, is cleared rather than
// believed - and a tunnel running without a record is still reported, because
// it is still routing this machine.
func (t *tunnel) running(ctx context.Context) ([]int, record, error) {
	pids, err := t.run.Matching(ctx, t.configPath())
	if err != nil {
		return nil, record{}, err
	}
	if len(pids) == 0 {
		if err := t.forget(); err != nil {
			return nil, record{}, err
		}
		return nil, record{}, ErrNotRunning
	}

	r, err := t.load()
	if errors.Is(err, ErrNotRunning) {
		// Up, but started by something other than this command.
		return pids, record{}, nil
	}
	if err != nil {
		return nil, record{}, err
	}
	return pids, r, nil
}

// system is the real runner.
type system struct{}

func (system) Look(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%w: it is a separate program, and `brew install %s` or your package manager installs it", ErrNoSingBox, name)
	}
	return path, nil
}

func (system) Run(ctx context.Context, in io.Reader, out io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
	return cmd.Run()
}

func (system) Start(_ context.Context, out io.Writer, name string, args ...string) error {
	// Deliberately not CommandContext: the point of a detached tunnel is to
	// outlive the command that started it, and a context canceled as this
	// process exits would take the tunnel with it.
	cmd := exec.Command(name, args...)
	// The password prompt has to reach the terminal, so sudo keeps stdin
	// until it is done; everything after that goes to the log.
	cmd.Stdin = os.Stdin
	cmd.Stdout, cmd.Stderr = out, out
	// Its own process group, so closing this terminal does not take the
	// tunnel with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return err
	}
	// Nothing waits for it. The child is reparented when this process exits,
	// which is what makes it a background tunnel rather than a zombie.
	return cmd.Process.Release()
}

func (system) Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, singBox, "version").Output()
	if err != nil {
		return "", fmt.Errorf("asking %s its version: %w", singBox, err)
	}
	return string(out), nil
}

func (system) Matching(ctx context.Context, needle string) ([]int, error) {
	// ps rather than pgrep: both exist on macOS and Linux, but ps is the one
	// with a stable output format to parse.
	out, err := exec.CommandContext(ctx, "ps", "-ax", "-o", "pid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}

	var pids []int
	for line := range strings.SplitSeq(string(out), "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		pid, err := strconv.Atoi(strings.Fields(line)[0])
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

func (system) Stop(ctx context.Context, out io.Writer, pids []int) error {
	// The tunnel runs as root, so ending it needs root. Every process
	// carrying it goes at once: sing-box and the sudo wrappers above it,
	// which would otherwise be left waiting on a child that is gone.
	args := []string{"kill", "-TERM"}
	for _, pid := range pids {
		args = append(args, strconv.Itoa(pid))
	}

	cmd := exec.CommandContext(ctx, "sudo", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}

// usable checks that the client is installed and new enough to read what this
// generates.
//
// A version that cannot be parsed is not an error: it is a build from source
// or a distribution doing its own thing, and refusing to run over a string
// nobody can read would be worse than trying.
func (t *tunnel) usable(ctx context.Context, out io.Writer) error {
	if _, err := t.run.Look(singBox); err != nil {
		return err
	}

	printed, err := t.run.Version(ctx)
	if err != nil {
		fmt.Fprintf(out, "Could not ask %s its version: %v\n", singBox, err)
		return nil
	}

	found := parseVersion(printed)
	if found == "" {
		fmt.Fprintf(out, "Could not read the %s version from %q, carrying on.\n",
			singBox, strings.TrimSpace(firstLine(printed)))
		return nil
	}
	if older(found, minSingBox) {
		return fmt.Errorf("%w: %s is %s and this needs %s or newer, because the config it writes uses route rule actions and the typed DNS format",
			ErrOldSingBox, singBox, found, minSingBox)
	}
	return nil
}

// parseVersion pulls the number out of what the client prints, which is a
// line like "sing-box version 1.14.0".
func parseVersion(printed string) string {
	fields := strings.Fields(firstLine(printed))
	for i, field := range fields {
		if field == "version" && i+1 < len(fields) {
			return strings.TrimPrefix(fields[i+1], "v")
		}
	}
	return ""
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// older compares two dotted versions. A pre-release suffix is ignored: a
// 1.12.0 beta reads the same config a 1.12.0 release does.
func older(have, want string) bool {
	haveParts := versionParts(have)
	wantParts := versionParts(want)

	for i := range wantParts {
		if i >= len(haveParts) {
			return true
		}
		if haveParts[i] != wantParts[i] {
			return haveParts[i] < wantParts[i]
		}
	}
	return false
}

// versionParts splits a version into numbers, stopping at anything that is
// not one.
func versionParts(version string) []int {
	version, _, _ = strings.Cut(version, "-")
	version, _, _ = strings.Cut(version, "+")

	var parts []int
	for _, field := range strings.Split(version, ".") {
		n, err := strconv.Atoi(field)
		if err != nil {
			break
		}
		parts = append(parts, n)
	}
	return parts
}
