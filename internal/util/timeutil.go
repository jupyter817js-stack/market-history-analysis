package util

import (
	"fmt"
	"time"
)

func ParseUnixSeconds(raw int64) time.Time {
	if raw == 0 {
		return time.Time{}
	}
	return time.Unix(raw, 0).UTC()
}

func ToTZ(t time.Time, loc *time.Location) time.Time {
	if t.IsZero() {
		return t
	}
	return t.In(loc)
}

func LoadLocation(name string) (*time.Location, error) {
	if name == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("load location %q: %w", name, err)
	}
	return loc, nil
}

func ParseFlexibleTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time format: %q", s)
}

func MonthWindows(start, end time.Time) []TimeWindow {
	if !end.After(start) {
		return nil
	}
	var out []TimeWindow
	cur := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
	if cur.Before(start) {
		cur = start
	}
	for cur.Before(end) {
		next := time.Date(cur.Year(), cur.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		if next.After(end) {
			next = end
		}
		out = append(out, TimeWindow{Start: cur, End: next})
		cur = next
	}
	return out
}

type TimeWindow struct {
	Start time.Time
	End   time.Time
}

func (w TimeWindow) Duration() time.Duration { return w.End.Sub(w.Start) }

func (w TimeWindow) Split() (TimeWindow, TimeWindow) {
	mid := w.Start.Add(w.End.Sub(w.Start) / 2)
	return TimeWindow{Start: w.Start, End: mid}, TimeWindow{Start: mid, End: w.End}
}
