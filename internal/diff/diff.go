package diff

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"rn1-strategy-extractor/internal/scanner"
	"rn1-strategy-extractor/internal/simulator"
)

// Snapshot is one side of the diff: a loaded scanner JSON and an
// optional simulator trace JSON, with metadata.
type Snapshot struct {
	Meta       SnapshotMeta
	Candidates []scanner.Candidate
	Traces     []simulator.Trace // optional; nil/empty if not provided
}

// LoadSnapshot reads a scanner JSON file and optionally a
// simulator trace JSON file. The trace path may be empty.
//
// Returns a populated Snapshot or a descriptive error. Network
// I/O is never performed.
func LoadSnapshot(scanPath, simPath, label string) (*Snapshot, error) {
	if scanPath == "" {
		return nil, fmt.Errorf("scan path required (label=%q)", label)
	}
	scanBody, err := os.ReadFile(scanPath)
	if err != nil {
		return nil, fmt.Errorf("read scan %q: %w", scanPath, err)
	}
	var cands []scanner.Candidate
	if err := json.Unmarshal(scanBody, &cands); err != nil {
		return nil, fmt.Errorf("decode scan JSON %q: %w", scanPath, err)
	}
	var traces []simulator.Trace
	if simPath != "" {
		simBody, err := os.ReadFile(simPath)
		if err != nil {
			return nil, fmt.Errorf("read sim trace %q: %w", simPath, err)
		}
		if err := json.Unmarshal(simBody, &traces); err != nil {
			return nil, fmt.Errorf("decode sim trace JSON %q: %w", simPath, err)
		}
	}

	meta := SnapshotMeta{
		Label:     label,
		Path:      scanPath,
		ScannedAt: earliestScannedAt(cands),
	}
	return &Snapshot{Meta: meta, Candidates: cands, Traces: traces}, nil
}

func earliestScannedAt(cands []scanner.Candidate) time.Time {
	var t time.Time
	for _, c := range cands {
		if t.IsZero() || c.ScannedAt.Before(t) {
			t = c.ScannedAt
		}
	}
	return t
}

// FreshnessOptions controls the B-after-A and max-age checks.
type FreshnessOptions struct {
	// MaxAge limits how far apart A and B may be before the diff
	// prefixes its notes with a STALE_SNAPSHOT warning (non-fatal).
	// Zero disables the check. Default 7 days when applied from
	// CLI.
	MaxAge time.Duration
}

// Result wraps the diff output with any non-fatal warnings.
type Result struct {
	Records  []DiffRecord
	Warnings []string
}

// Compute produces the diff records for the candidate union of
// snapshots A and B. The function is offline and pure; it makes
// no network calls. The B-after-A invariant is enforced; pairs
// where B is not strictly after A return an error.
func Compute(a, b *Snapshot, cfg ThresholdConfig, fresh FreshnessOptions) (*Result, error) {
	if a == nil || b == nil {
		return nil, fmt.Errorf("both snapshots required")
	}
	if !a.Meta.ScannedAt.IsZero() && !b.Meta.ScannedAt.IsZero() {
		if !b.Meta.ScannedAt.After(a.Meta.ScannedAt) {
			return nil, fmt.Errorf(
				"snapshot B (scanned at %s) must be strictly later than snapshot A (scanned at %s)",
				b.Meta.ScannedAt.Format(time.RFC3339),
				a.Meta.ScannedAt.Format(time.RFC3339),
			)
		}
	}

	res := &Result{}
	if fresh.MaxAge > 0 && !a.Meta.ScannedAt.IsZero() && !b.Meta.ScannedAt.IsZero() {
		if b.Meta.ScannedAt.Sub(a.Meta.ScannedAt) > fresh.MaxAge {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"STALE_SNAPSHOT: A and B are %s apart (max-age=%s)",
				b.Meta.ScannedAt.Sub(a.Meta.ScannedAt).Round(time.Second),
				fresh.MaxAge,
			))
		}
	}

	aIdx, aDupes, err := indexBySlug(a.Candidates, a.Traces)
	if err != nil {
		return nil, err
	}
	bIdx, bDupes, err := indexBySlug(b.Candidates, b.Traces)
	if err != nil {
		return nil, err
	}
	for _, d := range aDupes {
		res.Warnings = append(res.Warnings, fmt.Sprintf("DUPLICATE_SLUG in A: %s", d))
	}
	for _, d := range bDupes {
		res.Warnings = append(res.Warnings, fmt.Sprintf("DUPLICATE_SLUG in B: %s", d))
	}

	seen := map[string]bool{}
	for slug, ae := range aIdx {
		seen[slug] = true
		be, inB := bIdx[slug]
		if !inB {
			res.Records = append(res.Records, makeDroppedRecord(slug, ae, a.Meta.ScannedAt, b.Meta.ScannedAt))
			continue
		}
		classes := classify(ae, be, cfg)
		res.Records = append(res.Records, makeBothSidesRecord(slug, ae, be, classes, a.Meta.ScannedAt, b.Meta.ScannedAt))
	}
	for slug, be := range bIdx {
		if seen[slug] {
			continue
		}
		res.Records = append(res.Records, makeNewRecord(slug, be, a.Meta.ScannedAt, b.Meta.ScannedAt))
	}

	sortRecords(res.Records)
	return res, nil
}

// indexBySlug groups candidates and (optional) traces by event
// slug. Returns the index, the list of duplicate slugs seen
// (warning, not fatal), and a fatal error if the input is
// structurally broken.
func indexBySlug(cands []scanner.Candidate, traces []simulator.Trace) (map[string]*snapshotEntry, []string, error) {
	out := map[string]*snapshotEntry{}
	dupes := map[string]bool{}
	for i := range cands {
		slug := cands[i].EventSlug
		if slug == "" {
			continue
		}
		if _, ok := out[slug]; ok {
			dupes[slug] = true
			continue // keep the first instance
		}
		out[slug] = &snapshotEntry{Candidate: &cands[i]}
	}
	for i := range traces {
		slug := traces[i].EventSlug
		if slug == "" {
			continue
		}
		if e, ok := out[slug]; ok {
			e.Trace = &traces[i]
		}
		// Trace without matching candidate is silently ignored —
		// scanner JSON is authoritative for membership.
	}
	dlist := make([]string, 0, len(dupes))
	for d := range dupes {
		dlist = append(dlist, d)
	}
	sort.Strings(dlist)
	return out, dlist, nil
}

func makeBothSidesRecord(slug string, a, b *snapshotEntry, classes []DiffClass, aTs, bTs time.Time) DiffRecord {
	r := DiffRecord{EventSlug: slug, ScanATs: aTs, ScanBTs: bTs, DiffClasses: classes}
	fillFromCandidateA(&r, a.Candidate)
	fillFromCandidateB(&r, b.Candidate)
	r.RankDelta = r.RankA - r.RankB // positive = improved (lower rank = better)
	if a.Trace != nil {
		r.LabelsA = labelStrings(a.Trace.Labels)
	}
	if b.Trace != nil {
		r.LabelsB = labelStrings(b.Trace.Labels)
	}
	if a.Trace != nil && b.Trace != nil {
		r.LabelsAdded, r.LabelsRemoved = stringSetDiff(r.LabelsA, r.LabelsB)
	}
	r.RiskFlagsAdded, r.RiskFlagsRemoved = stringSetDiff(r.RiskFlagsA, r.RiskFlagsB)
	r.ReviewNote = ReviewNote(classes)
	return r
}

func makeNewRecord(slug string, b *snapshotEntry, aTs, bTs time.Time) DiffRecord {
	r := DiffRecord{
		EventSlug:   slug,
		ScanATs:     aTs,
		ScanBTs:     bTs,
		DiffClasses: []DiffClass{ClassNewCandidate},
	}
	fillFromCandidateB(&r, b.Candidate)
	if b.Trace != nil {
		r.LabelsB = labelStrings(b.Trace.Labels)
	}
	r.ReviewNote = NoteNewlyAppeared
	return r
}

func makeDroppedRecord(slug string, a *snapshotEntry, aTs, bTs time.Time) DiffRecord {
	r := DiffRecord{
		EventSlug:   slug,
		ScanATs:     aTs,
		ScanBTs:     bTs,
		DiffClasses: []DiffClass{ClassDroppedCandidate},
	}
	fillFromCandidateA(&r, a.Candidate)
	if a.Trace != nil {
		r.LabelsA = labelStrings(a.Trace.Labels)
	}
	r.ReviewNote = NoteDropped
	return r
}

func fillFromCandidateA(r *DiffRecord, c *scanner.Candidate) {
	if c == nil {
		return
	}
	r.TitleA = c.Title
	r.RankA = c.Rank
	r.ScoreA = c.Score
	r.BandA = c.Band
	r.GateA = string(c.LiquidityGate)
	r.QuoteCoverageA = c.LiquidityResult.FractionWithQuotes
	r.AvgSpreadA = c.LiquidityResult.AvgSpread
	r.MinTopDepthA = c.LiquidityResult.MinTopDepth
	r.BookBudgetExhaustedA = budgetExhaustedCount(c)
	r.RiskFlagsA = toStringSlice(c.RiskFlags)
}

func fillFromCandidateB(r *DiffRecord, c *scanner.Candidate) {
	if c == nil {
		return
	}
	r.TitleB = c.Title
	r.RankB = c.Rank
	r.ScoreB = c.Score
	r.BandB = c.Band
	r.GateB = string(c.LiquidityGate)
	r.QuoteCoverageB = c.LiquidityResult.FractionWithQuotes
	r.AvgSpreadB = c.LiquidityResult.AvgSpread
	r.MinTopDepthB = c.LiquidityResult.MinTopDepth
	r.BookBudgetExhaustedB = budgetExhaustedCount(c)
	r.RiskFlagsB = toStringSlice(c.RiskFlags)
}

// sortRecords orders the output so that: rows with both A and B
// come first (sorted by rank_b ascending; rank 0 treated as
// infinity), then NEW rows by rank_b, then DROPPED rows by
// rank_a. Within identical sort keys, sort by event_slug.
func sortRecords(rs []DiffRecord) {
	sort.SliceStable(rs, func(i, j int) bool {
		gi, gj := groupKey(rs[i]), groupKey(rs[j])
		if gi != gj {
			return gi < gj
		}
		ri, rj := effectiveRank(rs[i]), effectiveRank(rs[j])
		if ri != rj {
			return ri < rj
		}
		return rs[i].EventSlug < rs[j].EventSlug
	})
}

func groupKey(r DiffRecord) int {
	for _, c := range r.DiffClasses {
		if c == ClassDroppedCandidate {
			return 3
		}
		if c == ClassNewCandidate {
			return 2
		}
	}
	return 1
}

func effectiveRank(r DiffRecord) int {
	if r.RankB > 0 {
		return r.RankB
	}
	if r.RankA > 0 {
		return r.RankA
	}
	return 1 << 30
}

// FilterOptions controls which records are surfaced to the
// caller. Suppression by class lives here; sorting stays in
// sortRecords so the output is always deterministic.
type FilterOptions struct {
	// Limit caps the number of records returned. Zero means "no
	// cap". Applied after class filtering.
	Limit int
	// RequiredClasses limits output to records whose DiffClasses
	// contain ALL listed classes (intersection). Empty means no
	// filter.
	RequiredClasses []DiffClass
	// IncludeQuiet — when false (default), records whose only
	// class is NO_MATERIAL_CHANGE are suppressed.
	IncludeQuiet bool
	// IncludeNew, IncludeDropped, IncludeTerminal default true,
	// true, false respectively to match the design report.
	IncludeNew      bool
	IncludeDropped  bool
	IncludeTerminal bool
}

// DefaultFilterOptions returns the conservative default filter
// flags described in the design report §G.3.
func DefaultFilterOptions() FilterOptions {
	return FilterOptions{
		Limit:           100,
		IncludeQuiet:    false,
		IncludeNew:      true,
		IncludeDropped:  true,
		IncludeTerminal: false,
	}
}

// Apply returns the records subset matching the filter.
func (f FilterOptions) Apply(rs []DiffRecord) []DiffRecord {
	out := make([]DiffRecord, 0, len(rs))
	for _, r := range rs {
		if !f.passes(r) {
			continue
		}
		out = append(out, r)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out
}

func (f FilterOptions) passes(r DiffRecord) bool {
	cs := map[DiffClass]bool{}
	for _, c := range r.DiffClasses {
		cs[c] = true
	}
	if !f.IncludeQuiet {
		if len(r.DiffClasses) == 1 && r.DiffClasses[0] == ClassNoMaterialChange {
			return false
		}
	}
	if !f.IncludeNew && cs[ClassNewCandidate] {
		return false
	}
	if !f.IncludeDropped && cs[ClassDroppedCandidate] {
		return false
	}
	if !f.IncludeTerminal &&
		(cs[ClassBecameTerminal] || cs[ClassBecameClosed] || cs[ClassBecameInactive]) {
		return false
	}
	for _, want := range f.RequiredClasses {
		if !cs[want] {
			return false
		}
	}
	return true
}
