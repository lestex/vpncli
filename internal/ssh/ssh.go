// Package ssh is the connection the bootstrap runs over.
//
// It is a small wrapper on x/crypto/ssh rather than a call to the ssh binary,
// so a failed step comes back as an error with the command's own stderr in it
// instead of something to parse out of a terminal.
//
// Host keys are trusted on first use and pinned after that. A server created a
// minute ago has no key anybody could have known in advance, so the first
// connection has nothing to check against; from then on the key is in local
// state and a different one is refused. That matters here more than usual,
// because what this connection carries is the server's REALITY private key.
package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const (
	// defaultPort is where sshd listens on every image we support.
	defaultPort = "22"
	// defaultUser is who a fresh cloud image gives the key to.
	defaultUser = "root"

	// dialTimeout bounds one connection attempt.
	dialTimeout = 15 * time.Second
	// dialAttempts is how many times a connection is tried. sshd can refuse
	// for a moment after boot even once the port answers, and a bootstrap that
	// gave up there would leave a server nobody configured.
	dialAttempts = 5
	// dialBackoff is the wait between attempts.
	dialBackoff = 3 * time.Second
)

// ErrHostKeyChanged is returned when a server presents a key other than the
// one recorded for it. It is deliberately not recoverable here: either the
// server was rebuilt, in which case the row is stale, or the connection is not
// going where it should.
var ErrHostKeyChanged = errors.New("the server's SSH host key changed")

// ErrNoAuth is returned when there is nothing to authenticate with.
var ErrNoAuth = errors.New("no SSH key to connect with")

// Config is what one connection needs.
type Config struct {
	// Host is the address to connect to, without a port.
	Host string
	// User defaults to root.
	User string
	// KeyPath is a private key file. A leading ~ is expanded. Empty means the
	// agent is the only way in.
	KeyPath string
	// KnownHostKey is the key this server presented before, in
	// authorized_keys form. Empty trusts whatever answers, which is what a
	// server being connected to for the first time gets.
	KnownHostKey string
}

// Client is one open connection.
type Client struct {
	conn *ssh.Client
	// hostKey is what the server actually presented, for a caller that has
	// nothing recorded yet and wants to keep it.
	hostKey string
}

// Dial opens a connection, retrying while sshd finishes coming up.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	auth, err := authMethods(cfg.KeyPath)
	if err != nil {
		return nil, err
	}

	user := cfg.User
	if user == "" {
		user = defaultUser
	}

	var lastErr error
	for attempt := range dialAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(dialBackoff):
			}
		}

		client, err := dial(ctx, cfg, user, auth)
		if err == nil {
			return client, nil
		}
		// A key that is not the pinned one will not become the pinned one by
		// being asked again.
		if errors.Is(err, ErrHostKeyChanged) || ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
	}

	return nil, fmt.Errorf("connecting to %s: %w", cfg.Host, lastErr)
}

// dial makes one attempt.
func dial(ctx context.Context, cfg Config, user string, auth []ssh.AuthMethod) (*Client, error) {
	var seen string
	clientConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		Timeout:         dialTimeout,
		HostKeyCallback: checkHostKey(cfg.KnownHostKey, &seen),
	}

	dialer := net.Dialer{Timeout: dialTimeout}
	address := net.JoinHostPort(cfg.Host, defaultPort)

	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}

	// The handshake has its own deadline: a TCP connection that is answered
	// and then goes quiet must not hold the bootstrap open indefinitely.
	if err := conn.SetDeadline(time.Now().Add(dialTimeout)); err != nil {
		conn.Close()
		return nil, err
	}

	sshConn, channels, requests, err := ssh.NewClientConn(conn, address, clientConfig)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		sshConn.Close()
		return nil, err
	}

	return &Client{conn: ssh.NewClient(sshConn, channels, requests), hostKey: seen}, nil
}

// HostKey is the key the server presented, in authorized_keys form.
func (c *Client) HostKey() string { return c.hostKey }

// Close ends the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Run executes one command and returns its standard output.
//
// A command that fails takes its stderr into the error. The bootstrap is a
// sequence of package installs and systemd calls, and which one failed is
// usually written there rather than in the exit status.
func (c *Client) Run(ctx context.Context, command string) (string, error) {
	return c.run(ctx, command, nil)
}

// Upload writes content to path with the given mode, creating the directory
// above it. It goes through `install` rather than a shell redirect so the mode
// is set as the file appears - a config holding a private key must never exist
// world readable, not even for the moment before a chmod.
func (c *Client) Upload(ctx context.Context, path string, mode os.FileMode, content []byte) error {
	command := fmt.Sprintf("install -D -m %04o /dev/stdin %s", mode.Perm(), shellQuote(path))
	if _, err := c.run(ctx, command, content); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// run is Run with optional standard input.
func (c *Client) run(ctx context.Context, command string, stdin []byte) (string, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return "", fmt.Errorf("opening a session: %w", err)
	}
	defer session.Close()

	if stdin != nil {
		session.Stdin = strings.NewReader(string(stdin))
	}
	var stdout, stderr strings.Builder
	session.Stdout = &stdout
	session.Stderr = &stderr

	// A session has no context of its own. Closing it from here is what makes
	// Ctrl-C during a long apt install actually let go.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			session.Close()
		case <-done:
		}
	}()

	err = session.Run(command)
	if ctx.Err() != nil {
		return stdout.String(), ctx.Err()
	}
	if err != nil {
		return stdout.String(), commandError(command, err, stderr.String())
	}
	return stdout.String(), nil
}

// commandError renders a failed command with whatever it had to say.
func commandError(command string, err error, stderr string) error {
	said := strings.TrimSpace(stderr)
	if said == "" {
		return fmt.Errorf("%s: %w", firstWords(command), err)
	}
	return fmt.Errorf("%s: %w: %s", firstWords(command), err, lastLines(said, 5))
}

// firstWords shortens a command for an error message. Some of what the
// bootstrap runs is a paragraph long, and the error only has to say which step
// it was.
func firstWords(command string) string {
	command = strings.TrimSpace(strings.SplitN(command, "\n", 2)[0])
	if len(command) > 60 {
		return command[:57] + "..."
	}
	return command
}

// lastLines keeps the end of a command's stderr, which is where the reason
// usually is.
func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}

// checkHostKey builds the callback: record what was presented, and refuse
// anything that is not the pinned key.
func checkHostKey(pinned string, seen *string) ssh.HostKeyCallback {
	return func(_ string, remote net.Addr, key ssh.PublicKey) error {
		presented := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		*seen = presented

		if pinned == "" || presented == pinned {
			return nil
		}
		return fmt.Errorf("%w: %s offered %s, expected %s",
			ErrHostKeyChanged, remote, fingerprint(key), pinned)
	}
}

// fingerprint is the SHA256 form ssh itself prints.
func fingerprint(key ssh.PublicKey) string { return ssh.FingerprintSHA256(key) }

// authMethods collects the ways in, in the order they are tried: the
// configured key first, since it was named on purpose, then the agent.
func authMethods(keyPath string) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	var reasons []string

	if keyPath != "" {
		path, err := expand(keyPath)
		if err != nil {
			return nil, err
		}
		signer, err := loadKey(path)
		if err != nil {
			// A key that cannot be read is worth saying out loud, but it is
			// not fatal on its own: the agent may hold the same key, which is
			// exactly the case for a passphrase-protected one.
			reasons = append(reasons, err.Error())
		} else {
			methods = append(methods, ssh.PublicKeys(signer))
		}
	}

	if socket := os.Getenv("SSH_AUTH_SOCK"); socket != "" {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("reaching the SSH agent: %v", err))
		} else {
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	if len(methods) == 0 {
		if len(reasons) > 0 {
			return nil, fmt.Errorf("%w: %s", ErrNoAuth, strings.Join(reasons, "; "))
		}
		return nil, fmt.Errorf("%w: set ssh_key_path with `vpncli providers init`, or start an agent", ErrNoAuth)
	}
	return methods, nil
}

// loadKey reads and parses one private key file.
func loadKey(path string) (ssh.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the SSH key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		var passphrase *ssh.PassphraseMissingError
		if errors.As(err, &passphrase) {
			// Asking for it here would mean holding a passphrase in this
			// process; the agent already solves that.
			return nil, fmt.Errorf("%s needs a passphrase: add it to your agent with `ssh-add %s`", path, path)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return signer, nil
}

// expand resolves a leading ~, which is how an SSH key path is usually
// written and is not something the shell has expanded for us here.
func expand(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expanding %s: %w", path, err)
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
}

// shellQuote makes a path safe to hand to a remote shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
