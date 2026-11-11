package shadow

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ReadLog reads every record from a JSONL shadow log. Missing
// files are NOT an error — they return an empty slice (this is
// how a fresh `add` on a new path is supported). Malformed lines
// are an error and abort the read with a clear location.
//
// The function makes no network call and never mutates the
// file.
func ReadLog(path string) ([]ShadowRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open shadow log %q: %w", path, err)
	}
	defer f.Close()

	var out []ShadowRecord
	scanner := bufio.NewScanner(f)
	// Allow large JSONL records (history fields can grow).
	buf := make([]byte, 0, 1<<16)
	scanner.Buffer(buf, 1<<24) // up to 16 MiB per line
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec ShadowRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("shadow log %q line %d: %w", path, lineNo, err)
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan shadow log %q: %w", path, err)
	}
	return out, nil
}

// AppendLog appends one record to the shadow log. Creates the
// parent directory and the file if missing. The function never
// rewrites existing bytes — it opens with O_APPEND only. The
// TestShadow_AppendOnlyInvariant test asserts that prior bytes
// of the file are byte-identical before and after each call.
func AppendLog(path string, rec ShadowRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of shadow log %q: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open shadow log for append %q: %w", path, err)
	}
	defer f.Close()

	body, err := json.Marshal(&rec)
	if err != nil {
		return fmt.Errorf("marshal shadow record: %w", err)
	}
	if _, err := f.Write(body); err != nil {
		return fmt.Errorf("write shadow record: %w", err)
	}
	if _, err := f.Write([]byte{'\n'}); err != nil {
		return fmt.Errorf("write shadow record newline: %w", err)
	}
	return nil
}

// LatestByShadowID returns the most recent record per shadow_id
// in the order they appear in the log (last write wins).
func LatestByShadowID(records []ShadowRecord) map[string]ShadowRecord {
	out := map[string]ShadowRecord{}
	for _, r := range records {
		out[r.ShadowID] = r
	}
	return out
}

// LatestByEventSlug is the convenience accessor most update
// paths actually want.
func LatestByEventSlug(records []ShadowRecord) map[string]ShadowRecord {
	out := map[string]ShadowRecord{}
	for _, r := range records {
		out[r.EventSlug] = r
	}
	return out
}

// CopyFile is a small helper used by tests to take a "before"
// snapshot of the log for the append-only invariant check.
// Kept private to the package by lowercase name would be ideal,
// but exposed so the test in the same package can reuse it.
// Not used by add / update / report paths.
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
