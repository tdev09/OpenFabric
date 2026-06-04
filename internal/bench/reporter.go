package bench

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// PrintReport writes a formatted benchmark report to the given writer.
// Designed to look good in both terminal and log output.
func PrintReport(w io.Writer, report *BenchReport) {
	line := strings.Repeat("─", 60)

	fmt.Fprintf(w, "\n%s\n", line)
	fmt.Fprintf(w, "  %s BENCHMARK REPORT\n", "OPENFABRIC")
	fmt.Fprintf(w, "  ID: %s   Nodes: %d   RAM: %s\n",
		report.ID,
		report.NodeCount,
		formatBytes(report.TotalRAMBytes),
	)
	fmt.Fprintf(w, "  %s\n", report.RunAt.Format(time.RFC1123))
	fmt.Fprintf(w, "%s\n\n", line)

	suiteOrder := []SuiteID{
		SuiteRoundTrip,
		SuiteScheduler,
		SuiteStorage,
		SuiteNetwork,
		SuiteInference,
	}

	for _, sid := range suiteOrder {
		sr, ok := report.Suites[sid]
		if !ok {
			continue
		}

		fmt.Fprintf(w, "  %-20s", suiteName(sid))

		if sr.Error != "" {
			fmt.Fprintf(w, "SKIPPED - %s\n", sr.Error)
			continue
		}

		if sr.Samples == 0 {
			fmt.Fprintf(w, "NO DATA\n")
			continue
		}

		fmt.Fprintf(w, "p50=%-12s p95=%-12s p99=%-12s mean=%-12s\n",
			FormatValue(sr.Stats.P50, sr.Unit),
			FormatValue(sr.Stats.P95, sr.Unit),
			FormatValue(sr.Stats.P99, sr.Unit),
			FormatValue(sr.Stats.Mean, sr.Unit),
		)
	}

	fmt.Fprintf(w, "\n%s\n", line)

	// Signature verification
	if report.Signature != "" {
		if err := report.Verify(); err != nil {
			fmt.Fprintf(w, "  ⚠ Signature INVALID: %v\n", err)
		} else {
			fmt.Fprintf(w, "  ✓ Signed and verified\n")
		}
	}
	fmt.Fprintf(w, "%s\n\n", line)
}

func suiteName(id SuiteID) string {
	names := map[SuiteID]string{
		SuiteRoundTrip: "Task round-trip",
		SuiteScheduler: "Scheduler",
		SuiteStorage:   "Storage sync",
		SuiteNetwork:   "Node bandwidth",
		SuiteInference: "LLM inference",
	}
	if n, ok := names[id]; ok {
		return n
	}
	return string(id)
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
