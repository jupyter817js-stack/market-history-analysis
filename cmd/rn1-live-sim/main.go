// Command rn1-live-sim is the read-only Construction Simulator
// (live mode). It reads JSON output produced by rn1-live-scan
// (--format json) from a file or stdin and emits per-candidate
// descriptive observability labels against the RN1 9-state model.
//
// The simulator does NOT trade, does NOT place orders, does NOT
// cancel orders, does NOT sign anything, does NOT read wallet or
// account state, does NOT call any authenticated endpoint, and does
// NOT produce buy/sell/sizing/PnL/arbitrage language. Every output
// label is descriptive — it describes the public-data shape of a
// candidate, never an action.
//
// Usage:
//
//	rn1-live-sim --input <path>     [--format table|json] [--label-min PROBE_ELIGIBLE]
//	cat candidates.json | rn1-live-sim --format json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"rn1-strategy-extractor/internal/scanner"
	"rn1-strategy-extractor/internal/simulator"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	var (
		inputPath  string
		outFormat  string
		labelMin   string
		outputPath string
	)
	flag.StringVar(&inputPath, "input", "", "scanner JSON path (omit or use \"-\" to read stdin)")
	flag.StringVar(&outFormat, "format", "table", "output format: table|json")
	flag.StringVar(&labelMin, "label-min", "", "only emit traces whose Labels contain this label (e.g. PROBE_ELIGIBLE)")
	flag.StringVar(&outputPath, "out", "", "output path (default stdout)")
	flag.Parse()

	body, err := readInput(inputPath)
	if err != nil {
		slog.Error("read input", "err", err)
		os.Exit(1)
	}

	var candidates []scanner.Candidate
	if err := json.Unmarshal(body, &candidates); err != nil {
		slog.Error("decode scanner JSON", "err", err)
		os.Exit(1)
	}

	now := time.Now().UTC()
	traces := simulator.BuildTraces(candidates, now)

	if labelMin != "" {
		want := simulator.Label(strings.TrimSpace(labelMin))
		filtered := traces[:0]
		for _, t := range traces {
			if hasLabel(t.Labels, want) {
				filtered = append(filtered, t)
			}
		}
		traces = filtered
	}

	out, err := openOutput(outputPath)
	if err != nil {
		slog.Error("open output", "err", err)
		os.Exit(1)
	}
	defer out.Close()

	switch strings.ToLower(outFormat) {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(traces)
	default:
		printTable(out, traces)
	}
}

func readInput(path string) ([]byte, error) {
	if path == "" || path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

type writeCloser struct {
	io.Writer
	c io.Closer
}

func (w writeCloser) Close() error {
	if w.c == nil {
		return nil
	}
	return w.c.Close()
}

func openOutput(path string) (io.WriteCloser, error) {
	if path == "" {
		return writeCloser{Writer: os.Stdout}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func hasLabel(labels []simulator.Label, want simulator.Label) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

func printTable(w io.Writer, ts []simulator.Trace) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "rank\tevent_slug\tsport\tscore\tband\tgate\tlabels\tquote_cov\trisk\treview")
	for _, t := range ts {
		labels := make([]string, 0, len(t.Labels))
		for _, l := range t.Labels {
			labels = append(labels, string(l))
		}
		risk := strings.Join(t.RiskFlags, ",")
		if risk == "" {
			risk = "-"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%.2f\t%s\t%s\t%s\t%.2f\t%s\t%s\n",
			t.Rank,
			t.EventSlug,
			orDash(t.SportPrefix),
			t.SelectorScore,
			t.ScoreBand,
			t.LiquidityGate,
			strings.Join(labels, "+"),
			t.QuoteCoverage,
			risk,
			t.HumanReviewStatus,
		)
	}
	_ = tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
