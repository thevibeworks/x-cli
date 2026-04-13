package cmdutil

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// PrintJSON emits a value as indented JSON to stdout. Used by every
// command's `--json` flag path.
func PrintJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// TabPrinter writes key/value rows aligned via a tabwriter.
type TabPrinter struct {
	tw *tabwriter.Writer
}

func NewTabPrinter(w io.Writer) *TabPrinter {
	return &TabPrinter{tw: tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)}
}

func (p *TabPrinter) Row(k string, v any) {
	fmt.Fprintf(p.tw, "%s\t%v\n", k, v)
}

func (p *TabPrinter) Flush() error { return p.tw.Flush() }

// HumanCount formats large integers as 1.2k, 3.4M, etc. Used in compact
// list views where space is at a premium.
func HumanCount(n int) string {
	abs := n
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1_000_000_000)
	case abs >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case abs >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// RelTime returns "5m", "3h", "2d" relative to now for an RFC3339 string.
func RelTime(iso string) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// TruncateRunes returns a string of at most `n` runes, appending "…" if
// the original was longer. Operates on runes so multi-byte characters
// (Chinese, emoji) don't get corrupted at the truncation point.
func TruncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// SingleLine collapses internal newlines into spaces so a tweet body
// fits one row. Used in compact list views.
func SingleLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ")
}
