package cli

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lestex/vpncli/internal/provider"
	"github.com/lestex/vpncli/internal/state"
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

func TestPrintServers(t *testing.T) {
	var buf bytes.Buffer
	err := printServers(&buf, []state.Server{
		{
			ID:         7,
			ProviderID: "1001",
			Name:       "vpncli-fra1-a1b2",
			Region:     "fra1",
			Size:       "s-1vcpu-1gb",
			Image:      "ubuntu-24-04-x64",
			IPv4:       "203.0.113.10",
			Status:     "active",
			CreatedAt:  time.Now().Add(-3 * time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("printServers: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want a header plus one row:\n%s", len(lines), buf.String())
	}
	// The local id is what other commands take, so it leads the row.
	if !strings.HasPrefix(lines[1], "7") {
		t.Errorf("row does not lead with the local id: %q", lines[1])
	}
	if strings.Contains(lines[1], "1001") {
		t.Errorf("row shows the provider id: %q", lines[1])
	}
}

// A listing from state and one from the API read as the same table, apart from
// the provider: state can hold servers from several and has to say which, an
// API listing is all one provider and naming it every line says nothing.
func TestListingsShareAHeaderApartFromTheProvider(t *testing.T) {
	var fromAPI, fromState bytes.Buffer
	if err := printInstances(&fromAPI, []provider.VPSInstance{{ID: "1001", Provider: "digitalocean"}}); err != nil {
		t.Fatalf("printInstances: %v", err)
	}
	if err := printServers(&fromState, []state.Server{{ID: 7, Provider: "digitalocean"}}); err != nil {
		t.Fatalf("printServers: %v", err)
	}

	// Compare the column names, not the raw lines: tabwriter pads to the width
	// of the content, so a wider id shifts everything after it.
	apiHeader := strings.Fields(strings.Split(fromAPI.String(), "\n")[0])
	stateHeader := strings.Fields(strings.Split(fromState.String(), "\n")[0])

	if slices.Contains(apiHeader, "PROVIDER") {
		t.Errorf("a listing from one provider names it on every line: %v", apiHeader)
	}
	if !slices.Contains(stateHeader, "PROVIDER") {
		t.Errorf("a listing from state does not say which provider: %v", stateHeader)
	}

	// Everything else has to line up, or the two read as different tables.
	if without := slices.DeleteFunc(stateHeader, func(c string) bool { return c == "PROVIDER" }); !slices.Equal(apiHeader, without) {
		t.Errorf("headers differ beyond the provider:\napi   %v\nstate %v", apiHeader, without)
	}
}

// The provider is the second column, next to the id: together they are what
// names a server, and the id alone stops being unique the moment a second
// provider is configured.
func TestServerListingNamesTheProvider(t *testing.T) {
	var buf bytes.Buffer
	err := printServers(&buf, []state.Server{
		{ID: 1, Provider: "digitalocean", Name: "vpncli-fra1-a1b2", IPv4: "203.0.113.10"},
		{ID: 2, Provider: "hetzner", Name: "vpncli-fsn1-c3d4", IPv4: "203.0.113.20"},
	})
	if err != nil {
		t.Fatalf("printServers: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if header := strings.Fields(lines[0]); len(header) < 2 || header[1] != "PROVIDER" {
		t.Fatalf("header = %v, want PROVIDER second", header)
	}
	for i, want := range []string{"digitalocean", "hetzner"} {
		if got := strings.Fields(lines[i+1]); len(got) < 2 || got[1] != want {
			t.Errorf("row %d = %v, want %q in the provider column", i, got, want)
		}
	}
}

func TestPrintServersEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := printServers(&buf, nil); err != nil {
		t.Fatalf("printServers: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "no servers found" {
		t.Errorf("got %q, want the empty message", got)
	}
}
