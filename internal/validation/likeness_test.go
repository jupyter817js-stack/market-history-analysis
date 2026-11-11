package validation

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

// loadFixtureProfile is the shared profile loader used by every
// likeness regression test. Returns the canonical fixture.
func loadFixtureProfile(t *testing.T) *HistoricalProfile {
	t.Helper()
	root := findRepoRoot(t)
	p, err := LoadProfile(filepath.Join(root, "testdata", "fixtures", "sample_historical_profile.json"))
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	return p
}

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// ---------------------------------------------------------------------------
// Per-axis component unit tests
// ---------------------------------------------------------------------------

func TestLikenessAxis_SportPrefix(t *testing.T) {
	p := loadFixtureProfile(t)
	cases := []struct {
		prefix string
		want   float64
	}{
		{"nba", 1.0},
		{"atp", 1.0},
		{"j2100", 1.0},
		{"", 0.0},
		{"xxx-not-a-prefix", 0.0},
	}
	for _, c := range cases {
		got := scoreSportPrefix(c.prefix, p)
		if got != c.want {
			t.Errorf("scoreSportPrefix(%q) = %.2f, want %.2f", c.prefix, got, c.want)
		}
	}
}

func TestLikenessAxis_Family(t *testing.T) {
	cases := []struct {
		families []string
		want     float64
	}{
		{nil, 0.0},
		{[]string{}, 0.0},
		{[]string{"soccer_3way_moneyline"}, 1.0},
		{[]string{"spread_ml_total_stack"}, 1.0},
		{[]string{"bo3_esports_stack"}, 1.0},
		{[]string{"tennis_set_total_stack"}, 1.0},
		{[]string{"expanded_companion_stack"}, 1.0},
		{[]string{"random_unknown_family"}, 0.0},
		{[]string{"random_unknown_family", "expanded_companion_stack"}, 1.0},
	}
	for _, c := range cases {
		got := scoreFamily(c.families)
		if got != c.want {
			t.Errorf("scoreFamily(%v) = %.2f, want %.2f", c.families, got, c.want)
		}
	}
}

func TestLikenessAxis_BundleSize(t *testing.T) {
	cases := []struct {
		count int
		want  float64
	}{
		{0, 0.0},
		{-1, 0.0},
		{1, 0.1},
		{3, 0.3},
		{5, 0.5},
		{9, 0.9},
		{10, 1.0},
		{12, 1.0}, // saturates
		{42, 1.0},
	}
	for _, c := range cases {
		got := scoreBundleSize(c.count)
		if !approxEqual(got, c.want) {
			t.Errorf("scoreBundleSize(%d) = %.3f, want %.3f", c.count, got, c.want)
		}
	}
}

func TestLikenessAxis_Liquidity(t *testing.T) {
	cases := []struct {
		gate string
		want float64
	}{
		{"PASS", 1.0},
		{"WARN", 0.6},
		{"FAIL", 0.0},
		{"UNKNOWN", 0.0},
		{"", 0.0},
		{"unrecognized", 0.0},
	}
	for _, c := range cases {
		got := scoreLiquidity(c.gate)
		if !approxEqual(got, c.want) {
			t.Errorf("scoreLiquidity(%q) = %.2f, want %.2f", c.gate, got, c.want)
		}
	}
}

func TestLikenessAxis_ScoreBand(t *testing.T) {
	cases := []struct {
		score float64
		want  float64
	}{
		// RN1 core band (inclusive both ends)
		{5.0, 1.0},
		{7.5, 1.0},
		{9.5, 1.0},
		// Half band (low end)
		{4.0, 0.5},
		{4.9, 0.5},
		// Half band (high end)
		{9.6, 0.5},
		{10.5, 0.5},
		{12.0, 0.5},
		// Out of band
		{3.9, 0.0},
		{12.1, 0.0},
		{0.0, 0.0},
		{-1.0, 0.0},
	}
	for _, c := range cases {
		got := scoreScoreBand(c.score)
		if !approxEqual(got, c.want) {
			t.Errorf("scoreScoreBand(%.2f) = %.2f, want %.2f", c.score, got, c.want)
		}
	}
}

func TestLikenessAxis_Horizon(t *testing.T) {
	h := func(hrs float64) time.Duration {
		return time.Duration(hrs * float64(time.Hour))
	}
	cases := []struct {
		hrs  float64
		want float64
	}{
		// Core window (inclusive both ends)
		{6, 1.0},
		{24, 1.0},
		{72, 1.0},
		// Half (low)
		{2, 0.5},
		{5.9, 0.5},
		// Half (high)
		{72.5, 0.5},
		{168, 0.5},
		// Out
		{0, 0.0},
		{1, 0.0},
		{168.1, 0.0},
		{1000, 0.0},
		{-1, 0.0},
	}
	for _, c := range cases {
		got := scoreHorizon(h(c.hrs))
		if !approxEqual(got, c.want) {
			t.Errorf("scoreHorizon(%.1fh) = %.2f, want %.2f", c.hrs, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Weights sum to 1.0
// ---------------------------------------------------------------------------

func TestLikeness_WeightsSumToOne(t *testing.T) {
	sum := wSportPrefix + wFamily + wBundleSize + wLiquidity + wScoreBand + wHorizon
	if !approxEqual(sum, 1.0) {
		t.Errorf("weights sum = %.4f, want 1.0", sum)
	}
}

// ---------------------------------------------------------------------------
// Five forensic Layer-5 clusters: regression
//
// Per design §3.4, all 5 clusters must score >= 0.75 and at least 4
// must clear the RN1_PRIME threshold (>= 0.80). The exact expected
// totals depend on the fixture's binary_count values; we re-compute
// them here from the formula so the test does not silently drift if
// the fixture changes.
// ---------------------------------------------------------------------------

func TestLikeness_FiveForensicClusters(t *testing.T) {
	profile := loadFixtureProfile(t)
	if len(profile.Rn1Layer5Prototypes) != 5 {
		t.Fatalf("fixture should have 5 prototypes, got %d", len(profile.Rn1Layer5Prototypes))
	}

	type expectedResult struct {
		slug      string
		minTotal  float64
		band      LikenessBand
	}
	want := map[string]expectedResult{
		// ATP missing family => B=0, total = 1 - 0.25 = 0.75 exactly
		"atp-alcaraz-zverev-2026-01-30": {"atp-alcaraz-zverev-2026-01-30", 0.75, BandRn1Plausible},
		// NBA: all 6 axes max => total = 1.00
		"nba-sas-min-2026-05-10": {"nba-sas-min-2026-05-10", 1.00, BandRn1Prime},
		// Bayern: bundle 3 => C=0.3 => total = 1 - 0.15 + 0.15*0.3 = 0.895
		"bun-bay-hei-2026-05-02": {"bun-bay-hei-2026-05-02", 0.895, BandRn1Prime},
		// Parma: same shape as Bayern (3 markets, soccer_3way) => 0.895
		"sea-par-rom-2026-05-10": {"sea-par-rom-2026-05-10", 0.895, BandRn1Prime},
		// CS2: fixture binary_count=12 => C=1.0 => total = 1.00
		"cs2-mglz-g2-2026-05-11": {"cs2-mglz-g2-2026-05-11", 1.00, BandRn1Prime},
	}

	primeCount := 0
	for _, proto := range profile.Rn1Layer5Prototypes {
		in := LikenessInputFromPrototype(proto)
		score := ScoreLikeness(in, profile)
		exp, ok := want[proto.EventSlug]
		if !ok {
			t.Errorf("unexpected prototype slug %q", proto.EventSlug)
			continue
		}
		if !approxEqual(score.Total, exp.minTotal) {
			t.Errorf("%s: total = %.4f, want %.4f (band %s)", proto.EventSlug,
				score.Total, exp.minTotal, exp.band)
		}
		if score.Band != exp.band {
			t.Errorf("%s: band = %q, want %q (total %.4f)", proto.EventSlug,
				score.Band, exp.band, score.Total)
		}
		if score.Total < 0.75 {
			t.Errorf("%s: total %.4f below 0.75 floor (design §3.4 invariant)",
				proto.EventSlug, score.Total)
		}
		if score.Band == BandRn1Prime {
			primeCount++
		}
	}
	if primeCount < 4 {
		t.Errorf("only %d of 5 prototypes reached RN1_PRIME band; design §3.4 requires >=4", primeCount)
	}
}

// ---------------------------------------------------------------------------
// Axis isolation: zeroing one axis should drop the total by exactly
// that axis's weight, no more and no less.
// ---------------------------------------------------------------------------

func TestLikeness_AxisIsolation(t *testing.T) {
	profile := loadFixtureProfile(t)
	maxIn := LikenessInput{
		SportPrefix:   "nba",
		Families:      []string{"spread_ml_total_stack"},
		BinaryCount:   12,
		LiquidityGate: "PASS",
		Score:         9.5,
		TimeToStart:   24 * time.Hour,
	}
	maxScore := ScoreLikeness(maxIn, profile)
	if !approxEqual(maxScore.Total, 1.0) {
		t.Fatalf("max-input total = %.4f, want 1.0", maxScore.Total)
	}

	cases := []struct {
		name       string
		mutate     func(*LikenessInput)
		wantTotal  float64
	}{
		{"zero sport", func(in *LikenessInput) { in.SportPrefix = "xxx-unknown" },
			1.0 - wSportPrefix},
		{"zero family", func(in *LikenessInput) { in.Families = nil },
			1.0 - wFamily},
		{"zero bundle", func(in *LikenessInput) { in.BinaryCount = 0 },
			1.0 - wBundleSize},
		{"zero liquidity", func(in *LikenessInput) { in.LiquidityGate = "FAIL" },
			1.0 - wLiquidity},
		{"zero score band", func(in *LikenessInput) { in.Score = 0.0 },
			1.0 - wScoreBand},
		{"zero horizon", func(in *LikenessInput) { in.TimeToStart = 0 },
			1.0 - wHorizon},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := maxIn
			in.Families = append([]string(nil), maxIn.Families...) // defensive copy
			tc.mutate(&in)
			got := ScoreLikeness(in, profile).Total
			if !approxEqual(got, tc.wantTotal) {
				t.Errorf("%s: total = %.4f, want %.4f", tc.name, got, tc.wantTotal)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Non-sport candidates should land in NON_RN1
// ---------------------------------------------------------------------------

func TestLikeness_NonSportIsNonRn1(t *testing.T) {
	profile := loadFixtureProfile(t)
	in := LikenessInput{
		SportPrefix:   "", // unknown
		Families:      nil,
		BinaryCount:   2,
		LiquidityGate: "WARN",
		Score:         3.0,
		TimeToStart:   0,
	}
	score := ScoreLikeness(in, profile)
	if score.Band != BandNonRn1 {
		t.Errorf("non-sport candidate band = %q, want NON_RN1 (total %.4f)",
			score.Band, score.Total)
	}
	if score.Total >= 0.40 {
		t.Errorf("non-sport candidate total %.4f >= 0.40 (would not be NON_RN1)", score.Total)
	}
}

// ---------------------------------------------------------------------------
// Pure-function property: same input always yields same output
// ---------------------------------------------------------------------------

func TestLikeness_Deterministic(t *testing.T) {
	profile := loadFixtureProfile(t)
	in := LikenessInput{
		SportPrefix:   "bun",
		Families:      []string{"soccer_3way_moneyline"},
		BinaryCount:   3,
		LiquidityGate: "PASS",
		Score:         5.0,
		TimeToStart:   24 * time.Hour,
	}
	first := ScoreLikeness(in, profile)
	for i := 0; i < 5; i++ {
		next := ScoreLikeness(in, profile)
		if next.Total != first.Total || next.Band != first.Band {
			t.Errorf("iter %d: got total=%.4f band=%q; first total=%.4f band=%q",
				i, next.Total, next.Band, first.Total, first.Band)
		}
	}
}

// ---------------------------------------------------------------------------
// Nil profile guard
// ---------------------------------------------------------------------------

func TestLikeness_NilProfileSafe(t *testing.T) {
	in := LikenessInput{SportPrefix: "nba"}
	score := ScoreLikeness(in, nil)
	if score.Band != BandNonRn1 || score.Total != 0 {
		t.Errorf("nil profile should produce zero score, got total=%.4f band=%q",
			score.Total, score.Band)
	}
}

// ---------------------------------------------------------------------------
// Reasons contain no forbidden vocabulary
// ---------------------------------------------------------------------------

func TestLikeness_ReasonsAreSafe(t *testing.T) {
	profile := loadFixtureProfile(t)
	// Probe every axis-failure path.
	cases := []LikenessInput{
		{}, // all zeros
		{SportPrefix: "nba", Families: nil, BinaryCount: 1, LiquidityGate: "WARN", Score: 11.0, TimeToStart: 1 * time.Hour},
		{SportPrefix: "unknown-x", Families: []string{"unknown_family"}, BinaryCount: 0, LiquidityGate: "FAIL", Score: 0, TimeToStart: 0},
	}
	forbidden := []string{
		"buy", "sell", "stake", "position size", "wallet", "private", "order",
		"paper", "real", "live",
	}
	for _, in := range cases {
		score := ScoreLikeness(in, profile)
		for _, r := range score.Reasons {
			lower := r
			for _, bad := range forbidden {
				if containsLower(lower, bad) {
					t.Errorf("reason %q contains forbidden token %q", r, bad)
				}
			}
		}
	}
}

// containsLower is a tiny case-insensitive substring check used only
// in tests to avoid pulling in strings.ToLower at call sites.
func containsLower(s, substr string) bool {
	lower := []byte(s)
	for i := range lower {
		if lower[i] >= 'A' && lower[i] <= 'Z' {
			lower[i] += 'a' - 'A'
		}
	}
	subLower := []byte(substr)
	for i := range subLower {
		if subLower[i] >= 'A' && subLower[i] <= 'Z' {
			subLower[i] += 'a' - 'A'
		}
	}
	for i := 0; i+len(subLower) <= len(lower); i++ {
		match := true
		for j := range subLower {
			if lower[i+j] != subLower[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
