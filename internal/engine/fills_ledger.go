package engine

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxAcceptableFillLag mirrors the F-0 validation tracker's
// MaxAcceptableTickLag (10 min). Two cycles racing to append into
// the same fills ledger can produce small temporal disagreements;
// real clock drift produces hour-scale disagreements. The relaxed
// guard accepts the former and rejects the latter.
const MaxAcceptableFillLag = 10 * time.Minute

// AppendFill writes a single PaperFill row to the fills JSONL
// ledger. The ledger is append-only.
//
// Chronology guard (relaxed 2026-05-16): a fill whose ObservedAt
// is MORE than MaxAcceptableFillLag before the prior last row is
// rejected as likely clock drift. Smaller lags are accepted; the
// downstream BuildPaperLegs sorts fills chronologically.
//
// Caller is responsible for ensuring the parent directory exists.
func AppendFill(ledgerPath string, fill PaperFill) error {
	if ledgerPath == "" {
		return fmt.Errorf("ledger path required")
	}
	if fill.EventSlug == "" || fill.ConditionID == "" {
		return fmt.Errorf("fill requires event_slug + condition_id")
	}
	if fill.Side != "a" && fill.Side != "b" {
		return fmt.Errorf("fill side must be \"a\" or \"b\" (got %q)", fill.Side)
	}
	if fill.ObservedAt.IsZero() {
		return fmt.Errorf("fill requires observed_at")
	}
	if !IsValidPhase(fill.Phase) {
		return fmt.Errorf("fill phase %q is not a valid closed-enum PhaseLabel", fill.Phase)
	}

	// Relaxed chronology guard.
	if last, ok, err := lastFillTime(ledgerPath); err != nil {
		return err
	} else if ok && fill.ObservedAt.Before(last) {
		lag := last.Sub(fill.ObservedAt)
		if lag > MaxAcceptableFillLag {
			return fmt.Errorf(
				"fill observed_at %s precedes prior last fill %s by %v (> %v, clock drift?)",
				fill.ObservedAt.Format(time.RFC3339), last.Format(time.RFC3339),
				lag, MaxAcceptableFillLag)
		}
		// Within tolerance — accept; BuildPaperLegs will sort.
	}

	body, err := json.Marshal(fill)
	if err != nil {
		return fmt.Errorf("marshal fill: %w", err)
	}
	f, err := os.OpenFile(ledgerPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open ledger %q: %w", ledgerPath, err)
	}
	defer f.Close()
	if _, err := f.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("append fill: %w", err)
	}
	return nil
}

// ReadFills loads every PaperFill from the JSONL ledger in append
// order. Returns nil slice for missing or empty file.
func ReadFills(ledgerPath string) ([]PaperFill, error) {
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
	var out []PaperFill
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var f PaperFill
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			return nil, fmt.Errorf("decode fill: %w", err)
		}
		out = append(out, f)
	}
	return out, nil
}

func lastFillTime(ledgerPath string) (time.Time, bool, error) {
	st, err := os.Stat(ledgerPath)
	if os.IsNotExist(err) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	if st.Size() == 0 {
		return time.Time{}, false, nil
	}
	f, err := os.Open(ledgerPath)
	if err != nil {
		return time.Time{}, false, err
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
		var fill PaperFill
		if err := json.Unmarshal([]byte(line), &fill); err != nil {
			return time.Time{}, false, fmt.Errorf("decode fill in tail scan: %w", err)
		}
		last = fill.ObservedAt
		have = true
	}
	if err := scanner.Err(); err != nil {
		return time.Time{}, false, err
	}
	return last, have, nil
}

// EnsureFillsDir creates the parent directory of the fills ledger
// if it does not yet exist. Helper for CLI callers.
func EnsureFillsDir(ledgerPath string) error {
	dir := filepath.Dir(ledgerPath)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0755)
}
