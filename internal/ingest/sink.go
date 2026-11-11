package ingest

import (
	"context"
	"time"

	"rn1-strategy-extractor/internal/store"
)

type Sink interface {
	StartSourceRun(ctx context.Context, sourceName, runType, accountAddress, proxyWallet string) (int64, error)
	FinishSourceRun(ctx context.Context, runID int64, status, errMsg string, reqCount, rawCount, normCount int) error
	InsertRawResponse(ctx context.Context, in store.InsertRawResponse) (int64, error)
	UpsertAccount(ctx context.Context, accountAddress, proxyWallet, displayName string, createdAtSource *time.Time, profileRaw []byte) error
	InsertActionIfAbsent(ctx context.Context, r store.ActionRow) (bool, error)
	InsertPositionCurrent(ctx context.Context, r store.PositionCurrentRow) error
	InsertPositionClosed(ctx context.Context, r store.PositionClosedRow) error
	UpsertMarketBasic(ctx context.Context, r store.MarketBasicRow) error
	TouchedConditionIDs(ctx context.Context) ([]string, error)
	TouchedEventSlugs(ctx context.Context) ([]string, error)
	TouchedEventSlugsWithoutCondition(ctx context.Context) ([]string, error)
}

var _ Sink = (*store.Store)(nil)
