package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/lestex/vpncli/internal/provider"
)

func TestPrintInstancesEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := printInstances(&buf, nil); err != nil {
		t.Fatalf("printInstances: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "no servers found" {
		t.Errorf("got %q, want the empty-account message", got)
	}
}

func TestPrintInstances(t *testing.T) {
	var buf bytes.Buffer
	err := printInstances(&buf, []provider.VPSInstance{
		{
			ID:        "1001",
			Name:      "vpncli-fra1-a1b2",
			Region:    "fra1",
			Size:      "s-1vcpu-1gb",
			Image:     "ubuntu-24-04-x64",
			IPv4:      "203.0.113.10",
			Status:    provider.StatusActive,
			CreatedAt: time.Now().Add(-3 * time.Hour),
		},
		{
			// Still building: empty columns should render as dashes.
			ID:     "1002",
			Name:   "vpncli-ams3-c3d4",
			Status: provider.StatusProvisioning,
		},
	})
	if err != nil {
		t.Fatalf("printInstances: %v", err)
	}

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want a header plus 2 rows:\n%s", len(lines), out)
	}

	for _, want := range []string{"ID", "NAME", "REGION", "SIZE", "IMAGE", "IPV4", "STATUS", "AGE"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("header is missing %q: %q", want, lines[0])
		}
	}
	for _, want := range []string{"1001", "vpncli-fra1-a1b2", "203.0.113.10", "active", "3h"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("row is missing %q: %q", want, lines[1])
		}
	}
	if !strings.Contains(lines[2], "-") || !strings.Contains(lines[2], "provisioning") {
		t.Errorf("incomplete row did not render dashes: %q", lines[2])
	}
}

func TestAge(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		at   time.Time
		want string
	}{
		{"zero time", time.Time{}, "-"},
		{"seconds", now.Add(-10 * time.Second), "just now"},
		{"minutes", now.Add(-5 * time.Minute), "5m"},
		{"hours", now.Add(-3 * time.Hour), "3h"},
		{"days", now.Add(-50 * time.Hour), "2d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := age(tt.at); got != tt.want {
				t.Errorf("age(%v) = %q, want %q", tt.at, got, tt.want)
			}
		})
	}
}

func TestOrDash(t *testing.T) {
	if got := orDash(""); got != "-" {
		t.Errorf("orDash(\"\") = %q, want %q", got, "-")
	}
	if got := orDash("fra1"); got != "fra1" {
		t.Errorf("orDash(\"fra1\") = %q, want it unchanged", got)
	}
}
