package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cryptossh "golang.org/x/crypto/ssh"
)

// testServer is an SSH server in this process. It answers exec requests from a
// table, which is enough to exercise everything the bootstrap does: run a
// command, read its output, fail with stderr, and take a file on stdin.
type testServer struct {
	t        *testing.T
	listener net.Listener
	config   *cryptossh.ServerConfig

	mu       sync.Mutex
	commands []string
	stdin    map[string]string

	// replies maps a command prefix to what it should do.
	replies map[string]reply
}

type reply struct {
	stdout string
	stderr string
	status uint32
}

func newTestServer(t *testing.T, hostKey cryptossh.Signer) *testServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	config := &cryptossh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(hostKey)

	s := &testServer{
		t:        t,
		listener: listener,
		config:   config,
		stdin:    map[string]string{},
		replies:  map[string]reply{},
	}
	t.Cleanup(func() { listener.Close() })

	go s.serve()
	return s
}

// port is where the server is listening. Dial always uses 22, so the tests
// point Config.Host at a loopback address and the listener at the same port
// where they can; instead the client is built directly against this address.
func (s *testServer) address() string { return s.listener.Addr().String() }

func (s *testServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *testServer) handle(conn net.Conn) {
	defer conn.Close()

	sshConn, channels, requests, err := cryptossh.NewServerConn(conn, s.config)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go cryptossh.DiscardRequests(requests)

	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(cryptossh.UnknownChannelType, "only sessions here")
			continue
		}
		channel, sessionRequests, err := newChannel.Accept()
		if err != nil {
			return
		}
		go s.session(channel, sessionRequests)
	}
}

func (s *testServer) session(channel cryptossh.Channel, requests <-chan *cryptossh.Request) {
	defer channel.Close()

	for request := range requests {
		if request.Type != "exec" {
			request.Reply(false, nil)
			continue
		}
		request.Reply(true, nil)

		// The payload is a length-prefixed string.
		command := string(request.Payload[4:])
		r := s.replyFor(command)

		// Anything the client sends is the file being uploaded.
		stdin, _ := io.ReadAll(channel)
		s.record(command, string(stdin))

		io.WriteString(channel, r.stdout)
		io.WriteString(channel.Stderr(), r.stderr)
		channel.SendRequest("exit-status", false, cryptossh.Marshal(struct{ Status uint32 }{r.status}))
		return
	}
}

func (s *testServer) replyFor(command string) reply {
	s.mu.Lock()
	defer s.mu.Unlock()

	for prefix, r := range s.replies {
		if strings.HasPrefix(command, prefix) {
			return r
		}
	}
	return reply{}
}

func (s *testServer) record(command, stdin string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, command)
	s.stdin[command] = stdin
}

func (s *testServer) ran() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func (s *testServer) sentOn(command string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stdin[command]
}

func (s *testServer) answer(prefix string, r reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies[prefix] = r
}

// hostKey generates a signer, and returns it with its authorized_keys line.
func hostKey(t *testing.T) (cryptossh.Signer, string) {
	t.Helper()

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a host key: %v", err)
	}
	signer, err := cryptossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer, strings.TrimSpace(string(cryptossh.MarshalAuthorizedKey(signer.PublicKey())))
}

// connect opens a client against the test server. Dial fixes the port at 22,
// so these tests go through the same code one level down.
func connect(t *testing.T, s *testServer, pinned string) *Client {
	t.Helper()

	var seen string
	config := &cryptossh.ClientConfig{
		User:            "root",
		Auth:            []cryptossh.AuthMethod{},
		Timeout:         2 * time.Second,
		HostKeyCallback: checkHostKey(pinned, &seen),
	}

	conn, err := net.DialTimeout("tcp", s.address(), 2*time.Second)
	if err != nil {
		t.Fatalf("dialing: %v", err)
	}
	sshConn, channels, requests, err := cryptossh.NewClientConn(conn, s.address(), config)
	if err != nil {
		conn.Close()
		t.Fatalf("handshake: %v", err)
	}

	client := &Client{conn: cryptossh.NewClient(sshConn, channels, requests), hostKey: seen}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestRun(t *testing.T) {
	signer, _ := hostKey(t)
	server := newTestServer(t, signer)
	server.answer("systemctl is-active", reply{stdout: "active\n"})

	client := connect(t, server, "")

	got, err := client.Run(context.Background(), "systemctl is-active xray")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(got) != "active" {
		t.Errorf("Run() = %q, want the command's output", got)
	}
}

// Which step failed is written in stderr far more often than in the exit
// status, so the error has to carry it.
func TestRunFailureCarriesStderr(t *testing.T) {
	signer, _ := hostKey(t)
	server := newTestServer(t, signer)
	server.answer("apt-get", reply{
		stderr: "E: Could not get lock /var/lib/dpkg/lock-frontend",
		status: 100,
	})

	client := connect(t, server, "")

	_, err := client.Run(context.Background(), "apt-get install -y nginx")
	if err == nil {
		t.Fatal("expected an error for a command that failed")
	}
	if !strings.Contains(err.Error(), "Could not get lock") {
		t.Errorf("error %q does not carry what the command said", err)
	}
	if !strings.Contains(err.Error(), "apt-get") {
		t.Errorf("error %q does not say which command failed", err)
	}
}

func TestUpload(t *testing.T) {
	signer, _ := hostKey(t)
	server := newTestServer(t, signer)
	client := connect(t, server, "")

	content := []byte(`{"inbounds": []}`)
	if err := client.Upload(context.Background(), "/usr/local/etc/xray/config.json", 0o600, content); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	ran := server.ran()
	if len(ran) != 1 {
		t.Fatalf("ran %v, want one command", ran)
	}
	// The mode is set as the file appears: a config holding a private key must
	// never exist world readable, not even briefly.
	if !strings.Contains(ran[0], "-m 0600") {
		t.Errorf("upload command %q does not set the mode as it writes", ran[0])
	}
	if !strings.Contains(ran[0], "'/usr/local/etc/xray/config.json'") {
		t.Errorf("upload command %q does not quote the path", ran[0])
	}
	if got := server.sentOn(ran[0]); got != string(content) {
		t.Errorf("uploaded %q, want %q", got, content)
	}
}

// The first connection has nothing to check against and keeps what it saw.
func TestHostKeyIsRecordedOnFirstUse(t *testing.T) {
	signer, authorized := hostKey(t)
	server := newTestServer(t, signer)

	client := connect(t, server, "")

	if client.HostKey() != authorized {
		t.Errorf("host key = %q, want %q", client.HostKey(), authorized)
	}
}

func TestHostKeyIsAcceptedWhenItMatches(t *testing.T) {
	signer, authorized := hostKey(t)
	server := newTestServer(t, signer)

	client := connect(t, server, authorized)

	if client.HostKey() != authorized {
		t.Errorf("host key = %q, want %q", client.HostKey(), authorized)
	}
}

// What this connection carries is the server's REALITY private key, so a
// server answering with a key we have not seen before is refused.
func TestHostKeyChangeIsRefused(t *testing.T) {
	signer, _ := hostKey(t)
	_, somebodyElse := hostKey(t)
	server := newTestServer(t, signer)

	var seen string
	config := &cryptossh.ClientConfig{
		User:            "root",
		Timeout:         2 * time.Second,
		HostKeyCallback: checkHostKey(somebodyElse, &seen),
	}

	conn, err := net.DialTimeout("tcp", server.address(), 2*time.Second)
	if err != nil {
		t.Fatalf("dialing: %v", err)
	}
	defer conn.Close()

	_, _, _, err = cryptossh.NewClientConn(conn, server.address(), config)
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("got %v, want ErrHostKeyChanged", err)
	}
}

// A wait that is given up on has to end the command, not leak a session that
// outlives it.
func TestRunGivesUpWhenTheContextIsCanceled(t *testing.T) {
	signer, _ := hostKey(t)
	server := newTestServer(t, signer)
	client := connect(t, server, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.Run(ctx, "sleep 600"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestDialWithNothingToAuthenticateWith(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")

	_, err := Dial(context.Background(), Config{Host: "203.0.113.10"})
	if !errors.Is(err, ErrNoAuth) {
		t.Fatalf("got %v, want ErrNoAuth", err)
	}
	if !strings.Contains(err.Error(), "vpncli providers init") {
		t.Errorf("error %q does not say how to fix it", err)
	}
}

// A key with a passphrase is not a broken config, it is one the agent is
// supposed to hold. The message has to say so rather than blame the file.
func TestPassphraseProtectedKeySaysToUseTheAgent(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	block, err := cryptossh.MarshalPrivateKeyWithPassphrase(private, "", []byte("hunter2"))
	if err != nil {
		t.Fatalf("encrypting the key: %v", err)
	}

	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing the key: %v", err)
	}
	t.Setenv("SSH_AUTH_SOCK", "")

	_, err = authMethods(path)
	if !errors.Is(err, ErrNoAuth) {
		t.Fatalf("got %v, want ErrNoAuth", err)
	}
	if !strings.Contains(err.Error(), "ssh-add") {
		t.Errorf("error %q does not point at the agent", err)
	}
}

func TestAuthMethodsWithAnUnreadableKeyAndNoAgent(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")

	_, err := authMethods(filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, ErrNoAuth) {
		t.Fatalf("got %v, want ErrNoAuth", err)
	}
}

func TestExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory here")
	}

	got, err := expand("~/.ssh/id_ed25519")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if want := filepath.Join(home, ".ssh", "id_ed25519"); got != want {
		t.Errorf("expand() = %q, want %q", got, want)
	}
	// Anything else is left exactly as written.
	if got, _ := expand("/etc/keys/id"); got != "/etc/keys/id" {
		t.Errorf("expand(/etc/keys/id) = %q, want it untouched", got)
	}
}

func TestShellQuote(t *testing.T) {
	tests := map[string]string{
		"/etc/xray/config.json": `'/etc/xray/config.json'`,
		"/tmp/it's there":       `'/tmp/it'\''s there'`,
	}
	for in, want := range tests {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstWordsShortensALongCommand(t *testing.T) {
	long := strings.Repeat("apt-get install nginx ", 10)
	got := firstWords(long)
	if len(got) > 60 {
		t.Errorf("firstWords kept %d characters: %q", len(got), got)
	}

	// A multi-line script is named by its first line, not its whole body.
	if got := firstWords("set -eu\nexit 1"); got != "set -eu" {
		t.Errorf("firstWords() = %q, want the first line", got)
	}
}
