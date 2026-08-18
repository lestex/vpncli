package cli

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/lestex/vpncli/internal/provider"
)

// printInstances writes instances as an aligned table. Shared so listings
// from the API and from local state stay visually identical.
func printInstances(w io.Writer, instances []provider.VPSInstance) error {
	if len(instances) == 0 {
		_, err := fmt.Fprintln(w, "no servers found")
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tREGION\tSIZE\tIMAGE\tIPV4\tSTATUS\tAGE")

	for _, inst := range instances {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			inst.ID,
			orDash(inst.Name),
			orDash(inst.Region),
			orDash(inst.Size),
			orDash(inst.Image),
			orDash(inst.IPv4),
			inst.Status,
			age(inst.CreatedAt),
		)
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
