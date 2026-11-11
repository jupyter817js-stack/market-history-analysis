package engine

import (
	"strings"
	"testing"
	"time"

	"rn1-strategy-extractor/internal/scanner"
)

// mkProbeCandidate builds a scanner.Candidate with the fields the
// entry decision touches. Defaults are PROBE-eligible (score 10.5,
// PASS, 24h ahead, 5 binary markets at $20 each with good
// liquidity).
func mkProbeCandidate(slug string, mutators ...func(*scanner.Candidate)) scanner.Candidate {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	startTs := now.Add(24 * time.Hour)
	per := make([]scanner.PerMarketLiquidity, 5)
	for i := range per {
		per[i] = scanner.PerMarketLiquidity{
			ConditionID:        slug + "-cond-" + string(rune('a'+i)),
			HasTwoSidedQuotesA: true, HasTwoSidedQuotesB: true,
			SpreadA: 0.02, SpreadB: 0.02,
			TopDepthA: 500, TopDepthB: 500,
		}
	}
	c := scanner.Candidate{
		EventSlug:     slug,
		SportPrefix:   "mls",
		Score:         10.5,
		Band:          "very_strong",
		LiquidityGate: scanner.LiquidityPass,
		StartTs:       &startTs,
		BinaryCount:   5,
		MarketCount:   5,
		LiquidityResult: scanner.LiquidityResult{
			Label:             scanner.LiquidityPass,
			PerMarket:         per,
			MarketsConsidered: 5,
			MarketsWithBoth:   5,
		},
	}
	for _, m := range mutators {
		m(&c)
	}
	return c
}

// ---------------------------------------------------------------------------
// ShouldProbe
// ---------------------------------------------------------------------------

func TestShouldProbe_AcceptsCanonical(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	c := mkProbeCandidate("evt")
	ok, reason := ShouldProbe(&c, DefaultEntryWindow(), now)
	if !ok {
		t.Errorf("canonical candidate rejected: %s", reason)
	}
}

func TestShouldProbe_RejectsBelowVeryStrong(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	c := mkProbeCandidate("evt", func(c *scanner.Candidate) { c.Score = 9.5 })
	ok, _ := ShouldProbe(&c, DefaultEntryWindow(), now)
	if ok {
		t.Errorf("score 9.5 should fail very_strong floor 10.0")
	}
}

func TestShouldProbe_RejectsNonPass(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	for _, gate := range []scanner.LiquidityLabel{
		scanner.LiquidityWarn, scanner.LiquidityFail, scanner.LiquidityUnknown,
	} {
		c := mkProbeCandidate("evt", func(c *scanner.Candidate) {
			c.LiquidityGate = gate
		})
		ok, _ := ShouldProbe(&c, DefaultEntryWindow(), now)
		if ok {
			t.Errorf("gate %q should fail PROBE", gate)
		}
	}
}

func TestShouldProbe_RejectsHumanReviewRequired(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	c := mkProbeCandidate("evt", func(c *scanner.Candidate) {
		c.RiskFlags = []scanner.RiskFlag{scanner.RiskHumanReviewRequired}
	})
	ok, reason := ShouldProbe(&c, DefaultEntryWindow(), now)
	if ok {
		t.Errorf("HUMAN_REVIEW_REQUIRED should fail PROBE")
	}
	if !strings.Contains(reason, "HUMAN_REVIEW_REQUIRED") {
		t.Errorf("reason = %q; expected to name the flag", reason)
	}
}

func TestShouldProbe_RejectsOutsideWindow(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		startTs time.Time
	}{
		{"too soon (1h)", now.Add(1 * time.Hour)},
		{"too far (96h)", now.Add(96 * time.Hour)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cand := mkProbeCandidate("evt", func(cc *scanner.Candidate) {
				ts := c.startTs
				cc.StartTs = &ts
			})
			ok, _ := ShouldProbe(&cand, DefaultEntryWindow(), now)
			if ok {
				t.Errorf("%s should fail PROBE", c.name)
			}
		})
	}
}

func TestShouldProbe_AcceptsAtBoundary(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	for _, hrs := range []float64{2, 72} {
		cand := mkProbeCandidate("evt", func(c *scanner.Candidate) {
			ts := now.Add(time.Duration(hrs) * time.Hour)
			c.StartTs = &ts
		})
		ok, reason := ShouldProbe(&cand, DefaultEntryWindow(), now)
		if !ok {
			t.Errorf("boundary %fh should pass PROBE: %s", hrs, reason)
		}
	}
}

func TestShouldProbe_RejectsMissingStartTs(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	c := mkProbeCandidate("evt", func(c *scanner.Candidate) { c.StartTs = nil })
	ok, _ := ShouldProbe(&c, DefaultEntryWindow(), now)
	if ok {
		t.Errorf("missing StartTs should fail PROBE")
	}
}

func TestShouldProbe_RejectsBinaryCountBelow2(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	c := mkProbeCandidate("evt", func(c *scanner.Candidate) { c.BinaryCount = 1 })
	ok, _ := ShouldProbe(&c, DefaultEntryWindow(), now)
	if ok {
		t.Errorf("BinaryCount=1 should fail bundle invariant")
	}
}

func TestShouldProbe_NilCandidate(t *testing.T) {
	ok, _ := ShouldProbe(nil, DefaultEntryWindow(), time.Now())
	if ok {
		t.Error("nil should fail")
	}
}

// ---------------------------------------------------------------------------
// PlanEntry — open
// ---------------------------------------------------------------------------

func TestPlanEntry_OpenProducesBothSidesEveryMarket(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	c := mkProbeCandidate("evt-open")
	out := PlanEntry(EntryDecisionInput{
		Candidate:   &c,
		Limits:      DefaultSizingLimits(),
		EntryWindow: DefaultEntryWindow(),
		Now:         now,
		CycleTs:     "2026-05-16T08-00-00Z",
	})
	if out.Action != ActionOpen {
		t.Fatalf("action = %q, want open: %s", out.Action, out.Reason)
	}
	if out.NewPositionPhase != PaperOpenB {
		t.Errorf("new phase = %q, want paper_open_b", out.NewPositionPhase)
	}
	// 5 markets × 2 sides = 10 fills.
	if len(out.ProposedFills) != 10 {
		t.Fatalf("got %d fills, want 10", len(out.ProposedFills))
	}
	if err := SanityCheckFills(out.ProposedFills); err != nil {
		t.Errorf("invariant violated: %v", err)
	}
	// First A fill must be tagged paper_accumulate_a; first B
	// must be paper_open_b.
	bySide := map[string][]PaperFill{}
	for _, f := range out.ProposedFills {
		bySide[f.Side] = append(bySide[f.Side], f)
	}
	for _, fa := range bySide["a"] {
		if fa.Phase != PaperAccumulateA {
			t.Errorf("A fill phase = %q, want paper_accumulate_a", fa.Phase)
		}
	}
	for _, fb := range bySide["b"] {
		if fb.Phase != PaperOpenB {
			t.Errorf("B fill phase = %q, want paper_open_b", fb.Phase)
		}
	}
}

func TestPlanEntry_SkipsWhenProbeFails(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	c := mkProbeCandidate("evt", func(c *scanner.Candidate) { c.Score = 7 })
	out := PlanEntry(EntryDecisionInput{
		Candidate: &c, Limits: DefaultSizingLimits(),
		EntryWindow: DefaultEntryWindow(), Now: now,
	})
	if out.Action != ActionSkip {
		t.Errorf("action = %q, want skip", out.Action)
	}
	if len(out.ProposedFills) != 0 {
		t.Errorf("expected 0 fills, got %d", len(out.ProposedFills))
	}
}

func TestPlanEntry_SkipsWhenAllMarketsRejectedBySizing(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	c := mkProbeCandidate("evt", func(c *scanner.Candidate) {
		// Wide spread on every market → sizing rejects all.
		for i := range c.LiquidityResult.PerMarket {
			c.LiquidityResult.PerMarket[i].SpreadA = 0.15
		}
	})
	out := PlanEntry(EntryDecisionInput{
		Candidate: &c, Limits: DefaultSizingLimits(),
		EntryWindow: DefaultEntryWindow(), Now: now,
	})
	if out.Action != ActionSkip {
		t.Errorf("action = %q, want skip", out.Action)
	}
	if !strings.Contains(out.Reason, "rejected by sizing") {
		t.Errorf("reason = %q; expected 'rejected by sizing'", out.Reason)
	}
}

// ---------------------------------------------------------------------------
// PlanEntry — expand
// ---------------------------------------------------------------------------

func TestPlanEntry_ExpandPicksNewMarketsOnly(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	c := mkProbeCandidate("evt-x")
	// Pretend we already hold the first 3 of the 5 markets.
	pos := &PaperPosition{
		EventSlug:    "evt-x",
		CurrentPhase: PaperOpenB,
		Legs: map[string]*PaperLeg{
			c.LiquidityResult.PerMarket[0].ConditionID: {ConditionID: c.LiquidityResult.PerMarket[0].ConditionID},
			c.LiquidityResult.PerMarket[1].ConditionID: {ConditionID: c.LiquidityResult.PerMarket[1].ConditionID},
			c.LiquidityResult.PerMarket[2].ConditionID: {ConditionID: c.LiquidityResult.PerMarket[2].ConditionID},
		},
	}
	out := PlanEntry(EntryDecisionInput{
		Candidate: &c, ExistingPosition: pos,
		Limits: DefaultSizingLimits(), EntryWindow: DefaultEntryWindow(), Now: now,
	})
	if out.Action != ActionExpand {
		t.Fatalf("action = %q, want expand: %s", out.Action, out.Reason)
	}
	// 2 new markets × 2 sides = 4 fills.
	if len(out.ProposedFills) != 4 {
		t.Errorf("got %d fills, want 4 (2 new markets × 2 sides)", len(out.ProposedFills))
	}
	for _, f := range out.ProposedFills {
		if f.Phase != PaperExpand {
			t.Errorf("expand fill phase = %q, want paper_expand", f.Phase)
		}
	}
	if out.NewPositionPhase != PaperExpand {
		t.Errorf("new phase = %q, want paper_expand", out.NewPositionPhase)
	}
}

func TestPlanEntry_SkipsWhenNoNewMarketsAndNoLayerLogic(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	c := mkProbeCandidate("evt-x")
	// Already hold every market.
	legs := map[string]*PaperLeg{}
	for _, pm := range c.LiquidityResult.PerMarket {
		legs[pm.ConditionID] = &PaperLeg{ConditionID: pm.ConditionID}
	}
	pos := &PaperPosition{EventSlug: "evt-x", CurrentPhase: PaperOpenB, Legs: legs}

	out := PlanEntry(EntryDecisionInput{
		Candidate: &c, ExistingPosition: pos,
		Limits: DefaultSizingLimits(), EntryWindow: DefaultEntryWindow(), Now: now,
	})
	if out.Action != ActionSkip {
		t.Errorf("action = %q, want skip (no new markets)", out.Action)
	}
	if !strings.Contains(out.Reason, "LAYER decision deferred") {
		t.Errorf("reason = %q; expected mention of LAYER deferral", out.Reason)
	}
}

func TestPlanEntry_StoppedPositionRefusesAllEntries(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	c := mkProbeCandidate("evt-x")
	pos := &PaperPosition{EventSlug: "evt-x", CurrentPhase: PaperStopped}
	out := PlanEntry(EntryDecisionInput{
		Candidate: &c, ExistingPosition: pos,
		Limits: DefaultSizingLimits(), EntryWindow: DefaultEntryWindow(), Now: now,
	})
	if out.Action != ActionSkip {
		t.Errorf("action = %q, want skip", out.Action)
	}
	if out.NewPositionPhase != PaperStopped {
		t.Errorf("new phase = %q, should stay paper_stopped", out.NewPositionPhase)
	}
}

func TestPlanEntry_IdlePositionRefusesAllEntries(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	c := mkProbeCandidate("evt-x")
	pos := &PaperPosition{EventSlug: "evt-x", CurrentPhase: PaperIdle}
	out := PlanEntry(EntryDecisionInput{
		Candidate: &c, ExistingPosition: pos,
		Limits: DefaultSizingLimits(), EntryWindow: DefaultEntryWindow(), Now: now,
	})
	if out.Action != ActionSkip {
		t.Errorf("action = %q, want skip", out.Action)
	}
}

func TestPlanEntry_NilCandidate(t *testing.T) {
	out := PlanEntry(EntryDecisionInput{})
	if out.Action != ActionSkip {
		t.Errorf("nil candidate should skip")
	}
}

// ---------------------------------------------------------------------------
// SanityCheckFills
// ---------------------------------------------------------------------------

func TestSanityCheckFills_AcceptsEvenSides(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	fills := []PaperFill{
		mkFill("evt", "0xc1", "a", now, 10, 0.5, 5.0, 0.01, PaperAccumulateA),
		mkFill("evt", "0xc1", "b", now, 10, 0.5, 5.0, 0.01, PaperOpenB),
	}
	if err := SanityCheckFills(fills); err != nil {
		t.Errorf("valid fills rejected: %v", err)
	}
}

func TestSanityCheckFills_RejectsOneSidedMarket(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	fills := []PaperFill{
		mkFill("evt", "0xc1", "a", now, 10, 0.5, 5.0, 0.01, PaperAccumulateA),
		// no b fill → both-sides invariant violation
	}
	err := SanityCheckFills(fills)
	if err == nil {
		t.Error("expected error for one-sided market")
	}
}

func TestSanityCheckFills_RejectsZeroShares(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	fills := []PaperFill{
		mkFill("evt", "0xc", "a", now, 0, 0.5, 0, 0, PaperAccumulateA),
		mkFill("evt", "0xc", "b", now, 0, 0.5, 0, 0, PaperOpenB),
	}
	err := SanityCheckFills(fills)
	if err == nil {
		t.Error("expected error for zero shares")
	}
}

func TestSanityCheckFills_RejectsInvalidPhase(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	fills := []PaperFill{
		mkFill("evt", "0xc", "a", now, 10, 0.5, 5.0, 0.01, "BUY"),
		mkFill("evt", "0xc", "b", now, 10, 0.5, 5.0, 0.01, "SELL"),
	}
	err := SanityCheckFills(fills)
	if err == nil {
		t.Error("expected error for invalid phase")
	}
}

func TestSanityCheckFills_AcceptsEmpty(t *testing.T) {
	if err := SanityCheckFills(nil); err != nil {
		t.Errorf("empty fills should be accepted, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SummarizeEntryAction
// ---------------------------------------------------------------------------

func TestSummarizeEntryAction_FormatsCleanly(t *testing.T) {
	out := EntryDecisionOutput{
		EventSlug: "evt-x", Action: ActionOpen,
		Reason: "open: PROBE passed, 5 markets, paper_total=50.0",
	}
	s := SummarizeEntryAction(out)
	if !strings.Contains(s, "[OPEN]") || !strings.Contains(s, "evt-x") {
		t.Errorf("summary = %q; missing key fields", s)
	}
}
