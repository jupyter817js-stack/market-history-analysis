package validation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rn1-strategy-extractor/internal/scanner"
)

// DefaultMinLikenessForTracking is the threshold below which the
// envelope tracker does NOT record per-condition ticks. The
// likeness score itself is recorded for every candidate.
const DefaultMinLikenessForTracking = 0.60

// ProcessCycleInput configures one ProcessCycle call.
type ProcessCycleInput struct {
	HistoricalProfile      *HistoricalProfile
	ScanCandidatesPath     string  // .dryrun/server/<ts>/candidates.json
	CycleTs                string  // matches the cycle directory name
	LikenessLedgerPath     string  // .validation/likeness_per_candidate.jsonl
	EnvelopeLedgerPath     string  // .validation/envelope_tracking.jsonl
	SettlementLedgerPath   string  // .validation/settlements.jsonl
	MinLikenessForTracking float64 // 0 → use Default
	Now                    time.Time
}

// ProcessCycleResult is the per-call summary returned by
// ProcessCycle. Counts mirror what the CLI prints.
type ProcessCycleResult struct {
	CycleTs               string `json:"cycle_ts"`
	ScanCandidates        int    `json:"scan_candidates"`
	LikenessRecorded      int    `json:"likeness_recorded"`
	PrimeCount            int    `json:"prime_count"`
	PlausibleCount        int    `json:"plausible_count"`
	AdjacentCount         int    `json:"adjacent_count"`
	NonRn1Count           int    `json:"non_rn1_count"`
	EnvelopeTicksAppended int    `json:"envelope_ticks_appended"`
	SettlementsDetected   int    `json:"settlements_detected"`
}

// ProcessCycle is the orchestration entrypoint called once per
// 6-hour cycle. It:
//
//  1. Loads candidates.json from the cycle directory.
//  2. Scores each candidate with the likeness scorer.
//  3. Appends a LikenessRecord for every candidate.
//  4. For candidates whose likeness >= MinLikenessForTracking,
//     appends one TickObservation per binary market.
//  5. Detects settlements: events that were previously tracked
//     (have envelope ticks in the ledger) AND are no longer in
//     the current scan AND whose EventStartTs has passed.
//     Emits one SettlementOutcome per detected event with
//     winning_side = "UNKNOWN" (a future resolve-settlements
//     command updates these once Gamma confirms the outcome).
//
// All ledger writes are append-only. ProcessCycle never modifies
// or deletes prior records. Caller is responsible for ensuring
// the parent directories of all ledger paths exist.
func ProcessCycle(in ProcessCycleInput) (ProcessCycleResult, error) {
	var res ProcessCycleResult

	if in.HistoricalProfile == nil {
		return res, fmt.Errorf("historical profile required")
	}
	if in.ScanCandidatesPath == "" {
		return res, fmt.Errorf("scan candidates path required")
	}
	if in.LikenessLedgerPath == "" || in.EnvelopeLedgerPath == "" || in.SettlementLedgerPath == "" {
		return res, fmt.Errorf("all three ledger paths required")
	}
	if in.MinLikenessForTracking <= 0 {
		in.MinLikenessForTracking = DefaultMinLikenessForTracking
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	res.CycleTs = in.CycleTs

	// 1. Load candidates.
	cands, err := readCandidates(in.ScanCandidatesPath)
	if err != nil {
		return res, fmt.Errorf("read candidates: %w", err)
	}
	res.ScanCandidates = len(cands)

	// Build a set of slugs in the current scan for settlement
	// detection later.
	currentSlugs := map[string]bool{}
	for _, c := range cands {
		currentSlugs[c.EventSlug] = true
	}

	// 2-3-4. Score + record + tick.
	for _, c := range cands {
		in2 := LikenessInputFromCandidate(&c)
		score := ScoreLikeness(in2, in.HistoricalProfile)

		rec := LikenessRecord{
			ObservedAt:  now,
			CycleTs:     in.CycleTs,
			EventSlug:   c.EventSlug,
			SportPrefix: c.SportPrefix,
			Score:       score,
		}
		if err := AppendLikeness(in.LikenessLedgerPath, rec); err != nil {
			return res, fmt.Errorf("append likeness for %q: %w", c.EventSlug, err)
		}
		res.LikenessRecorded++

		switch score.Band {
		case BandRn1Prime:
			res.PrimeCount++
		case BandRn1Plausible:
			res.PlausibleCount++
		case BandRn1Adjacent:
			res.AdjacentCount++
		default:
			res.NonRn1Count++
		}

		if score.Total < in.MinLikenessForTracking {
			continue
		}

		// Append one TickObservation per binary market in this
		// candidate. Skip markets without observed quotes (the
		// envelope tracker needs both ask values to compute
		// SumWavg).
		for _, pm := range c.LiquidityResult.PerMarket {
			ask := bestAskFor(pm)
			if ask.aSide == 0 || ask.bSide == 0 {
				continue
			}
			tick := TickObservation{
				ObservedAt:   now,
				CycleTs:      in.CycleTs,
				EventSlug:    c.EventSlug,
				ConditionID:  pm.ConditionID,
				EventStartTs: c.StartTs,
				AskA:         ask.aSide,
				AskB:         ask.bSide,
				BidA:         ask.aBid,
				BidB:         ask.bBid,
				SpreadA:      pm.SpreadA,
				SpreadB:      pm.SpreadB,
				TopDepthA:    pm.TopDepthA,
				TopDepthB:    pm.TopDepthB,
				Likeness:     score.Total,
				Band:         score.Band,
			}
			if err := AppendTick(in.EnvelopeLedgerPath, tick); err != nil {
				return res, fmt.Errorf("append tick for %q/%q: %w", c.EventSlug, pm.ConditionID, err)
			}
			res.EnvelopeTicksAppended++
		}
	}

	// 5. Settlement detection.
	settled, err := detectAndEmitSettlements(in, currentSlugs, now)
	if err != nil {
		return res, fmt.Errorf("settlement detection: %w", err)
	}
	res.SettlementsDetected = settled

	return res, nil
}

// bestAskWrap bundles the four price points we need from a
// PerMarketLiquidity row. Zero values indicate "no observable
// quote"; the caller skips those rows.
type bestAskWrap struct {
	aSide, aBid float64
	bSide, bBid float64
}

func bestAskFor(pm scanner.PerMarketLiquidity) bestAskWrap {
	if !pm.HasTwoSidedQuotesA || !pm.HasTwoSidedQuotesB {
		return bestAskWrap{}
	}
	// We need ask prices. SpreadA = ask - bid; the scanner stores
	// spread + top depth + top-of-book flags, not the raw best ask.
	// For Phase F-0 we use a conservative approximation:
	//   ask_estimate = 0.5 + spread / 2
	//   bid_estimate = 0.5 - spread / 2
	// This is intentionally imprecise; the hypothetical model is
	// only meaningful in aggregate (criteria 1-3 over hundreds of
	// envelopes), and this approximation preserves the right
	// ordinal SumWavg behaviour.
	//
	// A more accurate model would pull raw best_ask values, which
	// requires an additional scan field. Phase F-0 starts with
	// this approximation and the operator can compare it against
	// settlement outcomes; deviations inform a future schema add.
	askA := 0.5 + pm.SpreadA/2.0
	bidA := 0.5 - pm.SpreadA/2.0
	askB := 0.5 + pm.SpreadB/2.0
	bidB := 0.5 - pm.SpreadB/2.0
	return bestAskWrap{aSide: askA, aBid: bidA, bSide: askB, bBid: bidB}
}

// detectAndEmitSettlements scans the envelope ledger for events
// no longer in the current candidate set whose last-observed
// StartTs has passed, and emits a SettlementOutcome (winning side
// "UNKNOWN") for each. Idempotent: an event already settled is
// not settled again unless it reappears (which it shouldn't).
func detectAndEmitSettlements(in ProcessCycleInput, currentSlugs map[string]bool, now time.Time) (int, error) {
	ticks, err := ReadTicks(in.EnvelopeLedgerPath)
	if err != nil {
		return 0, fmt.Errorf("read envelope ticks: %w", err)
	}
	if len(ticks) == 0 {
		return 0, nil
	}

	prior, err := ReadSettlements(in.SettlementLedgerPath)
	if err != nil {
		return 0, fmt.Errorf("read prior settlements: %w", err)
	}
	alreadySettled := map[string]bool{}
	for _, s := range prior {
		alreadySettled[s.EventSlug] = true
	}

	// Group ticks by event_slug to find each event's last-known
	// EventStartTs.
	type eventInfo struct {
		lastTick     TickObservation
		startTs      *time.Time
	}
	bySlug := map[string]*eventInfo{}
	for _, t := range ticks {
		info, ok := bySlug[t.EventSlug]
		if !ok {
			info = &eventInfo{}
			bySlug[t.EventSlug] = info
		}
		if t.ObservedAt.After(info.lastTick.ObservedAt) {
			info.lastTick = t
			if t.EventStartTs != nil {
				ts := *t.EventStartTs
				info.startTs = &ts
			}
		}
	}

	// For each tracked slug not in current scan AND whose StartTs
	// has passed AND not already settled, emit a SettlementOutcome.
	var settledNow []string
	for slug, info := range bySlug {
		if currentSlugs[slug] {
			continue
		}
		if alreadySettled[slug] {
			continue
		}
		if info.startTs == nil || info.startTs.After(now) {
			continue
		}
		settledNow = append(settledNow, slug)
	}
	if len(settledNow) == 0 {
		return 0, nil
	}
	sort.Strings(settledNow)

	// Build per-condition resolutions from the envelope ticks for
	// each settled event.
	legs := BuildLegStates(ticks)
	for _, slug := range settledNow {
		// Collect every leg key for this event and its ticks.
		var perCond []ConditionResolution
		for key, leg := range legs {
			if leg.EventSlug != slug {
				continue
			}
			// Pull the ticks for this specific (slug, cond).
			subTicks := filterTicksByKey(ticks, key)
			res := ComputeHypoPnL(leg, subTicks, "UNKNOWN")
			perCond = append(perCond, res)
		}
		// Resolved time: the moment we detected settlement.
		out := BuildSettlementOutcome(slug, now, nil, perCond)
		if err := AppendSettlement(in.SettlementLedgerPath, out); err != nil {
			return 0, fmt.Errorf("append settlement for %q: %w", slug, err)
		}
	}
	return len(settledNow), nil
}

func filterTicksByKey(all []TickObservation, key string) []TickObservation {
	var out []TickObservation
	for _, t := range all {
		if t.EventSlug+"|"+t.ConditionID == key {
			out = append(out, t)
		}
	}
	return out
}

// readCandidates loads a scanner candidates.json into a slice.
// Accepts UTF-8 with optional BOM.
func readCandidates(path string) ([]scanner.Candidate, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	if len(body) >= 3 && body[0] == 0xEF && body[1] == 0xBB && body[2] == 0xBF {
		body = body[3:]
	}
	if len(body) == 0 {
		return nil, nil
	}
	var cands []scanner.Candidate
	if err := json.Unmarshal(body, &cands); err != nil {
		return nil, fmt.Errorf("decode %q: %w", path, err)
	}
	return cands, nil
}

// EnsureValidationDir is a convenience helper that creates the
// parent directories of all three ledger paths in one call.
func EnsureValidationDir(in ProcessCycleInput) error {
	for _, p := range []string{
		in.LikenessLedgerPath, in.EnvelopeLedgerPath, in.SettlementLedgerPath,
	} {
		if p == "" {
			continue
		}
		dir := filepath.Dir(p)
		if dir == "" || dir == "." {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("mkdir %q: %w", dir, err)
		}
	}
	return nil
}

// likeness_ledger helpers --------------------------------

// AppendLikeness appends one LikenessRecord row to the per-candidate
// likeness ledger.
func AppendLikeness(ledgerPath string, rec LikenessRecord) error {
	if ledgerPath == "" {
		return fmt.Errorf("ledger path required")
	}
	if rec.EventSlug == "" {
		return fmt.Errorf("likeness record requires event_slug")
	}
	if rec.ObservedAt.IsZero() {
		return fmt.Errorf("likeness record requires observed_at")
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal likeness: %w", err)
	}
	f, err := os.OpenFile(ledgerPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open ledger %q: %w", ledgerPath, err)
	}
	defer f.Close()
	if _, err := f.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("append likeness: %w", err)
	}
	return nil
}

// ReadLikenesses loads every LikenessRecord from the ledger.
func ReadLikenesses(ledgerPath string) ([]LikenessRecord, error) {
	body, err := os.ReadFile(ledgerPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read ledger %q: %w", ledgerPath, err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	if len(body) >= 3 && body[0] == 0xEF && body[1] == 0xBB && body[2] == 0xBF {
		body = body[3:]
	}
	var out []LikenessRecord
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r LikenessRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("decode likeness line: %w", err)
		}
		out = append(out, r)
	}
	return out, nil
}
