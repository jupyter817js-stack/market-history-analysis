package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// AppendMerge writes one PaperMerge row to the merges JSONL
// ledger. Append-only.
func AppendMerge(ledgerPath string, m PaperMerge) error {
	if ledgerPath == "" {
		return fmt.Errorf("ledger path required")
	}
	if m.EventSlug == "" || m.ConditionID == "" {
		return fmt.Errorf("merge requires event_slug + condition_id")
	}
	if m.ObservedAt.IsZero() {
		return fmt.Errorf("merge requires observed_at")
	}
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal merge: %w", err)
	}
	f, err := os.OpenFile(ledgerPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open ledger %q: %w", ledgerPath, err)
	}
	defer f.Close()
	if _, err := f.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("append merge: %w", err)
	}
	return nil
}

// ReadMerges loads every PaperMerge from the JSONL ledger.
func ReadMerges(ledgerPath string) ([]PaperMerge, error) {
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
	var out []PaperMerge
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m PaperMerge
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return nil, fmt.Errorf("decode merge line: %w", err)
		}
		out = append(out, m)
	}
	return out, nil
}

// AppendRedemption writes one PaperRedemption row to the
// redemptions JSONL ledger.
func AppendRedemption(ledgerPath string, r PaperRedemption) error {
	if ledgerPath == "" {
		return fmt.Errorf("ledger path required")
	}
	if r.EventSlug == "" || r.ConditionID == "" {
		return fmt.Errorf("redemption requires event_slug + condition_id")
	}
	if r.ObservedAt.IsZero() {
		return fmt.Errorf("redemption requires observed_at")
	}
	body, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal redemption: %w", err)
	}
	f, err := os.OpenFile(ledgerPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open ledger %q: %w", ledgerPath, err)
	}
	defer f.Close()
	if _, err := f.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("append redemption: %w", err)
	}
	return nil
}

// ReadRedemptions loads every PaperRedemption from the JSONL ledger.
func ReadRedemptions(ledgerPath string) ([]PaperRedemption, error) {
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
	var out []PaperRedemption
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r PaperRedemption
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("decode redemption line: %w", err)
		}
		out = append(out, r)
	}
	return out, nil
}

// PaperPnlSummary is the engine-wide aggregate written to
// .paper/pnl_summary.json on each rebuild. Pure function of the
// fills + merges + redemptions ledgers.
type PaperPnlSummary struct {
	GeneratedAt              time.Time          `json:"generated_at"`
	LifetimeFillsCount       int                `json:"lifetime_fills_count"`
	LifetimeMergesCount      int                `json:"lifetime_merges_count"`
	LifetimeRedemptionsCount int                `json:"lifetime_redemptions_count"`
	LifetimeEventsTouched    int                `json:"lifetime_events_touched"`

	PaperTotalCostUsdc       float64            `json:"paper_total_cost_usdc"`
	PaperRealizedUsdc        float64            `json:"paper_realized_usdc"`
	PaperMergePnlUsdc        float64            `json:"paper_merge_pnl_usdc"`
	PaperRedeemPnlUsdc       float64            `json:"paper_redeem_pnl_usdc"`
	PaperNetPnlUsdc          float64            `json:"paper_net_pnl_usdc"`
	PaperReturnPct           float64            `json:"paper_return_pct"`

	OpenPositionsCount       int                `json:"open_positions_count"`
	StoppedPositionsCount    int                `json:"stopped_positions_count"`
	IdlePositionsCount       int                `json:"idle_positions_count"`

	PaperPnlBySportPrefix    map[string]float64 `json:"paper_pnl_by_sport_prefix,omitempty"`
}

// BuildPaperPnlSummary aggregates the three ledgers + the per-
// position phase distribution into one summary. Pure function:
// same inputs always yield the same output.
//
// positions can be nil; in that case the phase counts are zero
// and PnL-by-sport-prefix is empty.
func BuildPaperPnlSummary(
	fills []PaperFill,
	merges []PaperMerge,
	redemptions []PaperRedemption,
	positions map[string]*PaperPosition,
	now time.Time,
) PaperPnlSummary {
	s := PaperPnlSummary{
		GeneratedAt:              now,
		LifetimeFillsCount:       len(fills),
		LifetimeMergesCount:      len(merges),
		LifetimeRedemptionsCount: len(redemptions),
		PaperPnlBySportPrefix:    map[string]float64{},
	}

	// Distinct events touched.
	eventSet := map[string]bool{}
	for _, f := range fills {
		eventSet[f.EventSlug] = true
	}
	s.LifetimeEventsTouched = len(eventSet)

	// Total cost = sum of fill costs.
	for _, f := range fills {
		s.PaperTotalCostUsdc += f.PaperCostUsdc
	}
	// Realized = merge realized + redemption realized.
	for _, m := range merges {
		s.PaperMergePnlUsdc += m.PaperMergePnlUsdc
		s.PaperRealizedUsdc += m.PaperMergedRealizedUsdc
	}
	for _, r := range redemptions {
		// UNKNOWN-side redemptions are pending resolution (see
		// applyRealizedPnl in cycle.go); their -cost row is
		// recorded but does NOT contribute to summary PnL until
		// the winning side is confirmed.
		if r.WinningSide == "unknown" || r.WinningSide == "" {
			continue
		}
		s.PaperRedeemPnlUsdc += r.PaperRedeemPnlUsdc
		s.PaperRealizedUsdc += r.PaperRedeemedRealizedUsdc
	}
	s.PaperNetPnlUsdc = s.PaperMergePnlUsdc + s.PaperRedeemPnlUsdc
	if s.PaperTotalCostUsdc > 0 {
		s.PaperReturnPct = 100.0 * s.PaperNetPnlUsdc / s.PaperTotalCostUsdc
	}

	// Position phase counts + per-prefix PnL.
	for _, p := range positions {
		if p == nil {
			continue
		}
		switch p.CurrentPhase {
		case PaperStopped:
			s.StoppedPositionsCount++
		case PaperIdle:
			s.IdlePositionsCount++
		default:
			s.OpenPositionsCount++
		}
		if p.SportPrefix != "" {
			s.PaperPnlBySportPrefix[p.SportPrefix] += p.PaperRealizedPnlUsdc
		}
	}
	return s
}

// WritePnlSummary writes (overwrites) the JSON summary at the
// given path. Unlike the other ledgers this file is NOT
// append-only -- it is a derived snapshot of the underlying
// append-only ledgers and can be safely regenerated each cycle.
func WritePnlSummary(path string, s PaperPnlSummary) error {
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal summary: %w", err)
	}
	return os.WriteFile(path, body, 0644)
}
