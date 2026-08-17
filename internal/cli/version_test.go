package cli

import (
	"bytes"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("executing %v: %v", args, err)
	}
	return out.String()
}

func TestVersionCommand(t *testing.T) {
	got := run(t, "version")
	if !strings.HasPrefix(got, "vpncli ") {
		t.Errorf("version output = %q, want it to start with %q", got, "vpncli ")
	}
	if !strings.Contains(got, "go1.") {
		t.Errorf("version output = %q, want it to name the Go version", got)
	}
}

func TestVersionShort(t *testing.T) {
	got := strings.TrimSpace(run(t, "version", "--short"))
	if got == "" {
		t.Fatal("--short printed nothing")
	}
	if strings.Contains(got, " ") {
		t.Errorf("--short printed %q, want a bare version string", got)
	}
}

func TestVersionFallsBackWhenUnstamped(t *testing.T) {
	// `go test` builds an unstamped binary, so this exercises the fallback.
	v, _ := buildInfo()
	if v == "" {
		t.Error("buildInfo returned an empty version")
	}
}
