package validation

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// HistoricalProfile is the read-only summary of RN1's historical
// cohort distribution that the likeness scorer compares against.
// It is built once by a separate `build-historical-profile`
// command (which has DB access) and serialized to a JSON file
// loaded at runtime by every `rn1-validate tick` call.
//
// The validation package itself never touches the DB. This
// separation keeps the Phase F-0 runtime path entirely network-
// free (and DB-free) -- a hard requirement for read-only live
// operation.
type HistoricalProfile struct {
	// Provenance.
	GeneratedAt          time.Time `json:"generated_at"`
	BuilderVersion       string    `json:"builder_version"`
	SourceDatasetSummary string    `json:"source_dataset_summary"`

	// Top-level cohort sizing.
	TotalClusters                int `json:"total_clusters"`
	Rn1PositiveEnvelopeClusters  int `json:"rn1_positive_envelope_clusters"`
	Rn1TouchedConditions         int `json:"rn1_touched_conditions"`

	// Sport prefix recognition. The likeness scorer's A axis
	// fires 1.0 for any prefix in this set, 0.0 otherwise.
	// Should match selection.SportPrefixes at profile build
	// time; runtime drift is acceptable but logged by the
	// scorer's consistency check.
	Rn1KnownPrefixes []string `json:"rn1_known_prefixes"`

	// Family detection frequency over RN1-touched events.
	// Likeness B axis only checks membership; this map is
	// informational for the evaluation rubric (family-level
	// PnL breakdown).
	Rn1FamilyDistribution map[string]float64 `json:"rn1_family_distribution"`

	// Selector score distribution over RN1-touched events.
	// Used by the evaluation rubric (criterion 7 KS-test).
	// Keys are score bucket labels ("5.0", "7.5", "9.5", ...).
	Rn1ScoreDistribution map[string]float64 `json:"rn1_score_distribution"`

	// Bundle size quantiles over RN1-touched events.
	Rn1BundleSizeP10 int `json:"rn1_bundle_size_p10"`
	Rn1BundleSizeP50 int `json:"rn1_bundle_size_p50"`
	Rn1BundleSizeP90 int `json:"rn1_bundle_size_p90"`

	// The five forensic Layer-5 reference clusters. The likeness
	// scorer sanity-test (TestLikeness_FiveForensicClusters)
	// asserts each prototype scores >= 0.75.
	Rn1Layer5Prototypes []Layer5Prototype `json:"rn1_layer5_prototypes"`
}

// Layer5Prototype is one of the five forensic-audited RN1
// clusters from rn1-validation-pass-1-2026-05-12.md §C. Used at
// design time to sanity-check the likeness scorer; not consumed
// at runtime by tick/evaluate.
type Layer5Prototype struct {
	EventSlug        string   `json:"event_slug"`
	SportPrefix      string   `json:"sport_prefix"`
	Families         []string `json:"families"`
	BinaryCount      int      `json:"binary_count"`
	Score            float64  `json:"score"`
	BuyUsdc          float64  `json:"buy_usdc"`
	RealizedPnlUsdc  float64  `json:"realized_pnl_usdc"`
	HorizonHours     int      `json:"horizon_hours"`
	LiquidityGate    string   `json:"liquidity_gate"`
	OutcomeSign      string   `json:"outcome_sign"` // "POSITIVE" / "NEGATIVE" / "IN_FLIGHT"
}

// LoadProfile reads a HistoricalProfile from a JSON file. The
// loader does NOT validate that the source data is fresh; the
// `generated_at` field is exposed so consumers can decide.
//
// Returns a non-nil error if the file is missing, unparseable,
// or fails minimal structural validation.
func LoadProfile(path string) (*HistoricalProfile, error) {
	if path == "" {
		return nil, fmt.Errorf("historical profile path is required")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read historical profile %q: %w", path, err)
	}
	// Strip a UTF-8 BOM if the build tool wrote one (PowerShell
	// 5.1 default).
	if len(body) >= 3 && body[0] == 0xEF && body[1] == 0xBB && body[2] == 0xBF {
		body = body[3:]
	}
	var p HistoricalProfile
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("decode historical profile %q: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("validate historical profile %q: %w", path, err)
	}
	return &p, nil
}

// Validate performs structural sanity checks on a freshly loaded
// profile. It does NOT enforce a freshness cutoff -- operators
// decide that.
func (p *HistoricalProfile) Validate() error {
	if p.GeneratedAt.IsZero() {
		return fmt.Errorf("generated_at is required")
	}
	if len(p.Rn1KnownPrefixes) == 0 {
		return fmt.Errorf("rn1_known_prefixes must not be empty")
	}
	if p.TotalClusters <= 0 {
		return fmt.Errorf("total_clusters must be positive (got %d)", p.TotalClusters)
	}
	if p.Rn1BundleSizeP50 <= 0 {
		return fmt.Errorf("rn1_bundle_size_p50 must be positive")
	}
	// Family distribution should sum to approximately 1.0; allow
	// small floating-point slack.
	sum := 0.0
	for _, v := range p.Rn1FamilyDistribution {
		sum += v
	}
	if sum > 0 && (sum < 0.95 || sum > 1.05) {
		return fmt.Errorf("rn1_family_distribution should sum to ~1.0; got %.3f", sum)
	}
	// At least one Layer-5 prototype is required (the scorer
	// sanity test reads them).
	if len(p.Rn1Layer5Prototypes) == 0 {
		return fmt.Errorf("at least one rn1_layer5_prototype is required")
	}
	return nil
}

// HasKnownPrefix reports whether a sport prefix is in the profile's
// recognized set. Used by the likeness scorer A axis.
func (p *HistoricalProfile) HasKnownPrefix(prefix string) bool {
	for _, x := range p.Rn1KnownPrefixes {
		if x == prefix {
			return true
		}
	}
	return false
}

// KnownPrefixesSorted returns the prefix set in sorted order. Used
// only by report rendering; never affects scoring.
func (p *HistoricalProfile) KnownPrefixesSorted() []string {
	out := append([]string(nil), p.Rn1KnownPrefixes...)
	sort.Strings(out)
	return out
}
