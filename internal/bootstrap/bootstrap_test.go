package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/lestex/vpncli/internal/reality"
)

// fakeRunner is a server that does whatever it is told and remembers it. The
// bootstrap is a sequence of shell commands, so what is worth testing is which
// commands, in which order, and what went into the files.
type fakeRunner struct {
	commands []string
	uploads  map[string]upload

	// replies answers a command by prefix; anything unmatched returns "".
	replies map[string]string
	// fail makes the first command with this prefix fail.
	fail    string
	failErr error
}

type upload struct {
	mode    os.FileMode
	content string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		uploads: map[string]upload{},
		replies: map[string]string{
			"uname -m": "x86_64\n",
			"sysctl -n net.ipv4.tcp_congestion_control": "bbr\n",
			"systemctl is-active xray":                  "active\n",
		},
	}
}

func (f *fakeRunner) Run(_ context.Context, command string) (string, error) {
	f.commands = append(f.commands, command)

	if f.fail != "" && strings.HasPrefix(command, f.fail) {
		return "", f.failErr
	}
	for prefix, reply := range f.replies {
		if strings.HasPrefix(command, prefix) {
			return reply, nil
		}
	}
	return "", nil
}

func (f *fakeRunner) Upload(_ context.Context, path string, mode os.FileMode, content []byte) error {
	f.commands = append(f.commands, "upload "+path)
	f.uploads[path] = upload{mode: mode, content: string(content)}
	return nil
}

// ranBefore reports whether a command matching first happened before one
// matching second.
func (f *fakeRunner) ranBefore(t *testing.T, first, second string) bool {
	t.Helper()

	a := f.indexOf(t, first)
	b := f.indexOf(t, second)
	return a < b
}

func (f *fakeRunner) indexOf(t *testing.T, substring string) int {
	t.Helper()

	i := slices.IndexFunc(f.commands, func(c string) bool { return strings.Contains(c, substring) })
	if i < 0 {
		t.Fatalf("nothing ran containing %q:\n%s", substring, strings.Join(f.commands, "\n---\n"))
	}
	return i
}

func (f *fakeRunner) ranSomething(substring string) bool {
	return slices.ContainsFunc(f.commands, func(c string) bool { return strings.Contains(c, substring) })
}

func testOptions(t *testing.T) Options {
	t.Helper()

	material, err := reality.New()
	if err != nil {
		t.Fatalf("generating material: %v", err)
	}
	return Options{Material: material, Dest: "www.apple.com:443", ServerName: "www.apple.com"}
}

func run(t *testing.T, f *fakeRunner, opts Options) error {
	t.Helper()
	return Run(context.Background(), f, opts, nil)
}

func TestRun(t *testing.T) {
	f := newFakeRunner()
	opts := testOptions(t)

	if err := run(t, f, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, want := range []string{
		"apt-get",
		"sysctl --system",
		"sha256sum -c",
		"systemctl daemon-reload",
		"systemctl restart xray",
		"systemctl restart nginx",
		"ufw --force enable",
		"systemctl is-active xray",
	} {
		if !f.ranSomething(want) {
			t.Errorf("nothing ran containing %q", want)
		}
	}

	for _, path := range []string{configPath, unitPath, sysctlPath, decoyPath} {
		if _, ok := f.uploads[path]; !ok {
			t.Errorf("%s was never written", path)
		}
	}
}

// Enabling a default-deny firewall before SSH is allowed through it locks the
// door with the key inside, and the server is then only good for destroying.
func TestFirewallAllowsSSHBeforeItIsEnabled(t *testing.T) {
	f := newFakeRunner()

	if err := run(t, f, testOptions(t)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	firewall := f.commands[f.indexOf(t, "ufw --force enable")]
	allow := strings.Index(firewall, "ufw allow 22/tcp")
	enable := strings.Index(firewall, "ufw --force enable")
	if allow < 0 || allow > enable {
		t.Errorf("the firewall is enabled before SSH is allowed through it:\n%s", firewall)
	}
	if !strings.Contains(firewall, "ufw allow 443/tcp") {
		t.Errorf("the firewall does not let the tunnel through:\n%s", firewall)
	}
}

// The config carries the server's private key. It must never exist in a mode
// anything else on the machine could read.
func TestConfigIsWrittenUnreadableAndHandedToTheService(t *testing.T) {
	f := newFakeRunner()

	if err := run(t, f, testOptions(t)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	written := f.uploads[configPath]
	if written.mode.Perm() != 0o600 {
		t.Errorf("config written %04o, want 0600", written.mode.Perm())
	}

	if !f.ranSomething("chown " + serviceUser + ":" + serviceGroup + " " + configPath) {
		t.Error("the config is never handed to the account Xray runs as")
	}
	if !f.ranSomething("chmod 0400 " + configPath) {
		t.Error("the config is left writable by its owner")
	}
}

func TestConfigIsWhatXrayNeeds(t *testing.T) {
	f := newFakeRunner()
	opts := testOptions(t)

	if err := run(t, f, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var config map[string]any
	if err := json.Unmarshal([]byte(f.uploads[configPath].content), &config); err != nil {
		t.Fatalf("the config is not valid JSON: %v", err)
	}

	inbound := config["inbounds"].([]any)[0].(map[string]any)
	if got := inbound["port"].(float64); int(got) != Port {
		t.Errorf("port = %v, want %d", got, Port)
	}

	client := inbound["settings"].(map[string]any)["clients"].([]any)[0].(map[string]any)
	if client["id"] != opts.Material.UUID {
		t.Errorf("client id = %v, want the generated UUID", client["id"])
	}
	if client["flow"] != Flow {
		t.Errorf("flow = %v, want %s", client["flow"], Flow)
	}

	settings := inbound["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)
	if settings["dest"] != opts.Dest {
		t.Errorf("dest = %v, want %s", settings["dest"], opts.Dest)
	}
	if settings["privateKey"] != opts.Material.PrivateKey {
		t.Errorf("the config does not carry the generated private key")
	}
	if names := settings["serverNames"].([]any); len(names) != 1 || names[0] != opts.ServerName {
		t.Errorf("serverNames = %v, want [%s]", names, opts.ServerName)
	}
	if ids := settings["shortIds"].([]any); len(ids) != 1 || ids[0] != opts.Material.ShortID {
		t.Errorf("shortIds = %v, want the generated one", ids)
	}

	// A client presenting the public half has to reach a server holding the
	// private half of the same key.
	if strings.Contains(f.uploads[configPath].content, opts.Material.PublicKey) {
		t.Error("the public key was written to the server, which only needs the private one")
	}
}

// Reaching the provider's metadata service through the tunnel would hand out
// the account's own credentials.
func TestConfigBlocksPrivateAddresses(t *testing.T) {
	f := newFakeRunner()

	if err := run(t, f, testOptions(t)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(f.uploads[configPath].content, "geoip:private") {
		t.Errorf("nothing stops a client reaching a private address:\n%s", f.uploads[configPath].content)
	}
}

// Running as root would be one parsing bug away from handing over the machine.
func TestServiceDoesNotRunAsRoot(t *testing.T) {
	f := newFakeRunner()

	if err := run(t, f, testOptions(t)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	unit := f.uploads[unitPath].content
	if !strings.Contains(unit, "User="+serviceUser) {
		t.Errorf("the service does not drop privileges:\n%s", unit)
	}
	if !strings.Contains(unit, "AmbientCapabilities=CAP_NET_BIND_SERVICE") {
		t.Errorf("the service cannot bind 443 without root:\n%s", unit)
	}
	// Without this Xray cannot find geoip.dat, and the rule that blocks private
	// addresses stops it starting at all.
	if !strings.Contains(unit, "XRAY_LOCATION_ASSET="+sharePath) {
		t.Errorf("the service is not told where its geo files are:\n%s", unit)
	}
}

// What lands on the server has to be what was tested, from a host that has
// served the wrong thing before.
func TestXrayIsVerifiedAgainstAPinnedChecksum(t *testing.T) {
	f := newFakeRunner()

	if err := run(t, f, testOptions(t)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	install := f.commands[f.indexOf(t, "sha256sum -c")]
	if !strings.Contains(install, XrayVersion) {
		t.Errorf("the download is not pinned to a version:\n%s", install)
	}
	if !strings.Contains(install, xrayChecksums["64"]) {
		t.Errorf("the download is not checked against the recorded checksum:\n%s", install)
	}
	// Verification before installation, or it verifies nothing.
	if strings.Index(install, "sha256sum -c") > strings.Index(install, "install -m 0755") {
		t.Errorf("the binary is installed before it is verified:\n%s", install)
	}
}

func TestReleaseFollowsTheServersArchitecture(t *testing.T) {
	tests := map[string]string{
		"x86_64\n":  "64",
		"aarch64\n": "arm64-v8a",
	}

	for machine, want := range tests {
		f := newFakeRunner()
		f.replies["uname -m"] = machine

		asset, checksum, err := release(context.Background(), f)
		if err != nil {
			t.Fatalf("release(%q): %v", machine, err)
		}
		if asset != want {
			t.Errorf("release(%q) = %q, want %q", machine, asset, want)
		}
		if checksum != xrayChecksums[want] {
			t.Errorf("release(%q) returned the wrong checksum", machine)
		}
	}
}

// A guess here installs a binary the machine cannot run, and the failure comes
// much later and says much less.
func TestReleaseRefusesAnUnknownArchitecture(t *testing.T) {
	f := newFakeRunner()
	f.replies["uname -m"] = "riscv64\n"

	if _, _, err := release(context.Background(), f); err == nil || !strings.Contains(err.Error(), "riscv64") {
		t.Fatalf("got %v, want an error naming the architecture", err)
	}
}

// A kernel without BBR would leave the setting quietly ignored, and the whole
// step would have been for nothing.
func TestBBRIsChecked(t *testing.T) {
	f := newFakeRunner()
	f.replies["sysctl -n net.ipv4.tcp_congestion_control"] = "cubic\n"

	err := run(t, f, testOptions(t))
	if err == nil || !strings.Contains(err.Error(), "cubic") {
		t.Fatalf("got %v, want an error saying BBR did not take", err)
	}
}

// Every command exiting zero is not the same as a working server.
func TestRunFailsWhenXrayIsNotRunning(t *testing.T) {
	f := newFakeRunner()
	f.replies["systemctl is-active xray"] = "failed\n"
	f.replies["journalctl"] = "Feb 12 10:00:00 host xray[1]: invalid shortId\n"

	err := run(t, f, testOptions(t))
	if err == nil {
		t.Fatal("expected an error when the service is not running")
	}
	if !strings.Contains(err.Error(), "invalid shortId") {
		t.Errorf("error %q does not carry what the journal said", err)
	}
}

// The step that failed is the most useful thing an error here can name.
func TestRunNamesTheStepThatFailed(t *testing.T) {
	f := newFakeRunner()
	f.fail = "set -eu\nexport DEBIAN_FRONTEND"
	f.failErr = errors.New("dpkg was interrupted")

	err := run(t, f, testOptions(t))
	if err == nil {
		t.Fatal("expected the failure to come back")
	}
	if !strings.Contains(err.Error(), "installing packages") {
		t.Errorf("error %q does not name the step", err)
	}
	if !strings.Contains(err.Error(), "dpkg was interrupted") {
		t.Errorf("error %q loses what actually went wrong", err)
	}
}

// The wait runs to minutes, so a caller has something to say during it.
func TestRunReportsProgress(t *testing.T) {
	f := newFakeRunner()

	var steps []string
	err := Run(context.Background(), f, testOptions(t), func(step string) {
		steps = append(steps, step)
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(steps) < 5 {
		t.Errorf("only %d steps reported: %v", len(steps), steps)
	}
	if !slices.ContainsFunc(steps, func(s string) bool { return strings.Contains(s, XrayVersion) }) {
		t.Errorf("no step says which Xray version is going on: %v", steps)
	}
}

// Xray is started only once its config is there, and the firewall closes after
// the thing it is protecting is up.
func TestStepsAreOrdered(t *testing.T) {
	f := newFakeRunner()

	if err := run(t, f, testOptions(t)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !f.ranBefore(t, "upload "+configPath, "systemctl restart xray") {
		t.Error("xray is started before its config is written")
	}
	if !f.ranBefore(t, "systemctl restart xray", "ufw --force enable") {
		t.Error("the firewall closes before the server is up")
	}
	if !f.ranBefore(t, "sha256sum -c", "upload "+configPath) {
		t.Error("the config is written before xray is installed")
	}
}

// Re-running the bootstrap writes fresh key material. `systemctl enable --now`
// does nothing to a unit that is already running, so the server would carry on
// serving the old config while local state recorded the new one - and every
// client would be turned away as a stranger.
func TestXrayIsRestartedNotJustEnabled(t *testing.T) {
	f := newFakeRunner()

	if err := run(t, f, testOptions(t)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !f.ranSomething("systemctl restart xray") {
		t.Errorf("xray is never restarted, so a rewritten config never takes effect:\n%s",
			strings.Join(f.commands, "\n"))
	}
	if f.ranSomething("enable --now xray") {
		t.Error("`enable --now` does nothing when the unit is already running")
	}
}
