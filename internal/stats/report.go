package stats

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

func humanBytes(n float64) string {
	const u = 1024.0
	switch {
	case n >= u*u*u:
		return fmt.Sprintf("%.2f GiB", n/(u*u*u))
	case n >= u*u:
		return fmt.Sprintf("%.2f MiB", n/(u*u))
	case n >= u:
		return fmt.Sprintf("%.2f KiB", n/u)
	default:
		return fmt.Sprintf("%.0f B", n)
	}
}

func dur(d time.Duration) string {
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1000.0)
	case d >= time.Microsecond:
		return fmt.Sprintf("%.2fµs", float64(d.Nanoseconds())/1000.0)
	default:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	}
}

// WriteReport renders a human-readable summary for one size group.
func WriteReport(w io.Writer, label string, s Summary) {
	fmt.Fprintf(w, "\n========== %s ==========\n", label)
	fmt.Fprintf(w, "wall: %s\n", dur(s.Wall))

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "op\tcount\terr\tqps\tthroughput\tmin\tmean\tp50\tp90\tp95\tp99\tmax")
	writeRow(tw, "upload", s.Upload)
	writeRow(tw, "download", s.Download)
	writeRow(tw, "overall", s.Overall)
	tw.Flush()
}

func writeRow(w io.Writer, name string, ls LatencyStats) {
	if ls.Count == 0 {
		fmt.Fprintf(w, "%s\t-\t-\t-\t-\t-\t-\t-\t-\t-\t-\t-\n", name)
		return
	}
	fmt.Fprintf(w, "%s\t%d\t%d\t%.1f\t%s/s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		name,
		ls.Count,
		ls.Errors,
		ls.QPS,
		humanBytes(ls.ThroughputBps),
		dur(ls.Min),
		dur(ls.Mean),
		dur(ls.P50),
		dur(ls.P90),
		dur(ls.P95),
		dur(ls.P99),
		dur(ls.Max),
	)
}
