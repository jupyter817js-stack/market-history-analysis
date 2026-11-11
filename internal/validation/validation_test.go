package validation

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// findRepoRoot walks up from the test's working directory until it
// finds go.mod. Required by the static-check tests that scan source
// trees by absolute path.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found from %s", wd)
		}
		dir = parent
	}
}

// importsViolating parses every .go file in dir (excluding tests)
// and returns the subset of forbidden import paths that are
// actually imported.
func importsViolating(t *testing.T, dir string, forbidden map[string]bool) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Errorf("parse %s: %v", dir, err)
		return nil
	}
	seen := map[string]bool{}
	var v []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if forbidden[path] && !seen[path] {
					seen[path] = true
					v = append(v, path)
				}
			}
		}
	}
	return v
}

// ---------------------------------------------------------------------------
// Closed-enum and band classification
// ---------------------------------------------------------------------------

func TestValidation_ClosedEnumLikenessBand(t *testing.T) {
	want := map[LikenessBand]bool{
		BandRn1Prime: true, BandRn1Plausible: true,
		BandRn1Adjacent: true, BandNonRn1: true,
	}
	if len(AllLikenessBands) != len(want) {
		t.Fatalf("AllLikenessBands length mismatch: got %d want %d", len(AllLikenessBands), len(want))
	}
	for _, b := range AllLikenessBands {
		if !want[b] {
			t.Errorf("unexpected band %q in AllLikenessBands", b)
		}
	}
	if IsValidBand("PRIME") || IsValidBand("WATCHING") || IsValidBand("") {
		t.Error("IsValidBand must reject non-enum values")
	}
}

func TestValidation_BandClassification(t *testing.T) {
	cases := []struct {
		total float64
		want  LikenessBand
	}{
		{0.95, BandRn1Prime},
		{0.80, BandRn1Prime},
		{0.79, BandRn1Plausible},
		{0.60, BandRn1Plausible},
		{0.59, BandRn1Adjacent},
		{0.40, BandRn1Adjacent},
		{0.39, BandNonRn1},
		{0.0, BandNonRn1},
	}
	for _, c := range cases {
		got := classifyBand(c.total)
		if got != c.want {
			t.Errorf("classifyBand(%.2f) = %q, want %q", c.total, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Historical profile loader
// ---------------------------------------------------------------------------

func TestValidation_LoadProfile_Sample(t *testing.T) {
	root := findRepoRoot(t)
	path := filepath.Join(root, "testdata", "fixtures", "sample_historical_profile.json")
	p, err := LoadProfile(path)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.TotalClusters != 25385 {
		t.Errorf("TotalClusters=%d, want 25385", p.TotalClusters)
	}
	if len(p.Rn1Layer5Prototypes) != 5 {
		t.Errorf("Layer5Prototypes len=%d, want 5", len(p.Rn1Layer5Prototypes))
	}
	if !p.HasKnownPrefix("atp") || !p.HasKnownPrefix("bun") {
		t.Error("HasKnownPrefix should report atp + bun")
	}
	if p.HasKnownPrefix("xxx-not-a-prefix") {
		t.Error("HasKnownPrefix must reject unknown prefix")
	}
}

func TestValidation_LoadProfile_RejectsMissingFile(t *testing.T) {
	_, err := LoadProfile(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestValidation_LoadProfile_RejectsBadJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(p, []byte("{not valid"), 0644)
	_, err := LoadProfile(p)
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestValidation_LoadProfile_RejectsStructuralProblems(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name    string
		profile HistoricalProfile
	}{
		{"missing generated_at", HistoricalProfile{
			Rn1KnownPrefixes:    []string{"nba"},
			TotalClusters:       10,
			Rn1BundleSizeP50:    5,
			Rn1Layer5Prototypes: []Layer5Prototype{{EventSlug: "x"}},
		}},
		{"empty known prefixes", HistoricalProfile{
			GeneratedAt:         time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC),
			TotalClusters:       10,
			Rn1BundleSizeP50:    5,
			Rn1Layer5Prototypes: []Layer5Prototype{{EventSlug: "x"}},
		}},
		{"zero total clusters", HistoricalProfile{
			GeneratedAt:         time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC),
			Rn1KnownPrefixes:    []string{"nba"},
			Rn1BundleSizeP50:    5,
			Rn1Layer5Prototypes: []Layer5Prototype{{EventSlug: "x"}},
		}},
		{"no layer 5 prototypes", HistoricalProfile{
			GeneratedAt:      time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC),
			Rn1KnownPrefixes: []string{"nba"},
			TotalClusters:    10,
			Rn1BundleSizeP50: 5,
		}},
		{"family distribution far from 1", HistoricalProfile{
			GeneratedAt:           time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC),
			Rn1KnownPrefixes:      []string{"nba"},
			TotalClusters:         10,
			Rn1BundleSizeP50:      5,
			Rn1FamilyDistribution: map[string]float64{"x": 0.3, "y": 0.2}, // 0.5 -- too far from 1
			Rn1Layer5Prototypes:   []Layer5Prototype{{EventSlug: "x"}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.profile)
			p := filepath.Join(dir, "bad.json")
			_ = os.WriteFile(p, body, 0644)
			_, err := LoadProfile(p)
			if err == nil {
				t.Errorf("expected validation error for %s", tc.name)
			}
		})
	}
}

func TestValidation_LoadProfile_AcceptsZeroFamilyDistribution(t *testing.T) {
	// Empty family distribution is allowed (sum=0, no enforcement).
	p := HistoricalProfile{
		GeneratedAt:           time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC),
		Rn1KnownPrefixes:      []string{"nba"},
		TotalClusters:         10,
		Rn1BundleSizeP50:      5,
		Rn1Layer5Prototypes:   []Layer5Prototype{{EventSlug: "x"}},
		Rn1FamilyDistribution: map[string]float64{},
	}
	body, _ := json.Marshal(p)
	path := filepath.Join(t.TempDir(), "ok.json")
	_ = os.WriteFile(path, body, 0644)
	if _, err := LoadProfile(path); err != nil {
		t.Errorf("expected acceptance, got error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CI invariant: no network imports
// ---------------------------------------------------------------------------

func TestValidation_NoNetworkImport_StaticCheck(t *testing.T) {
	root := findRepoRoot(t)
	dir := filepath.Join(root, "internal", "validation")
	forbidden := map[string]bool{
		"net":                          true,
		"net/http":                     true,
		"net/url":                      true,
		"net/rpc":                      true,
		"golang.org/x/net/websocket":   true,
		"github.com/gorilla/websocket": true,
		"google.golang.org/grpc":       true,
	}
	v := importsViolating(t, dir, forbidden)
	if len(v) > 0 {
		t.Errorf("internal/validation imports forbidden network packages: %v", v)
	}
}

// ---------------------------------------------------------------------------
// CI invariant: no store/ingest/summary/replay imports (future leakage
// protection — the validation package is read-only and must not pull
// in DB or post-trade data sources at runtime).
// ---------------------------------------------------------------------------

func TestValidation_NoFutureLeakage_StaticCheck(t *testing.T) {
	root := findRepoRoot(t)
	dir := filepath.Join(root, "internal", "validation")
	forbidden := map[string]bool{
		"rn1-strategy-extractor/internal/store":   true,
		"rn1-strategy-extractor/internal/ingest":  true,
		"rn1-strategy-extractor/internal/summary": true,
		"rn1-strategy-extractor/internal/replay":  true,
	}
	v := importsViolating(t, dir, forbidden)
	if len(v) > 0 {
		t.Errorf("internal/validation imports forbidden packages: %v", v)
	}
}

// ---------------------------------------------------------------------------
// CI invariant: no wallet / signer / order placement tokens in source
// ---------------------------------------------------------------------------

func TestValidation_NoWalletOrTradingPath_StaticCheck(t *testing.T) {
	root := findRepoRoot(t)
	dir := filepath.Join(root, "internal", "validation")
	forbiddenSubstrings := []string{
		"signer", "keystore", "private-key", "privatekey", "private_key",
		"PostOrder", "post_order", "CancelOrder", "cancel_order",
		"PolygonRPC", "PolygonProvider", "EthClient",
		"/order", "/cancel",
		"\"buy\"", "\"sell\"",
	}
	// types.go and validation_test.go intentionally name some of
	// these tokens in safety statements / test names. The static
	// check exempts:
	//   - types.go: doc comment names forbidden tokens to forbid them
	//   - *_test.go: tests reference the tokens to grep for them
	exempt := map[string]bool{
		"types.go": true,
	}
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		if exempt[filepath.Base(p)] {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("read %s: %v", p, err)
			return nil
		}
		lower := strings.ToLower(string(body))
		for _, s := range forbiddenSubstrings {
			if strings.Contains(lower, strings.ToLower(s)) {
				t.Errorf("file %s contains forbidden token %q", p, s)
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// CI invariant: no forbidden vocabulary in output
// ---------------------------------------------------------------------------

func TestValidation_NoForbiddenVocabularyInOutput(t *testing.T) {
	// Build a fully-populated sample of each output type and
	// marshal to JSON. The check then greps the JSON body for
	// trade-action verbs / trade-economics nouns that must never
	// escape the validation package.
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)

	samples := []any{
		LikenessScore{
			SportPrefixComponent: 1.0, FamilyComponent: 1.0,
			BundleSizeComponent: 1.0, LiquidityComponent: 1.0,
			ScoreBandComponent: 1.0, HorizonComponent: 1.0,
			Total: 1.0, Band: BandRn1Prime,
			Reasons: []string{"sport prefix recognized", "family detected"},
		},
		TickObservation{
			ObservedAt: now, CycleTs: "2026-05-16T08-00-00Z",
			EventSlug: "lal-ala-bar-2026-05-16-more-markets",
			ConditionID: "0xabc", AskA: 0.51, AskB: 0.48,
			BidA: 0.50, BidB: 0.47, SpreadA: 0.01, SpreadB: 0.01,
			TopDepthA: 1200, TopDepthB: 1100,
			Likeness: 0.92, Band: BandRn1Prime,
		},
		HypotheticalLeg{
			EventSlug: "x", ConditionID: "0xabc",
			TickCount: 5, HypoSharesA: 5, HypoSharesB: 5,
			HypoCumCostA: 2.5, HypoCumCostB: 2.4,
			HypoWavgA: 0.5, HypoWavgB: 0.48, HypoSumWavg: 0.98,
			FirstObservedAt: now, LastObservedAt: now,
			MinHypoSumWavg: 0.95, TicksBelow1_0: 3, TicksBelow0_95: 1,
		},
		EnvelopeWindow{
			EventSlug: "x", ConditionID: "0xabc",
			Likeness: 0.92, Band: BandRn1Prime,
			FirstObservedAt: now, LastObservedAt: now,
			TickCount: 8, MinHypoSumWavg: 0.91,
			MinHypoSumWavgAt: now, HoursBelow1_0: 12, HoursBelow0_95: 6,
			WindowClosure: "RESOLVED",
		},
		SettlementOutcome{
			EventSlug: "x", ResolvedAt: now,
			WinningOutcomes: []string{"YES"},
			PerCondition: []ConditionResolution{{
				ConditionID: "0xabc", WinningSide: "A",
				MergeAt: now, HypoMergeUnits: 5, HypoMergeCost: 4.7,
				HypoMergeRealized: 5.0, HypoMergePnl: 0.3,
				HypoRedeemSide: "A", HypoRedeemUnits: 1,
				HypoRedeemCost: 0.5, HypoRedeemRealized: 1.0,
				HypoRedeemPnl: 0.5, HypoConditionPnl: 0.8,
			}},
			HypoTotalCost: 5.2, HypoMergePnl: 0.3, HypoRedeemPnl: 0.5,
			HypoNetPnl: 0.8, HypoReturnPct: 15.4,
		},
		EvaluationSnapshot{
			GeneratedAt: now, WindowStartAt: now, WindowEndAt: now,
			TotalCandidatesScored: 100, TotalEnvelopeWindows: 30,
			TotalSettlements: 20, EnvelopeOccurrenceRate: 0.6,
			MedianMinHypoSumWavg: 0.93, HypoNetPnlTotal: 12.4,
			FamilyPositiveCount: 3, SportPrefixPositiveCount: 6,
			SettlementCoverageRate: 0.85, LikenessKsTestPValue: 0.42,
			CiInvariantViolations: 0,
		},
	}

	body, err := json.MarshalIndent(samples, "", "  ")
	if err != nil {
		t.Fatalf("marshal samples: %v", err)
	}
	lower := strings.ToLower(string(body))

	forbidden := []string{
		// trade-action verbs
		"\"buy\"", "\"sell\"", "enter long", "enter short",
		"exit long", "exit short", "place order", "submit order",
		"take profit", "stop loss", "cancel order",
		// trade-economics nouns
		"stake", "position size", "expected profit",
		"expected return", "expected pnl", "arbitrage",
		// boundary-crossing words
		"paper_", "real_", "live_", "wallet", "private_key",
		"fill_id", "order_id",
	}
	for _, bad := range forbidden {
		if strings.Contains(lower, bad) {
			t.Errorf("output contains forbidden token %q\n%s", bad, body)
		}
	}
}

// ---------------------------------------------------------------------------
// CI invariant: no bare 9-state names in output
// ---------------------------------------------------------------------------

func TestValidation_NoBareNineStateInOutput(t *testing.T) {
	// Same samples as above; verify none contain raw 9-state
	// names that would imply the package is asserting actual
	// trading transitions.
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	samples := []any{
		LikenessScore{Total: 1.0, Band: BandRn1Prime,
			Reasons: []string{"sport prefix recognized"}},
		TickObservation{ObservedAt: now, EventSlug: "x", Likeness: 0.9, Band: BandRn1Prime},
		HypotheticalLeg{EventSlug: "x", FirstObservedAt: now},
		EnvelopeWindow{EventSlug: "x", FirstObservedAt: now,
			Band: BandRn1Prime, WindowClosure: "RESOLVED"},
		SettlementOutcome{EventSlug: "x", ResolvedAt: now,
			WinningOutcomes: []string{"YES"}},
	}
	body, _ := json.Marshal(samples)
	for _, bare := range []string{
		`"ACCUMULATE_A"`, `"OPEN_B"`, `"EXPAND"`, `"LAYER"`,
		`"MERGE_HARVEST"`, `"REDEEM"`,
	} {
		if strings.Contains(string(body), bare) {
			t.Errorf("output contains bare 9-state token %q", bare)
		}
	}
}
