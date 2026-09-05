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

// ErrNoSingBox is returned when the client is not installed.
var ErrNoSingBox = errors.New("sing-box is not installed")

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
