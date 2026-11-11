package selection

import (
	"testing"
	"time"
)

func tptr(t time.Time) *time.Time { return &t }
func bptr(b bool) *bool           { return &b }

func TestDetectSportPrefix(t *testing.T) {
	cases := map[string]string{
		"nba-sas-min-2026-05-10":         "nba",
		"epl-wes-ars-2026-05-10":         "epl",
		"cs2-mglz-g2-2026-05-11":         "cs2",
		"atp-alcaraz-zverev-2026-01-30":  "atp",
		"random-non-sport-slug":          "",
		"":                               "",
	}
	for slug, want := range cases {
		if got := DetectSportPrefix(slug); got != want {
			t.Errorf("DetectSportPrefix(%q) = %q, want %q", slug, got, want)
		}
	}
}

// TestDetectSportPrefix_HardeningPatch1 verifies the 13 league prefixes
// added in Hardening Patch 1 (2026-05-12) are recognized.
func TestDetectSportPrefix_HardeningPatch1(t *testing.T) {
	cases := map[string]string{
		"elc-wba-sot-2026-03-11":               "elc",
		"lib-est-fla-2026-04-29":               "lib",
		"uef-pol-nld-2025-11-14":               "uef",
		"uel-scf-scb-2026-05-07-more-markets":  "uel",
		"tur-fen-bes-2026-04-05":               "tur",
		"spl-ett-kha-2026-01-12":               "spl",
		"itsb-mod-reg-2026-05-01-more-markets": "itsb",
		"bra-bot-cre-2026-05-02-more-markets":  "bra",
		"fl1-aja-sbr-2026-03-21":               "fl1",
		"ere-nec-aja-2025-12-20":               "ere",
		"cdr-atm-rso-2026-04-18":               "cdr",
		"efa-mnc-sot-2026-04-25-more-markets":  "efa",
		"cze1-ssp-asp-2026-05-09":              "cze1",
	}
	for slug, want := range cases {
		if got := DetectSportPrefix(slug); got != want {
			t.Errorf("DetectSportPrefix(%q) = %q, want %q", slug, got, want)
		}
	}
}

// TestDecide_NewLeaguePrefixesNoLongerScoreZero verifies a 3-market
// soccer event under a Hardening-Patch-1 prefix scores above zero.
func TestDecide_NewLeaguePrefixesNoLongerScoreZero(t *testing.T) {
	now := time.Date(2026, 4, 5, 8, 0, 0, 0, time.UTC)
	endIn4h := now.Add(4 * time.Hour)
	cfg := DefaultConfig()
	cfg.IgnoreResolutionState = true
	leagues := []string{"elc", "fl1", "tur", "bra", "spl", "ere", "cdr", "lib", "uef", "uel", "efa", "itsb", "cze1"}
	for _, lg := range leagues {
		slug := lg + "-aaa-bbb-2026-04-05"
		markets := []MarketMeta{
			{EventSlug: slug, Title: "Will Team A win on 2026-04-05?",
				ClobTokenIDs: `["x","y"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
			{EventSlug: slug, Title: "Will Team B win on 2026-04-05?",
				ClobTokenIDs: `["z","w"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
			{EventSlug: slug, Title: "Will Team A vs. Team B end in a draw?",
				ClobTokenIDs: `["p","q"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
		}
		f := ExtractFeatures(markets, now)
		d := Decide(f, cfg, now)
		if !d.Eligible {
			t.Errorf("league %q expected eligible, got reasons=%v", lg, d.Reasons)
		}
		if d.Score < 5 {
			t.Errorf("league %q expected score >= 5 (3 binary + soccer family + near-term), got %v", lg, d.Score)
		}
	}
}

// TestDetectSportPrefix_HardeningPatch2 verifies the 8 prefixes added
// by the residual-anomaly investigation are recognized.
func TestDetectSportPrefix_HardeningPatch2(t *testing.T) {
	cases := map[string]string{
		"acn-alg-cdr-2026-01-06":               "acn",
		"col-ray-aek1-2026-04-09":              "col",
		"por-avs-por-2026-05-10":               "por",
		"por-ton-gil-2026-04-13":               "por",
		"efl-cfc-ars-2026-01-14":               "efl",
		"fif-rsa-zam-2025-11-15":               "fif",
		"fif-sen-per-2026-03-28":               "fif",
		"fif-cyp-mol-2026-03-30-more-markets":  "fif",
		"mex-gua-ame-2026-02-14":               "mex",
		"bl2-scp-dyn-2026-03-21":               "bl2",
		"fr2-clf-gui-2026-05-09-more-markets":  "fr2",
	}
	for slug, want := range cases {
		if got := DetectSportPrefix(slug); got != want {
			t.Errorf("DetectSportPrefix(%q) = %q, want %q", slug, got, want)
		}
	}
}

// TestDecide_Patch2LeaguePrefixesNoLongerScoreZero builds a synthetic
// 3-market soccer event under each of the 8 Patch-2 prefixes and
// verifies eligibility + score >= 5.
func TestDecide_Patch2LeaguePrefixesNoLongerScoreZero(t *testing.T) {
	now := time.Date(2026, 4, 5, 8, 0, 0, 0, time.UTC)
	endIn4h := now.Add(4 * time.Hour)
	cfg := DefaultConfig()
	cfg.IgnoreResolutionState = true
	leagues := []string{"acn", "col", "por", "efl", "fif", "mex", "bl2", "fr2"}
	for _, lg := range leagues {
		slug := lg + "-aaa-bbb-2026-04-05"
		markets := []MarketMeta{
			{EventSlug: slug, Title: "Will Team A win on 2026-04-05?",
				ClobTokenIDs: `["x","y"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
			{EventSlug: slug, Title: "Will Team B win on 2026-04-05?",
				ClobTokenIDs: `["z","w"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
			{EventSlug: slug, Title: "Will Team A vs. Team B end in a draw?",
				ClobTokenIDs: `["p","q"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
		}
		f := ExtractFeatures(markets, now)
		d := Decide(f, cfg, now)
		if !d.Eligible {
			t.Errorf("league %q expected eligible, got reasons=%v", lg, d.Reasons)
		}
		if d.Score < 5 {
			t.Errorf("league %q expected score >= 5 (3 binary + soccer family + near-term), got %v", lg, d.Score)
		}
	}
}

// TestDetectSportPrefix_HardeningPatch3 verifies the 4 prefixes added
// by Patch 3 (residual missing-prefix candidates).
func TestDetectSportPrefix_HardeningPatch3(t *testing.T) {
	cases := map[string]string{
		"arg-riv-cat-2026-03-08":               "arg",
		"arg-vel-gim-2026-05-10":               "arg",
		"es2-cor-mir-2026-03-27":               "es2",
		"es2-cor-mir-2026-03-27-more-markets":  "es2",
		"dota2-aur1-navi-2025-12-10":           "dota2",
		"dota2-og-ty-2025-12-05":               "dota2",
		"scop-hib-cel-2026-05-03":              "scop",
		"scop-ran-mot-2026-04-26":              "scop",
	}
	for slug, want := range cases {
		if got := DetectSportPrefix(slug); got != want {
			t.Errorf("DetectSportPrefix(%q) = %q, want %q", slug, got, want)
		}
	}
}

// TestDecide_Patch3LeaguePrefixesNoLongerScoreZero verifies a 3-market
// event under each Patch-3 prefix becomes eligible and scores >= 5.
// For dota2, the synthetic event uses a BO3 derivative shape that
// triggers bo3_esports_stack (score >= 9.5 if all binary).
func TestDecide_Patch3LeaguePrefixesNoLongerScoreZero(t *testing.T) {
	now := time.Date(2026, 4, 5, 8, 0, 0, 0, time.UTC)
	endIn4h := now.Add(4 * time.Hour)
	cfg := DefaultConfig()
	cfg.IgnoreResolutionState = true

	// arg, es2, scop -- soccer 3-way moneyline.
	soccerLeagues := []string{"arg", "es2", "scop"}
	for _, lg := range soccerLeagues {
		slug := lg + "-aaa-bbb-2026-04-05"
		markets := []MarketMeta{
			{EventSlug: slug, Title: "Will Team A win on 2026-04-05?",
				ClobTokenIDs: `["x","y"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
			{EventSlug: slug, Title: "Will Team B win on 2026-04-05?",
				ClobTokenIDs: `["z","w"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
			{EventSlug: slug, Title: "Will Team A vs. Team B end in a draw?",
				ClobTokenIDs: `["p","q"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
		}
		f := ExtractFeatures(markets, now)
		d := Decide(f, cfg, now)
		if !d.Eligible {
			t.Errorf("league %q expected eligible, got reasons=%v", lg, d.Reasons)
		}
		if d.Score < 5 {
			t.Errorf("league %q expected score >= 5, got %v", lg, d.Score)
		}
	}

	// dota2 -- BO3 esports stack (series + map1 + map2 + handicap + total).
	{
		slug := "dota2-aaa-bbb-2026-04-05"
		markets := []MarketMeta{
			{EventSlug: slug, Title: "Dota 2: Aurora vs Natus Vincere (BO3)",
				MarketSlug: slug, ClobTokenIDs: `["a","b"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
			{EventSlug: slug, Title: "Dota 2: Aurora vs Natus Vincere - Map 1 Winner",
				MarketSlug: slug + "-game1", ClobTokenIDs: `["c","d"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
			{EventSlug: slug, Title: "Dota 2: Aurora vs Natus Vincere - Map 2 Winner",
				MarketSlug: slug + "-game2", ClobTokenIDs: `["e","f"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
			{EventSlug: slug, Title: "Map Handicap: Aurora (-1.5)",
				MarketSlug: slug + "-handicap", ClobTokenIDs: `["g","h"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
			{EventSlug: slug, Title: "Games Total: O/U 2.5",
				MarketSlug: slug + "-total-games-2pt5", ClobTokenIDs: `["i","j"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
		}
		f := ExtractFeatures(markets, now)
		d := Decide(f, cfg, now)
		if !d.Eligible {
			t.Errorf("dota2: expected eligible, got reasons=%v", d.Reasons)
		}
		// 5 binary + bo3_esports_stack family + near-term + ≥4-market bonus = 8.5 minimum.
		if d.Score < 8 {
			t.Errorf("dota2: expected score >= 8 (5 binary + bo3 family + bonuses), got %v (families=%v)", d.Score, f.Families)
		}
	}
}

// TestDecide_DoNotPatchCasesStayLow verifies the 4 anomaly cases the
// Patch-2 investigation explicitly flagged as DO_NOT_ADD stay at low
// scores. Two are single-market events (correctly rejected by
// MinMarketsPerEvent=2); two are non-sport futures / exhibitions
// (no recognized sport prefix).
func TestDecide_DoNotPatchCasesStayLow(t *testing.T) {
	now := time.Date(2026, 4, 5, 8, 0, 0, 0, time.UTC)
	endIn4h := now.Add(4 * time.Hour)
	cfg := DefaultConfig()
	cfg.IgnoreResolutionState = true

	// 1. CS2 single-market event (1 condition only).
	{
		markets := []MarketMeta{
			{EventSlug: "cs2-b8-fal2-2025-12-04", Title: "Counter-Strike: B8 vs Team Falcons (BO1)",
				ClobTokenIDs: `["a","b"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
		}
		f := ExtractFeatures(markets, now)
		d := Decide(f, cfg, now)
		if d.Eligible {
			t.Errorf("cs2-b8-fal2 single-market: expected NOT eligible (MinMarketsPerEvent=2), got eligible (score=%v)", d.Score)
		}
	}
	// 2. ATP single-market event (recognized prefix, but 1 condition).
	{
		markets := []MarketMeta{
			{EventSlug: "atp-norrie-sonego-2025-11-07", Title: "Open de Moselle: Norrie vs Sonego",
				ClobTokenIDs: `["a","b"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
		}
		f := ExtractFeatures(markets, now)
		d := Decide(f, cfg, now)
		if d.Eligible {
			t.Errorf("atp-norrie-sonego single-market: expected NOT eligible, got eligible (score=%v)", d.Score)
		}
	}
	// 3. Non-sport future market.
	{
		markets := []MarketMeta{
			{EventSlug: "world-series-champion-2025", Title: "Will the Toronto Blue Jays win the 2025 World Series?",
				ClobTokenIDs: `["a","b"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
			{EventSlug: "world-series-champion-2025", Title: "Will the Los Angeles Dodgers win the 2025 World Series?",
				ClobTokenIDs: `["c","d"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
		}
		f := ExtractFeatures(markets, now)
		d := Decide(f, cfg, now)
		if d.Eligible {
			t.Errorf("world-series-champion-2025: expected NOT eligible (non-sport prefix), got eligible (score=%v)", d.Score)
		}
	}
	// 4. Exhibition prop.
	{
		markets := []MarketMeta{
			{EventSlug: "battle-of-the-sexes-aryna-sabalenka-vs-nick-kyrgios", Title: "Battle of the Sexes",
				ClobTokenIDs: `["a","b"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
		}
		f := ExtractFeatures(markets, now)
		d := Decide(f, cfg, now)
		if d.Eligible {
			t.Errorf("battle-of-the-sexes: expected NOT eligible (non-sport prefix), got eligible (score=%v)", d.Score)
		}
	}
}

// TestDetectSportPrefix_LiveScannerPatch2 verifies the 6 prefixes added
// by Live Scanner Prefix Patch 2 (2026-05-12) are recognized. Real
// slug samples come from the 2026-05-12 48h-horizon anomaly log.
func TestDetectSportPrefix_LiveScannerPatch2(t *testing.T) {
	cases := map[string]string{
		"brco-int-ath-2026-05-12":               "brco",
		"brco-cre-bah-2026-05-13-more-markets":  "brco",
		"egy1-phf-mos-2026-05-12":               "egy1",
		"egy1-eis-teg-2026-05-13-more-markets":  "egy1",
		"kor-gwa-seo-2026-05-12":                "kor",
		"kor-any-san-2026-05-13-more-markets":   "kor",
		"bol1-ant-uvi-2026-05-12":               "bol1",
		"bol1-bol-nap-2026-05-13-more-markets":  "bol1",
		"j1100-zel-ver-2026-05-13":              "j1100",
		"j1100-vis-kyo-2026-05-13-more-markets": "j1100",
		"nwsl-bos-pri-2026-05-12":               "nwsl",
		"nwsl-bos-pri-2026-05-12-more-markets":  "nwsl",
	}
	for slug, want := range cases {
		if got := DetectSportPrefix(slug); got != want {
			t.Errorf("DetectSportPrefix(%q) = %q, want %q", slug, got, want)
		}
	}
}

// TestDetectSportPrefix_Patch2_NonSportStillRejected guards against
// false positives from the new prefixes. Each example was crafted to
// sit close to one of the six new prefixes without matching the
// strict `<prefix>-` rule used by DetectSportPrefix.
func TestDetectSportPrefix_Patch2_NonSportStillRejected(t *testing.T) {
	cases := []string{
		"jerome-powell-out-as-fed-chair-by",
		"eurovision-2026-second-semi-final",
		"uk-gdp-growth-in-q1-2026-675",
		"argentina-presidential-election-2027",
		// `kor` is the shortest new prefix; verify lookalikes don't match.
		"korea-russia-summit-2026",         // longer "korea-" word, no dash split at "kor-"
		"korean-popularity-poll-2026",      // different stem
		"korperformance-2026",              // no dash at all after kor
		// `bol1` digit-bearing prefix; verify common "bol-" stems don't match it.
		"bolivian-presidential-poll-2027",  // "bolivian-" not "bol1-"
		// `nwsl` lookalike.
		"nwslchampions-prediction-special", // no dash → no match
		// `brco` near-miss: bra is already in list, but "brc-" alone is not.
		"brc-team-x-team-y-2026-04-01",
	}
	for _, slug := range cases {
		if got := DetectSportPrefix(slug); got != "" {
			t.Errorf("DetectSportPrefix(%q) = %q, want empty (non-sport / non-matching)", slug, got)
		}
	}
}

// TestDecide_LiveScannerPatch2LeaguePrefixesNoLongerScoreZero builds
// a synthetic 3-market soccer event under each of the 6 new prefixes
// and verifies eligibility + score >= 5 (3 binary + soccer family +
// near-term). Mirrors the Patch 1 / 2 / 3 league tests.
func TestDecide_LiveScannerPatch2LeaguePrefixesNoLongerScoreZero(t *testing.T) {
	now := time.Date(2026, 5, 12, 8, 0, 0, 0, time.UTC)
	endIn4h := now.Add(4 * time.Hour)
	cfg := DefaultConfig()
	cfg.IgnoreResolutionState = true
	leagues := []string{"brco", "egy1", "kor", "bol1", "j1100", "nwsl"}
	for _, lg := range leagues {
		slug := lg + "-aaa-bbb-2026-05-12"
		markets := []MarketMeta{
			{EventSlug: slug, Title: "Will Team A win on 2026-05-12?",
				ClobTokenIDs: `["x","y"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
			{EventSlug: slug, Title: "Will Team B win on 2026-05-12?",
				ClobTokenIDs: `["z","w"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
			{EventSlug: slug, Title: "Will Team A vs. Team B end in a draw?",
				ClobTokenIDs: `["p","q"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
		}
		f := ExtractFeatures(markets, now)
		d := Decide(f, cfg, now)
		if !d.Eligible {
			t.Errorf("league %q expected eligible, got reasons=%v", lg, d.Reasons)
		}
		if d.Score < 5 {
			t.Errorf("league %q expected score >= 5 (3 binary + soccer family + near-term), got %v", lg, d.Score)
		}
	}
}

// TestDetectSportPrefix_LiveScannerPatch3 verifies the 8 soccer
// prefixes added by Live Scanner Prefix Patch 3 (2026-05-12) are
// recognized. Slug samples come from the 72h / 96h wider-horizon
// dry-run anomaly log. Cricket (`crint`) is intentionally excluded.
func TestDetectSportPrefix_LiveScannerPatch3(t *testing.T) {
	cases := map[string]string{
		"j2100-st-ren-2026-05-16":               "j2100",
		"j2100-fuk-con-2026-05-16-more-markets": "j2100",
		"col1-atn-int-2026-05-12":               "col1",
		"col1-cdp-onc-2026-05-13-more-markets":  "col1",
		"ukr1-epi-pol-2026-05-12":               "ukr1",
		"ukr1-kud-ruk-2026-05-13-more-markets":  "ukr1",
		"per1-fcc-csc-2026-05-15":               "per1",
		"per1-cud-cag-2026-05-15-more-markets":  "per1",
		"chi1-ccu-aci-2026-05-15":               "chi1",
		"chi1-cdp-cls-2026-05-15-more-markets":  "chi1",
		"aus-ade-auc-2026-05-15":                "aus",
		"aus-new-syd-2026-05-16-more-markets":   "aus",
		"el1-sto-ste-2026-05-13":                "el1",
		"el1-bra-bol-2026-05-14-more-markets":   "el1",
		"el2-sal-gri-2026-05-15":                "el2",
		"el2-not-ches-2026-05-15-more-markets":  "el2",
	}
	for slug, want := range cases {
		if got := DetectSportPrefix(slug); got != want {
			t.Errorf("DetectSportPrefix(%q) = %q, want %q", slug, got, want)
		}
	}
}

// TestDetectSportPrefix_Patch3_NonSportStillRejected guards against
// false positives. Each example sits close to one of the 8 new
// prefixes (especially the 3-char `aus`, `el1`, `el2`) without
// matching the strict `<prefix>-` rule.
func TestDetectSportPrefix_Patch3_NonSportStillRejected(t *testing.T) {
	cases := []string{
		// `aus` is the shortest new prefix — make sure lookalikes don't match.
		"austria-elections-2027",        // longer "austria-" word
		"austrian-presidential-poll",    // different stem
		"australia-trade-deal-by-2027",  // longer "australia-" word, no dash split at "aus-"
		"austerity-vote-2026",           // unrelated word starting with "aus"
		// `col1` / `el1` / `el2` near-misses (digit-bearing strict).
		"colombia-presidential-2027",    // "colombia-" not "col1-"
		"election-2026-result",          // "election-" not "el1-" / "el2-"
		"ele-2026-night",                // "ele-" not "el1-" / "el2-"
		// `ukr1` near-miss.
		"ukraine-russia-ceasefire-2026", // "ukraine-" not "ukr1-"
		// `j2100` and `per1` near-misses.
		"japan-pm-2026",                 // "japan-" not "j2100-"
		"performance-poll-2026",         // "performance-" not "per1-"
		// `chi1` near-misses.
		"china-gdp-2026-q1",             // "china-" not "chi1-"
		"chi-trade-talk-by-2027",        // "chi-" alone is not in the prefix list
	}
	for _, slug := range cases {
		if got := DetectSportPrefix(slug); got != "" {
			t.Errorf("DetectSportPrefix(%q) = %q, want empty (non-sport / non-matching)", slug, got)
		}
	}
}

// TestDetectSportPrefix_Patch3_CricketStillUnknown documents that
// Patch 3 deliberately does NOT add `crint`. Cricket events should
// continue to fall into the UNKNOWN_PREFIX channel until a separate
// policy decision is made about cricket family detection.
func TestDetectSportPrefix_Patch3_CricketStillUnknown(t *testing.T) {
	cases := []string{
		"crint-bgd-pak-2026-05-08",
		"crint-bgd-pak-2026-05-08-team-top-batter",
		"crint-cyp-fin-2026-05-08-toss-match-double",
		"crint-cyp-fin-2026-05-08-most-sixes",
	}
	for _, slug := range cases {
		if got := DetectSportPrefix(slug); got != "" {
			t.Errorf("crint (cricket) was excluded from Patch 3 on purpose; DetectSportPrefix(%q) returned %q, want empty", slug, got)
		}
	}
}

// TestDecide_LiveScannerPatch3LeaguePrefixesNoLongerScoreZero builds
// a synthetic 3-market soccer event under each of the 8 new prefixes
// and verifies eligibility + score >= 5 (3 binary + soccer family +
// near-term). Mirrors the Patch 1 / 2 / 2 league tests.
func TestDecide_LiveScannerPatch3LeaguePrefixesNoLongerScoreZero(t *testing.T) {
	now := time.Date(2026, 5, 12, 8, 0, 0, 0, time.UTC)
	endIn4h := now.Add(4 * time.Hour)
	cfg := DefaultConfig()
	cfg.IgnoreResolutionState = true
	leagues := []string{"j2100", "col1", "ukr1", "per1", "chi1", "aus", "el1", "el2"}
	for _, lg := range leagues {
		slug := lg + "-aaa-bbb-2026-05-12"
		markets := []MarketMeta{
			{EventSlug: slug, Title: "Will Team A win on 2026-05-12?",
				ClobTokenIDs: `["x","y"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
			{EventSlug: slug, Title: "Will Team B win on 2026-05-12?",
				ClobTokenIDs: `["z","w"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
			{EventSlug: slug, Title: "Will Team A vs. Team B end in a draw?",
				ClobTokenIDs: `["p","q"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
		}
		f := ExtractFeatures(markets, now)
		d := Decide(f, cfg, now)
		if !d.Eligible {
			t.Errorf("league %q expected eligible, got reasons=%v", lg, d.Reasons)
		}
		if d.Score < 5 {
			t.Errorf("league %q expected score >= 5 (3 binary + soccer family + near-term), got %v", lg, d.Score)
		}
	}
}

func TestIsBinary(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`["1234","5678"]`, true},
		{`["a"]`, false},
		{`["a","b","c"]`, false},
		{``, false},
		{`not-json`, false},
	}
	for _, c := range cases {
		if got := IsBinary(c.in); got != c.want {
			t.Errorf("IsBinary(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDetectFamilies_Soccer3Way(t *testing.T) {
	markets := []MarketMeta{
		{Title: "Will Bayern Munich win on 2026-05-02?", MarketSlug: "bun-bay-hei-2026-05-02-bay"},
		{Title: "Will SC Heidenheim win on 2026-05-02?", MarketSlug: "bun-bay-hei-2026-05-02-hei"},
		{Title: "Will Bayern Munich vs. Heidenheim end in a draw?", MarketSlug: "bun-bay-hei-2026-05-02-draw"},
	}
	got := DetectFamilies(markets)
	found := false
	for _, f := range got {
		if f == "soccer_3way_moneyline" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected soccer_3way_moneyline, got %v", got)
	}
}

func TestDetectFamilies_BO3(t *testing.T) {
	markets := []MarketMeta{
		{Title: "Counter-Strike: TheMongolz vs G2 (BO3) - PGL Astana Group Stage", MarketSlug: "cs2-mglz-g2-2026-05-11"},
		{Title: "Counter-Strike: TheMongolz vs G2 - Map 1 Winner", MarketSlug: "cs2-mglz-g2-2026-05-11-game1"},
		{Title: "Counter-Strike: TheMongolz vs G2 - Map 2 Winner", MarketSlug: "cs2-mglz-g2-2026-05-11-game2"},
		{Title: "Map Handicap: G2 (-1.5) vs TheMongolz (+1.5)", MarketSlug: "cs2-mglz-g2-2026-05-11-map-handicap-home-1pt5"},
		{Title: "Games Total: O/U 2.5", MarketSlug: "cs2-mglz-g2-2026-05-11-total-games-2pt5"},
	}
	got := DetectFamilies(markets)
	found := false
	for _, f := range got {
		if f == "bo3_esports_stack" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected bo3_esports_stack, got %v", got)
	}
}

func TestDetectFamilies_SpreadMLTotal(t *testing.T) {
	markets := []MarketMeta{
		{Title: "Spurs vs. Timberwolves", MarketSlug: "nba-sas-min-2026-05-10"},
		{Title: "Spread: Spurs (-5.5)", MarketSlug: "nba-sas-min-2026-05-10-spread-away-5pt5"},
		{Title: "Spurs vs. Timberwolves: O/U 217.5", MarketSlug: "nba-sas-min-2026-05-10-total-217pt5"},
	}
	got := DetectFamilies(markets)
	found := false
	for _, f := range got {
		if f == "spread_ml_total_stack" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected spread_ml_total_stack, got %v", got)
	}
}

// TestDetectFamilies_TennisSetTotal verifies the Hardening-Patch-1
// tennis family detector triggers on ATP-style derivatives.
func TestDetectFamilies_TennisSetTotal(t *testing.T) {
	markets := []MarketMeta{
		{EventSlug: "atp-alcaraz-zverev-2026-01-30",
			Title: "Australian Open Men's: Carlos Alcaraz vs Alexander Zverev",
			MarketSlug: "atp-alcaraz-zverev-2026-01-30"},
		{EventSlug: "atp-alcaraz-zverev-2026-01-30",
			Title: "First Set Winner: Alcaraz vs Zverev",
			MarketSlug: "atp-alcaraz-zverev-2026-01-30-first-set-winner-Alcaraz-vs-Zverev"},
		{EventSlug: "atp-alcaraz-zverev-2026-01-30",
			Title: "Match Total: O/U 36.5",
			MarketSlug: "atp-alcaraz-zverev-2026-01-30-match-total-36pt5"},
		{EventSlug: "atp-alcaraz-zverev-2026-01-30",
			Title: "Match Total: O/U 40.5",
			MarketSlug: "atp-alcaraz-zverev-2026-01-30-match-total-40pt5"},
	}
	got := DetectFamilies(markets)
	found := false
	for _, f := range got {
		if f == "tennis_set_total_stack" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected tennis_set_total_stack in families, got %v", got)
	}
}

// TestDetectFamilies_ExpandedCompanion verifies the "-more-markets"
// companion family triggers when the event_slug ends with the suffix
// and there are >=5 binary markets.
func TestDetectFamilies_ExpandedCompanion(t *testing.T) {
	markets := []MarketMeta{}
	for i := 0; i < 9; i++ {
		markets = append(markets, MarketMeta{
			EventSlug:    "ucl-bay-psg-2026-05-06-more-markets",
			Title:        "Spread: PSG (-1.5)",
			MarketSlug:   "ucl-bay-psg-2026-05-06-more-markets-spread",
			ClobTokenIDs: `["a","b"]`,
		})
	}
	got := DetectFamilies(markets)
	found := false
	for _, f := range got {
		if f == "expanded_companion_stack" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected expanded_companion_stack, got %v", got)
	}

	// Negative case: same slug suffix but only 3 markets → no family.
	smaller := markets[:3]
	got2 := DetectFamilies(smaller)
	for _, f := range got2 {
		if f == "expanded_companion_stack" {
			t.Errorf("expected expanded_companion_stack NOT to trigger on 3 markets, got %v", got2)
		}
	}
}

func TestDecide_NBAEventIsEligible(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	endIn4h := now.Add(4 * time.Hour)
	markets := []MarketMeta{
		{EventSlug: "nba-sas-min-2026-05-10", Title: "Spurs vs. Timberwolves",
			ClobTokenIDs: `["a","b"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
		{EventSlug: "nba-sas-min-2026-05-10", Title: "Spread: Spurs (-5.5)",
			ClobTokenIDs: `["c","d"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
		{EventSlug: "nba-sas-min-2026-05-10", Title: "Spurs vs. Timberwolves: O/U 217.5",
			ClobTokenIDs: `["e","f"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
	}
	f := ExtractFeatures(markets, now)
	d := Decide(f, DefaultConfig(), now)
	if !d.Eligible {
		t.Fatalf("expected eligible, got reasons=%v", d.Reasons)
	}
	if d.Score < 3 {
		t.Errorf("expected score >= 3 (3 binary + family + near-term), got %v", d.Score)
	}
}

func TestDecide_ClosedMarketRejected(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	endIn4h := now.Add(4 * time.Hour)
	markets := []MarketMeta{
		{EventSlug: "nba-x-y-2026-05-10", Title: "X vs Y", ClobTokenIDs: `["a","b"]`,
			Active: bptr(true), Closed: bptr(true), EndDate: &endIn4h},
		{EventSlug: "nba-x-y-2026-05-10", Title: "Spread: X (-1.5)", ClobTokenIDs: `["c","d"]`,
			Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
	}
	f := ExtractFeatures(markets, now)
	d := Decide(f, DefaultConfig(), now)
	if d.Eligible {
		t.Errorf("expected NOT eligible when a market is closed, got eligible")
	}
}

func TestDecide_NonSportRejected(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	endIn4h := now.Add(4 * time.Hour)
	markets := []MarketMeta{
		{EventSlug: "crypto-btc-price-2026-05-10", Title: "Will BTC reach 100k?",
			ClobTokenIDs: `["a","b"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
		{EventSlug: "crypto-btc-price-2026-05-10", Title: "Will BTC reach 90k?",
			ClobTokenIDs: `["c","d"]`, Active: bptr(true), Closed: bptr(false), EndDate: &endIn4h},
	}
	f := ExtractFeatures(markets, now)
	d := Decide(f, DefaultConfig(), now)
	if d.Eligible {
		t.Errorf("expected NOT eligible for non-sport event, got eligible")
	}
}

func TestDecide_FarHorizonRejected(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	endFar := now.Add(30 * 24 * time.Hour)
	markets := []MarketMeta{
		{EventSlug: "nba-x-y-2026-06-09", Title: "X vs Y", ClobTokenIDs: `["a","b"]`,
			Active: bptr(true), Closed: bptr(false), EndDate: &endFar},
		{EventSlug: "nba-x-y-2026-06-09", Title: "Spread: X (-1.5)", ClobTokenIDs: `["c","d"]`,
			Active: bptr(true), Closed: bptr(false), EndDate: &endFar},
	}
	f := ExtractFeatures(markets, now)
	d := Decide(f, DefaultConfig(), now)
	if d.Eligible {
		t.Errorf("expected NOT eligible at 30-day horizon, got eligible")
	}
}
