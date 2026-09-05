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
	// Start runs a command in the background and returns its process id.
	Start(ctx context.Context, out io.Writer, name string, args ...string) (int, error)
	// Alive reports whether a process is still there.
	Alive(pid int) bool
	// Stop ends a process that is running as root.
	Stop(ctx context.Context, out io.Writer, pid int) error
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

// record is what a running tunnel left behind.
type record struct {
	PID     int
	Server  int64
	Started time.Time
}

// save writes the record of a running tunnel.
func (t *tunnel) save(r record) error {
	line := fmt.Sprintf("%d %d %d\n", r.PID, r.Server, r.Started.Unix())
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
	if len(fields) != 3 {
		return record{}, fmt.Errorf("%s is not a tunnel record", t.recordPath())
	}

	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return record{}, fmt.Errorf("%s is not a tunnel record: %w", t.recordPath(), err)
	}
	server, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return record{}, fmt.Errorf("%s is not a tunnel record: %w", t.recordPath(), err)
	}
	started, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return record{}, fmt.Errorf("%s is not a tunnel record: %w", t.recordPath(), err)
	}

	return record{PID: pid, Server: server, Started: time.Unix(started, 0)}, nil
}

// forget removes the record.
func (t *tunnel) forget() error {
	err := os.Remove(t.recordPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// running returns the record of a live tunnel. A record whose process is gone
// is cleared: sing-box killed by hand, or a machine that was rebooted, should
// not leave `vpncli tun status` insisting something is up.
func (t *tunnel) running() (record, error) {
	r, err := t.load()
	if err != nil {
		return record{}, err
	}
	if !t.run.Alive(r.PID) {
		if err := t.forget(); err != nil {
			return record{}, err
		}
		return record{}, ErrNotRunning
	}
	return r, nil
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

func (system) Start(ctx context.Context, out io.Writer, name string, args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// The password prompt has to reach the terminal, so sudo keeps stdin
	// until it is done; everything after that goes to the log.
	cmd.Stdin = os.Stdin
	cmd.Stdout, cmd.Stderr = out, out
	// Its own process group, so closing this terminal does not take the
	// tunnel with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

func (system) Alive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 asks the kernel about the process without touching it. A
	// permission error is a yes: the process exists and belongs to root,
	// which is exactly what a tunnel looks like from here.
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}

func (system) Stop(ctx context.Context, out io.Writer, pid int) error {
	// The tunnel runs as root, so ending it needs root. Killing the process
	// group takes sudo and sing-box together.
	cmd := exec.CommandContext(ctx, "sudo", "kill", "-TERM", strconv.Itoa(-pid))
	cmd.Stdin = os.Stdin
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}
