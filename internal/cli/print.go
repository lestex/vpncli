package cli

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lestex/vpncli/internal/provider"
	"github.com/lestex/vpncli/internal/state"
)

// row is one line of a server listing, whatever it was read from. Going
// through it is what keeps a listing from the API and one from local state
// looking the same - the only difference is which id the first column shows.
type row struct {
	id string
	// provider is empty for a listing that came from one provider's own API,
	// where naming it on every line would say nothing. Local state holds
	// servers from several, so there it is the column that tells them apart.
	provider  string
	name      string
	region    string
	size      string
	image     string
	ipv4      string
	status    string
	createdAt time.Time
}

// printInstances writes the live view from a provider API. The id column is
// the provider's own.
func printInstances(w io.Writer, instances []provider.VPSInstance) error {
	rows := make([]row, 0, len(instances))
	for _, inst := range instances {
		rows = append(rows, row{
			id:        inst.ID,
			name:      inst.Name,
			region:    inst.Region,
			size:      inst.Size,
			image:     inst.Image,
			ipv4:      inst.IPv4,
			status:    string(inst.Status),
			createdAt: inst.CreatedAt,
		})
	}
	return printRows(w, rows)
}

// printServers writes the persisted view. The id column is the short local one
// the user types.
func printServers(w io.Writer, servers []state.Server) error {
	rows := make([]row, 0, len(servers))
	for _, srv := range servers {
		rows = append(rows, row{
			id:        strconv.FormatInt(srv.ID, 10),
			provider:  srv.Provider,
			name:      srv.Name,
			region:    srv.Region,
			size:      srv.Size,
			image:     srv.Image,
			ipv4:      srv.IPv4,
			status:    srv.Status,
			createdAt: srv.CreatedAt,
		})
	}
	return printRows(w, rows)
}

// printRows writes rows as an aligned table.
func printRows(w io.Writer, rows []row) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "no servers found")
		return err
	}

	// The provider column appears only where it distinguishes anything: a
	// listing straight from one provider's API is all one provider.
	named := slices.ContainsFunc(rows, func(r row) bool { return r.provider != "" })

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	header := []string{"ID", "PROVIDER", "NAME", "REGION", "SIZE", "IMAGE", "IPV4", "STATUS", "AGE"}
	if !named {
		header = slices.DeleteFunc(header, func(c string) bool { return c == "PROVIDER" })
	}
	fmt.Fprintln(tw, strings.Join(header, "\t"))

	for _, r := range rows {
		fields := []string{r.id}
		if named {
			fields = append(fields, orDash(r.provider))
		}
		fields = append(fields,
			orDash(r.name),
			orDash(r.region),
			orDash(r.size),
			orDash(r.image),
			orDash(r.ipv4),
			orDash(r.status),
			age(r.createdAt),
		)
		fmt.Fprintln(tw, strings.Join(fields, "\t"))
	}

	return tw.Flush()
}

// orDash keeps columns aligned when a field is empty.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// age renders a creation timestamp as a compact relative duration.
func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}

	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
