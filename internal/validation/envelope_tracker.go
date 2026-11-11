package validation

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MaxAcceptableTickLag is the tolerance for the monotonicity
// guard. Ticks whose ObservedAt precedes the prior last tick by
// LESS than this duration are accepted (covers two cycles racing
// to append into the same ledger, where the slower one's "now"
// can lag the faster one's by tens of seconds). Lag beyond this
// suggests real clock drift (operator clock reset, NTP jump),
// which is rejected.
const MaxAcceptableTickLag = 10 * time.Minute

// AppendTick appends a single TickObservation to the envelope
// tracking ledger as a JSONL row. The ledger is append-only:
// existing rows are never modified. The function creates the file
// if it does not exist.
//
// Chronology guard (relaxed 2026-05-16): a tick whose ObservedAt
// is MORE than MaxAcceptableTickLag before the prior last row is
// rejected as likely clock drift. Smaller lags are accepted; the
// downstream BuildLegStates sorts ticks chronologically anyway,
// so out-of-order arrival is benign as long as the lag is bounded.
// Without this relaxation, two cycles running concurrently (e.g.
// a manual cycle started right before the scheduled task fires)
// would crash each other; in practice the production 2026-05-16
// race produced a 43-second lag that the strict guard rejected.
//
// Caller is responsible for ensuring the parent directory exists.
func AppendTick(ledgerPath string, tick TickObservation) error {
	if ledgerPath == "" {
		return fmt.Errorf("ledger path required")
	}
	if tick.EventSlug == "" || tick.ConditionID == "" {
		return fmt.Errorf("tick requires event_slug and condition_id")
	}
	if tick.ObservedAt.IsZero() {
		return fmt.Errorf("tick requires observed_at")
	}

	// Relaxed monotonicity guard.
	if last, ok, err := lastTickTime(ledgerPath); err != nil {
		return err
	} else if ok && tick.ObservedAt.Before(last) {
		lag := last.Sub(tick.ObservedAt)
		if lag > MaxAcceptableTickLag {
			return fmt.Errorf(
				"tick observed_at %s precedes prior last tick %s by %v (> %v, clock drift?)",
				tick.ObservedAt.Format(time.RFC3339), last.Format(time.RFC3339),
				lag, MaxAcceptableTickLag)
		}
		// Within tolerance — accept silently. BuildLegStates will
		// sort by ObservedAt when consuming the ledger.
	}

	body, err := json.Marshal(tick)
	if err != nil {
		return fmt.Errorf("marshal tick: %w", err)
	}

	f, err := os.OpenFile(ledgerPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open ledger %q: %w", ledgerPath, err)
	}
	defer f.Close()
	if _, err := f.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("append tick: %w", err)
	}
	return nil
}

// lastTickTime returns the ObservedAt of the last TickObservation
// in the ledger, or (zero, false, nil) if the file is empty or
// missing.
func lastTickTime(ledgerPath string) (time.Time, bool, error) {
	st, err := os.Stat(ledgerPath)
	if os.IsNotExist(err) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("stat %q: %w", ledgerPath, err)
	}
	if st.Size() == 0 {
		return time.Time{}, false, nil
	}
	// Stream-read; cheap even for large ledgers because we keep
	// only the last decoded row.
	f, err := os.Open(ledgerPath)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("open %q: %w", ledgerPath, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	var last time.Time
	have := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var t TickObservation
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			return time.Time{}, false, fmt.Errorf("decode tick: %w", err)
		}
		last = t.ObservedAt
		have = true
	}
	if err := scanner.Err(); err != nil {
		return time.Time{}, false, fmt.Errorf("scan %q: %w", ledgerPath, err)
	}
	return last, have, nil
}

// ReadTicks loads every TickObservation from the ledger in append
// order. Returns an empty slice for a missing or empty file.
func ReadTicks(ledgerPath string) ([]TickObservation, error) {
	if ledgerPath == "" {
		return nil, fmt.Errorf("ledger path required")
	}
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
	// Strip a UTF-8 BOM if a PowerShell-written file has one.
	if len(body) >= 3 && body[0] == 0xEF && body[1] == 0xBB && body[2] == 0xBF {
		body = body[3:]
	}
	var out []TickObservation
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var t TickObservation
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			return nil, fmt.Errorf("decode tick line %q: %w", line, err)
		}
		out = append(out, t)
	}
	return out, nil
}

// BuildLegStates groups ticks by (event_slug, condition_id) and
// computes the HypotheticalLeg running aggregates for each. The
// map key is the composite slug + "|" + condition_id. Within each
// leg, ticks are sorted by ObservedAt to ensure the running
// aggregates are computed in temporal order even if the ledger had
// minor out-of-order rows from concurrent cycle writes.
//
// The "1 unit per tick" model: each tick adds 1 hypothetical share
// to both A and B at the observed best_ask prices. HypoWavgA =
// CumCostA / TickCount. HypoSumWavg = HypoWavgA + HypoWavgB.
func BuildLegStates(ticks []TickObservation) map[string]*HypotheticalLeg {
	groups := map[string][]TickObservation{}
	for _, t := range ticks {
		key := t.EventSlug + "|" + t.ConditionID
		groups[key] = append(groups[key], t)
	}

	out := make(map[string]*HypotheticalLeg, len(groups))
	for key, ts := range groups {
		sort.SliceStable(ts, func(i, j int) bool {
			return ts[i].ObservedAt.Before(ts[j].ObservedAt)
		})
		leg := &HypotheticalLeg{
			EventSlug:       ts[0].EventSlug,
			ConditionID:     ts[0].ConditionID,
			FirstObservedAt: ts[0].ObservedAt,
			MinHypoSumWavg:  2.0, // bounded above by askA+askB <= 2
		}
		for _, t := range ts {
			leg.TickCount++
			leg.HypoSharesA = float64(leg.TickCount) // 1 per tick by definition
			leg.HypoSharesB = float64(leg.TickCount)
			leg.HypoCumCostA += t.AskA
			leg.HypoCumCostB += t.AskB
			leg.HypoWavgA = leg.HypoCumCostA / float64(leg.TickCount)
			leg.HypoWavgB = leg.HypoCumCostB / float64(leg.TickCount)
			leg.HypoSumWavg = leg.HypoWavgA + leg.HypoWavgB
			leg.LastObservedAt = t.ObservedAt
			if leg.HypoSumWavg < leg.MinHypoSumWavg {
				leg.MinHypoSumWavg = leg.HypoSumWavg
				leg.MinObservedAt = t.ObservedAt
			}
			if leg.HypoSumWavg < 1.0 {
				leg.TicksBelow1_0++
			}
			if leg.HypoSumWavg < 0.95 {
				leg.TicksBelow0_95++
			}
		}
		out[key] = leg
	}
	return out
}

// FinalizeEnvelopeWindow converts a HypotheticalLeg into the
// immutable EnvelopeWindow record that the evaluation rubric
// consumes. closure must be one of "RESOLVED", "TIMEOUT", or
// "DELISTED".
//
// HoursBelowX is computed as the time span from the FIRST tick
// where the running SumWavg first dropped below X to the LAST
// tick at which it was still below X. This is approximate but
// deterministic given the same tick sequence; the rubric uses it
// only for ranking, not for hypothetical fill timing.
func FinalizeEnvelopeWindow(leg *HypotheticalLeg, ticks []TickObservation, closure string) EnvelopeWindow {
	if leg == nil {
		return EnvelopeWindow{WindowClosure: closure}
	}

	band := classifyBand(0) // default
	likeness := 0.0
	// Grab the latest tick's likeness as representative of the
	// "high-likeness as of close" state.
	if len(ticks) > 0 {
		latest := ticks[len(ticks)-1]
		likeness = latest.Likeness
		band = latest.Band
	}

	w := EnvelopeWindow{
		EventSlug:        leg.EventSlug,
		ConditionID:      leg.ConditionID,
		Likeness:         likeness,
		Band:             band,
		FirstObservedAt:  leg.FirstObservedAt,
		LastObservedAt:   leg.LastObservedAt,
		TickCount:        leg.TickCount,
		MinHypoSumWavg:   leg.MinHypoSumWavg,
		MinHypoSumWavgAt: leg.MinObservedAt,
		WindowClosure:    closure,
	}
	// MinHypoSumWavg is initialized to 2.0; if no tick was ever
	// observed, blank it.
	if leg.TickCount == 0 {
		w.MinHypoSumWavg = 0
	}

	w.HoursBelow1_0 = hoursBelowThreshold(ticks, 1.0)
	w.HoursBelow0_95 = hoursBelowThreshold(ticks, 0.95)
	return w
}

// hoursBelowThreshold computes (last - first) of the tick-times
// during which the running SumWavg was below threshold. Returns
// 0 if no tick ever crossed below.
//
// We recompute running SumWavg here rather than relying on
// per-tick state, because the input is the raw tick slice and we
// need the running value at each tick.
func hoursBelowThreshold(ticks []TickObservation, threshold float64) float64 {
	if len(ticks) == 0 {
		return 0
	}
	sorted := append([]TickObservation(nil), ticks...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].ObservedAt.Before(sorted[j].ObservedAt)
	})
	var (
		cumA, cumB    float64
		first, last   time.Time
		anyBelow      bool
	)
	for i, t := range sorted {
		cumA += t.AskA
		cumB += t.AskB
		n := float64(i + 1)
		sumWavg := (cumA / n) + (cumB / n)
		if sumWavg < threshold {
			if !anyBelow {
				first = t.ObservedAt
				anyBelow = true
			}
			last = t.ObservedAt
		}
	}
	if !anyBelow {
		return 0
	}
	return last.Sub(first).Hours()
}

// EnsureLedgerDir creates the ledger's parent directory if it does
// not exist. Helper for CLI callers; not required by the core
// AppendTick path (which leaves directory creation to the caller).
func EnsureLedgerDir(ledgerPath string) error {
	dir := filepath.Dir(ledgerPath)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0755)
}
