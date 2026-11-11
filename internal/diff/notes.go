package diff

// ReviewNote returns the closed-list review-note string that
// best matches the given diff-class set. Notes are selected
// deterministically: the first matching rule below wins. No
// free-text generation occurs; every possible return value of
// this function is one of the strings declared as a constant in
// this file.
//
// Design report §H.1 specifies the closed list. The
// TestDiff_ReviewNoteIsTemplated static check (diff_test.go)
// asserts that every emitted note matches one of these
// constants exactly.
func ReviewNote(classes []DiffClass) string {
	set := map[DiffClass]bool{}
	for _, c := range classes {
		set[c] = true
	}

	// Most-specific first.
	switch {
	case set[ClassNewCandidate]:
		return NoteNewlyAppeared

	case set[ClassDroppedCandidate]:
		return NoteDropped

	case set[ClassUnknownResolved] && set[ClassBudgetExhaustedResolved] && set[ClassBecamePass]:
		return NoteBudgetResolvedToPass

	case set[ClassUnknownResolved] && set[ClassBudgetExhaustedResolved] && set[ClassBecameFail]:
		return NoteBudgetResolvedToFail

	case set[ClassUnknownResolved] && set[ClassBudgetExhaustedResolved] && set[ClassBecameWarn]:
		return NoteBudgetResolvedToWarn

	case set[ClassUnknownResolved] && set[ClassBecamePass]:
		return NoteUnknownResolvedToPass

	case set[ClassUnknownResolved] && set[ClassBecameFail]:
		return NoteUnknownResolvedToFail

	case set[ClassUnknownResolved] && set[ClassBecameWarn]:
		return NoteUnknownResolvedToWarn

	case set[ClassBecamePass] && set[ClassGateImproved]:
		return NoteGateImprovedToPass

	case set[ClassBecameFail] && set[ClassGateDegraded]:
		return NoteGateDegradedToFail

	case set[ClassBecameWarn] && set[ClassGateDegraded]:
		return NoteGateDegradedToWarn

	case set[ClassBecameUnknown]:
		return NoteBecameUnknown

	case set[ClassBecameTerminal]:
		return NoteBecameTerminal

	case set[ClassBecameClosed]:
		return NoteBecameClosed

	case set[ClassBecameInactive]:
		return NoteBecameInactive

	case set[ClassSpreadWidened] && !set[ClassGateImproved] && !set[ClassGateDegraded]:
		return NoteSpreadWidened

	case set[ClassSpreadTightened] && !set[ClassGateImproved] && !set[ClassGateDegraded]:
		return NoteSpreadTightened

	case set[ClassDepthDegraded] && !set[ClassGateImproved] && !set[ClassGateDegraded]:
		return NoteDepthDegraded

	case set[ClassDepthImproved] && !set[ClassGateImproved] && !set[ClassGateDegraded]:
		return NoteDepthImproved

	case set[ClassRankUp] && rankUpLarge(classes):
		return NoteSignificantRankUp

	case set[ClassHumanReviewStillReq]:
		return NoteStillRequiresReview

	case set[ClassNoMaterialChange]:
		return NoteNoMaterialChange
	}

	// Catch-all when the class set is non-trivial but doesn't
	// match any specific template above. Stays observational.
	return NoteSeeDiffClasses
}

// rankUpLarge is a small helper for the SignificantRankUp note —
// the caller of ReviewNote may set this expectation; for Phase 1
// we treat any RANK_UP as "significant" when no other class
// fired (i.e. the rank-up is the only thing notable).
func rankUpLarge(classes []DiffClass) bool {
	for _, c := range classes {
		if c == ClassRankUp {
			continue
		}
		if c == ClassNoMaterialChange {
			continue
		}
		// Any other class means "the rank-up isn't the headline."
		return false
	}
	return true
}

// Closed-list review-note constants. The static check in
// diff_test.go (TestDiff_ReviewNoteIsTemplated) asserts that
// every emitted ReviewNote value is one of these.
const (
	NoteNewlyAppeared = "Candidate newly appeared in the later snapshot."

	NoteDropped = "Candidate dropped from the later snapshot."

	NoteBudgetResolvedToPass = "Re-scan resolved prior UNKNOWN gate (budget exhaustion lifted). Liquidity gate is now PASS."

	NoteBudgetResolvedToFail = "Budget exhaustion resolved; candidate now classified as FAIL after sufficient coverage."

	NoteBudgetResolvedToWarn = "Budget exhaustion resolved; liquidity gate is now WARN."

	NoteUnknownResolvedToPass = "Books became observable between snapshots; gate moved UNKNOWN to PASS."

	NoteUnknownResolvedToFail = "Books became observable between snapshots; gate moved UNKNOWN to FAIL."

	NoteUnknownResolvedToWarn = "Books became observable between snapshots; gate moved UNKNOWN to WARN."

	NoteGateImprovedToPass = "Liquidity gate improved to PASS."

	NoteGateDegradedToFail = "Liquidity gate degraded to FAIL."

	NoteGateDegradedToWarn = "Liquidity gate degraded to WARN."

	NoteBecameUnknown = "Liquidity gate moved to UNKNOWN in the later snapshot."

	NoteBecameTerminal = "Event passed horizon between snapshots."

	NoteBecameClosed = "Event closed between snapshots."

	NoteBecameInactive = "Event became inactive between snapshots."

	NoteSpreadWidened = "Average spread widened; keep on human review."

	NoteSpreadTightened = "Average spread tightened; gate unchanged."

	NoteDepthImproved = "Top-3 depth improved; gate unchanged."

	NoteDepthDegraded = "Top-3 depth degraded; gate unchanged."

	NoteSignificantRankUp = "Significantly higher rank in the later snapshot."

	NoteStillRequiresReview = "Candidate still requires human review; no other material change."

	NoteNoMaterialChange = "No material change."

	NoteSeeDiffClasses = "See diff_classes for the observed changes."
)

// AllReviewNotes is the closed set of every note string the
// diff may emit. Used by TestDiff_ReviewNoteIsTemplated.
var AllReviewNotes = []string{
	NoteNewlyAppeared,
	NoteDropped,
	NoteBudgetResolvedToPass,
	NoteBudgetResolvedToFail,
	NoteBudgetResolvedToWarn,
	NoteUnknownResolvedToPass,
	NoteUnknownResolvedToFail,
	NoteUnknownResolvedToWarn,
	NoteGateImprovedToPass,
	NoteGateDegradedToFail,
	NoteGateDegradedToWarn,
	NoteBecameUnknown,
	NoteBecameTerminal,
	NoteBecameClosed,
	NoteBecameInactive,
	NoteSpreadWidened,
	NoteSpreadTightened,
	NoteDepthImproved,
	NoteDepthDegraded,
	NoteSignificantRankUp,
	NoteStillRequiresReview,
	NoteNoMaterialChange,
	NoteSeeDiffClasses,
}
