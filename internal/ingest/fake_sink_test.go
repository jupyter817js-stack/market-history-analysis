package ingest

import (
	"context"
	"errors"
	"sync"
	"time"

	"rn1-strategy-extractor/internal/store"
)

type fakeSink struct {
	mu        sync.Mutex
	nextRunID int64
	nextRawID int64
	runs      map[int64]string
	raws      []store.InsertRawResponse
	actions   map[string]store.ActionRow // by fingerprint
	curPos    []store.PositionCurrentRow
	closedPos []store.PositionClosedRow
	markets   map[string]store.MarketBasicRow
	accounts  map[string]struct {
		ProxyWallet, DisplayName string
		CreatedAt                *time.Time
		ProfileRaw               []byte
	}
}

func newFakeSink() *fakeSink {
	return &fakeSink{
		runs:    map[int64]string{},
		actions: map[string]store.ActionRow{},
		markets: map[string]store.MarketBasicRow{},
		accounts: map[string]struct {
			ProxyWallet, DisplayName string
			CreatedAt                *time.Time
			ProfileRaw               []byte
		}{},
	}
}

func (f *fakeSink) StartSourceRun(_ context.Context, sourceName, runType, accountAddress, proxyWallet string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextRunID++
	f.runs[f.nextRunID] = "running"
	return f.nextRunID, nil
}

func (f *fakeSink) FinishSourceRun(_ context.Context, runID int64, status, errMsg string, reqCount, rawCount, normCount int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs[runID] = status
	return nil
}

func (f *fakeSink) InsertRawResponse(_ context.Context, in store.InsertRawResponse) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextRawID++
	f.raws = append(f.raws, in)
	return f.nextRawID, nil
}

func (f *fakeSink) UpsertAccount(_ context.Context, accountAddress, proxyWallet, displayName string, createdAtSource *time.Time, profileRaw []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts[accountAddress] = struct {
		ProxyWallet, DisplayName string
		CreatedAt                *time.Time
		ProfileRaw               []byte
	}{proxyWallet, displayName, createdAtSource, profileRaw}
	return nil
}

func (f *fakeSink) InsertActionIfAbsent(_ context.Context, r store.ActionRow) (bool, error) {
	if r.Fingerprint == "" {
		return false, errors.New("empty fingerprint")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.actions[r.Fingerprint]; ok {
		return false, nil
	}
	f.actions[r.Fingerprint] = r
	return true, nil
}

func (f *fakeSink) InsertPositionCurrent(_ context.Context, r store.PositionCurrentRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.curPos = append(f.curPos, r)
	return nil
}

func (f *fakeSink) InsertPositionClosed(_ context.Context, r store.PositionClosedRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closedPos = append(f.closedPos, r)
	return nil
}

func (f *fakeSink) UpsertMarketBasic(_ context.Context, r store.MarketBasicRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markets[r.ConditionID] = r
	return nil
}

func (f *fakeSink) TouchedConditionIDs(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	set := map[string]struct{}{}
	for _, r := range f.actions {
		if r.ConditionID != "" {
			set[r.ConditionID] = struct{}{}
		}
	}
	for _, p := range f.curPos {
		if p.ConditionID != "" {
			set[p.ConditionID] = struct{}{}
		}
	}
	for _, p := range f.closedPos {
		if p.ConditionID != "" {
			set[p.ConditionID] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out, nil
}

func (f *fakeSink) TouchedEventSlugs(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	set := map[string]struct{}{}
	for _, r := range f.actions {
		if r.EventSlug != "" {
			set[r.EventSlug] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out, nil
}

func (f *fakeSink) TouchedEventSlugsWithoutCondition(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	set := map[string]struct{}{}
	for _, r := range f.actions {
		if r.EventSlug != "" && r.ConditionID == "" {
			set[r.EventSlug] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out, nil
}
