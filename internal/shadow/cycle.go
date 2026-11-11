package shadow

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"rn1-strategy-extractor/internal/scanner"
	"rn1-strategy-extractor/internal/simulator"
)

// CycleInput configures a one-shot bulk pass over the artifacts a
// single rn1-live-scan / rn1-live-sim / rn1-live-diff cycle wrote.
// Unlike Add/Update, which work one slug at a time, Cycle walks every
// candidate in the scan and decides on a per-slug basis:
//
//   - candidate present, slug already tracked → Update (decision empty,
//     so prior decision is carried forward unless the CLOSED_EVENT
//     auto-rule fires)
//   - candidate present, slug not yet tracked → Add as
//     DiscoverFilter.NewDecision (default DISCOVERED) IF the candidate
//     passes the discover filter; else skip
//
// Cycle never overrides an existing operator decision. The decision
// field on each Update call is left empty, so the library's
// "operator-supplied decision wins; otherwise keep prior" rule applies.
type CycleInput struct {
	LogPath        string
	ScanPath       string
	SimPath        string // optional ""
	DiffPath       string // optional ""
	DiscoverFilter DiscoverFilter
	Now            time.Time // zero = time.Now().UTC()
	AppendSource   string    // audit tag, e.g. "rn1-shadow cycle"
}

// DiscoverFilter controls which newly-seen candidates are auto-added.
// The zero value matches nothing — auto-discovery is opt-in.
type DiscoverFilter struct {
	// Enabled = false: do not auto-add any new candidate. The operator
	// is expected to run `add` manually. Cycle still updates already-
	// tracked slugs.
	Enabled bool

	// NewDecision is the closed-enum decision assigned to each
	// auto-added record. Defaults to DISCOVERED when empty.
	NewDecision ShadowDecision

	// MinScore is the inclusive lower bound on Candidate.Score. 0 means
	// no floor (every eligible candidate qualifies).
	MinScore float64

	// Bands, when non-empty, restricts auto-discovery to candidates
	// whose canonical ScoreBand is in this set. Example: {"very_strong"}.
	Bands map[string]bool

	// Gates, when non-empty, restricts auto-discovery to candidates
	// whose LiquidityGate is in this set. Example: {"PASS":true,"WARN":true}.
	Gates map[string]bool

	// MaxCandidates caps the number of candidates auto-discovered in a
	// single Cycle call. 0 means no cap. Useful to keep first-cycle
	// catch-up bounded.
	MaxCandidates int
}

// CycleResult is the per-call summary. Each slice contains the
// ShadowRecord that Cycle wrote, in scan-output order.
type CycleResult struct {
	Discovered []ShadowRecord     // newly added (DISCOVERED or NewDecision)
	Ticked     []ShadowRecord     // existing slugs updated (decision unchanged)
	AutoClosed []ShadowRecord     // existing slugs auto-transitioned to CLOSED_EVENT
	Skipped    []CycleSkip        // candidates not processed and the reason why
	Counts     CycleCounts        // numeric counts for logging / summary
}

// CycleSkip records a per-slug decision to do nothing this cycle.
type CycleSkip struct {
	EventSlug string `json:"event_slug"`
	Reason    string `json:"reason"`
}

// CycleCounts is the at-a-glance count panel for the result.
type CycleCounts struct {
	ScanCandidates    int `json:"scan_candidates"`
	AlreadyTracked    int `json:"already_tracked"`
	Discovered        int `json:"discovered"`
	Ticked            int `json:"ticked"`
	AutoClosed        int `json:"auto_closed"`
	SkippedFilter     int `json:"skipped_filter"`
	SkippedCap        int `json:"skipped_max_cap"`
}

// Cycle reads the scan / sim / diff artifacts in CycleInput, walks
// every candidate, and bulk-applies Add (for new slugs that pass the
// discover filter) and Update (for already-tracked slugs).
//
// Cycle is the bookkeeping primitive the operator runs once per cycle.
// It does not modify any operator decision — every Update is called
// with an empty Decision field, so the library's "carry-prior-or-
// auto-CLOSED_EVENT" rule applies. The operator continues to set or
// change decisions with the existing `update` subcommand or by editing
// the log via the regular flow.
func Cycle(in CycleInput) (CycleResult, error) {
	var res CycleResult

	if in.LogPath == "" {
		return res, fmt.Errorf("shadow log path required")
	}
	if in.ScanPath == "" {
		return res, fmt.Errorf("scan path required")
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if in.AppendSource == "" {
		in.AppendSource = "rn1-shadow cycle"
	}

	// Read scan.
	scanBody, err := os.ReadFile(in.ScanPath)
	if err != nil {
		return res, fmt.Errorf("read scan %q: %w", in.ScanPath, err)
	}
	var cands []scanner.Candidate
	if err := json.Unmarshal(scanBody, &cands); err != nil {
		return res, fmt.Errorf("decode scan JSON %q: %w", in.ScanPath, err)
	}
	res.Counts.ScanCandidates = len(cands)

	// Read sim (optional).
	var traces []simulator.Trace
	if in.SimPath != "" {
		simBody, err := os.ReadFile(in.SimPath)
		if err != nil {
			return res, fmt.Errorf("read sim %q: %w", in.SimPath, err)
		}
		if err := json.Unmarshal(simBody, &traces); err != nil {
			return res, fmt.Errorf("decode sim JSON %q: %w", in.SimPath, err)
		}
	}
	traceBySlug := map[string]*simulator.Trace{}
	for i := range traces {
		t := &traces[i]
		traceBySlug[t.EventSlug] = t
	}

	// Read diff (optional). Diff JSON is an array of records each with
	// EventSlug + DiffClasses. We only need the classes per slug.
	type diffShape struct {
		EventSlug   string   `json:"EventSlug"`
		DiffClasses []string `json:"DiffClasses"`
	}
	diffBySlug := map[string][]string{}
	if in.DiffPath != "" {
		diffBody, err := os.ReadFile(in.DiffPath)
		if err != nil {
			return res, fmt.Errorf("read diff %q: %w", in.DiffPath, err)
		}
		var diffs []diffShape
		if err := json.Unmarshal(diffBody, &diffs); err != nil {
			return res, fmt.Errorf("decode diff JSON %q: %w", in.DiffPath, err)
		}
		for _, d := range diffs {
			diffBySlug[d.EventSlug] = append([]string(nil), d.DiffClasses...)
		}
	}

	// Read existing shadow log → slug → tracked?
	existing, err := ReadLog(in.LogPath)
	if err != nil {
		return res, fmt.Errorf("read shadow log %q: %w", in.LogPath, err)
	}
	tracked := map[string]bool{}
	for _, r := range existing {
		tracked[r.EventSlug] = true
	}
	res.Counts.AlreadyTracked = len(tracked)

	newDecision := in.DiscoverFilter.NewDecision
	if newDecision == "" {
		newDecision = DecisionDiscovered
	}

	for _, c := range cands {
		slug := c.EventSlug
		obs := Observation{
			Candidate:        &c,
			Trace:            traceBySlug[slug],
			DiffClassesSince: diffBySlug[slug],
			Now:              now,
			AppendSource:     in.AppendSource,
		}

		if tracked[slug] {
			// Existing slug: tick it.
			rec, err := Update(UpdateInput{
				LogPath:   in.LogPath,
				EventSlug: slug,
				Decision:  "",
				Obs:       obs,
			})
			if err != nil {
				res.Skipped = append(res.Skipped, CycleSkip{
					EventSlug: slug,
					Reason:    fmt.Sprintf("update error: %v", err),
				})
				continue
			}
			if rec.ShadowDecision == DecisionClosedEvent {
				// Was it already closed at prior tick, or did this tick
				// trigger the auto-close? Check decision history.
				if len(rec.DecisionHistory) >= 2 &&
					rec.DecisionHistory[len(rec.DecisionHistory)-2] != string(DecisionClosedEvent) {
					res.AutoClosed = append(res.AutoClosed, rec)
					res.Counts.AutoClosed++
				} else {
					res.Ticked = append(res.Ticked, rec)
					res.Counts.Ticked++
				}
			} else {
				res.Ticked = append(res.Ticked, rec)
				res.Counts.Ticked++
			}
			continue
		}

		// New slug: consider auto-discovery.
		if !in.DiscoverFilter.Enabled {
			res.Skipped = append(res.Skipped, CycleSkip{
				EventSlug: slug,
				Reason:    "auto-discover disabled",
			})
			continue
		}
		if !discoverFilterAccepts(in.DiscoverFilter, &c) {
			res.Skipped = append(res.Skipped, CycleSkip{
				EventSlug: slug,
				Reason:    "discover filter rejected",
			})
			res.Counts.SkippedFilter++
			continue
		}
		if in.DiscoverFilter.MaxCandidates > 0 && res.Counts.Discovered >= in.DiscoverFilter.MaxCandidates {
			res.Skipped = append(res.Skipped, CycleSkip{
				EventSlug: slug,
				Reason:    "discover cap reached",
			})
			res.Counts.SkippedCap++
			continue
		}

		rec, err := Add(AddInput{
			LogPath:   in.LogPath,
			EventSlug: slug,
			Decision:  newDecision,
			Obs:       obs,
		})
		if err != nil {
			res.Skipped = append(res.Skipped, CycleSkip{
				EventSlug: slug,
				Reason:    fmt.Sprintf("add error: %v", err),
			})
			continue
		}
		res.Discovered = append(res.Discovered, rec)
		res.Counts.Discovered++
		// mark tracked so a duplicate scan row (unusual) doesn't try to re-add
		tracked[slug] = true
	}

	// Deterministic order for output / tests.
	sort.Slice(res.Discovered, func(i, j int) bool { return res.Discovered[i].EventSlug < res.Discovered[j].EventSlug })
	sort.Slice(res.Ticked, func(i, j int) bool { return res.Ticked[i].EventSlug < res.Ticked[j].EventSlug })
	sort.Slice(res.AutoClosed, func(i, j int) bool { return res.AutoClosed[i].EventSlug < res.AutoClosed[j].EventSlug })
	sort.Slice(res.Skipped, func(i, j int) bool { return res.Skipped[i].EventSlug < res.Skipped[j].EventSlug })
	return res, nil
}

func discoverFilterAccepts(f DiscoverFilter, c *scanner.Candidate) bool {
	if c == nil {
		return false
	}
	if f.MinScore > 0 && c.Score < f.MinScore {
		return false
	}
	if len(f.Bands) > 0 && !f.Bands[strings.ToLower(c.Band)] {
		return false
	}
	if len(f.Gates) > 0 && !f.Gates[string(c.LiquidityGate)] {
		return false
	}
	return true
}
