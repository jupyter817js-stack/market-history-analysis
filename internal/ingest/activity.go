package ingest

import (
	"context"
	"fmt"
	"time"

	"rn1-strategy-extractor/internal/normalize"
	"rn1-strategy-extractor/internal/polymarket"
	"rn1-strategy-extractor/internal/store"
	"rn1-strategy-extractor/internal/util"
)

const (
	defaultPageSize  = 500
	defaultMaxOffset = 9500
)

func (o *Orchestrator) IngestActivity(ctx context.Context, identity AccountIdentity, start, end time.Time) error {
	if identity.Effective() == "" {
		return fmt.Errorf("missing proxy/account address")
	}
	o.resetCounters()
	runID, err := o.Store.StartSourceRun(ctx, "polymarket", "activity", identity.Input, identity.ProxyWallet)
	if err != nil {
		return fmt.Errorf("start source run: %w", err)
	}

	runErr := o.ingestActivityRanges(ctx, runID, identity, start, end)
	if ferr := o.finish(ctx, runID, runErr); ferr != nil && runErr == nil {
		return ferr
	}
	return runErr
}

func (o *Orchestrator) ingestActivityRanges(ctx context.Context, runID int64, identity AccountIdentity, start, end time.Time) error {
	if !end.After(start) {
		return fmt.Errorf("end must be after start")
	}
	windows := util.MonthWindows(start, end)
	if len(windows) == 0 {
		windows = []util.TimeWindow{{Start: start, End: end}}
	}
	for _, w := range windows {
		if err := o.ingestActivityWindow(ctx, runID, identity, w); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) ingestActivityWindow(ctx context.Context, runID int64, identity AccountIdentity, w util.TimeWindow) error {
	pageSize := o.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	maxOffset := o.MaxOffsetSafety
	if maxOffset <= 0 {
		maxOffset = defaultMaxOffset
	}
	minWindow := o.MinWindow
	if minWindow <= 0 {
		minWindow = time.Hour
	}

	offset := 0
	for {
		o.bumpRequest()
		page, err := o.Client.GetActivity(ctx, polymarket.ActivityRequest{
			User:          identity.Effective(),
			StartUnix:     w.Start.Unix(),
			EndUnix:       w.End.Unix(),
			Limit:         pageSize,
			Offset:        offset,
			SortBy:        "TIMESTAMP",
			SortDirection: "ASC",
		})
		if err != nil {
			return fmt.Errorf("activity fetch [%s..%s offset=%d]: %w", w.Start.Format(time.RFC3339), w.End.Format(time.RFC3339), offset, err)
		}

		var rawID int64
		if !o.DryRun {
			pageOff := offset
			ws := w.Start
			we := w.End
			rawID, err = o.Store.InsertRawResponse(ctx, store.InsertRawResponse{
				SourceRunID:   runID,
				SourceName:    "polymarket",
				Endpoint:      page.Endpoint,
				RequestURL:    page.Raw.URL,
				RequestParams: page.Params,
				ResponseBody:  page.Raw.Body,
				ResponseHash:  page.Raw.Hash,
				HTTPStatus:    page.Raw.StatusCode,
				FetchedAt:     page.Raw.FetchedAt,
				PageOffset:    &pageOff,
				WindowStart:   &ws,
				WindowEnd:     &we,
			})
			if err != nil {
				return fmt.Errorf("insert raw activity page: %w", err)
			}
			o.bumpRaw()
		}

		for _, raw := range page.Rows {
			row, err := normalize.Activity(normalize.ActivityContext{
				SourceRunID:    runID,
				AccountAddress: identity.Input,
				ProxyWallet:    identity.ProxyWallet,
				RawResponseID:  rawID,
				Location:       o.Location,
			}, raw)
			if err != nil {
				return fmt.Errorf("normalize activity row: %w", err)
			}
			if o.DryRun {
				o.bumpNormalized()
				continue
			}
			ins, err := o.Store.InsertActionIfAbsent(ctx, row)
			if err != nil {
				return fmt.Errorf("insert action: %w", err)
			}
			if ins {
				o.bumpNormalized()
			}
		}

		if len(page.Rows) < pageSize {
			return nil
		}

		nextOffset := offset + pageSize
		if nextOffset >= maxOffset {
			if w.Duration() <= minWindow {
				o.logger().Warn("activity window saturated at min size; marking partial",
					"window_start", w.Start, "window_end", w.End, "offset", offset)
				o.markPartial()
				return nil
			}
			left, right := w.Split()
			o.logger().Info("activity window split",
				"window_start", w.Start, "window_end", w.End,
				"left_start", left.Start, "left_end", left.End,
				"right_start", right.Start, "right_end", right.End)
			if err := o.ingestActivityWindow(ctx, runID, identity, left); err != nil {
				return err
			}
			if err := o.ingestActivityWindow(ctx, runID, identity, right); err != nil {
				return err
			}
			return nil
		}
		offset = nextOffset
	}
}
