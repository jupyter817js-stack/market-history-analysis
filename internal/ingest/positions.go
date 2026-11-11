package ingest

import (
	"context"
	"fmt"

	"rn1-strategy-extractor/internal/normalize"
	"rn1-strategy-extractor/internal/polymarket"
	"rn1-strategy-extractor/internal/store"
)

func (o *Orchestrator) IngestPositions(ctx context.Context, identity AccountIdentity) error {
	if identity.Effective() == "" {
		return fmt.Errorf("missing proxy/account address")
	}
	o.resetCounters()
	runID, err := o.Store.StartSourceRun(ctx, "polymarket", "positions", identity.Input, identity.ProxyWallet)
	if err != nil {
		return fmt.Errorf("start source run: %w", err)
	}
	runErr := func() error {
		zero := float64(0)
		offset := 0
		for {
			o.bumpRequest()
			page, err := o.Client.GetPositions(ctx, polymarket.PositionsRequest{
				User:          identity.Effective(),
				SizeThreshold: &zero,
				Limit:         500,
				Offset:        offset,
				SortBy:        "TITLE",
				SortDirection: "ASC",
			})
			if err != nil {
				return fmt.Errorf("positions fetch offset=%d: %w", offset, err)
			}
			var rawID int64
			if !o.DryRun {
				po := offset
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
					PageOffset:    &po,
				})
				if err != nil {
					return fmt.Errorf("insert raw positions page: %w", err)
				}
				o.bumpRaw()
			}
			for _, raw := range page.Rows {
				row, err := normalize.PositionCurrent(normalize.PositionContext{
					SourceRunID:    runID,
					AccountAddress: identity.Input,
					ProxyWallet:    identity.ProxyWallet,
					RawResponseID:  rawID,
				}, raw)
				if err != nil {
					return fmt.Errorf("normalize current position: %w", err)
				}
				if o.DryRun {
					o.bumpNormalized()
					continue
				}
				if err := o.Store.InsertPositionCurrent(ctx, row); err != nil {
					return fmt.Errorf("insert current position: %w", err)
				}
				o.bumpNormalized()
			}
			if len(page.Rows) < 500 {
				return nil
			}
			next := offset + 500
			if next >= 10000 {
				o.logger().Warn("positions paging reached docs cap; marking partial", "offset", next)
				o.markPartial()
				return nil
			}
			offset = next
		}
	}()
	if ferr := o.finish(ctx, runID, runErr); ferr != nil && runErr == nil {
		return ferr
	}
	return runErr
}

func (o *Orchestrator) IngestClosedPositions(ctx context.Context, identity AccountIdentity) error {
	if identity.Effective() == "" {
		return fmt.Errorf("missing proxy/account address")
	}
	o.resetCounters()
	runID, err := o.Store.StartSourceRun(ctx, "polymarket", "closed_positions", identity.Input, identity.ProxyWallet)
	if err != nil {
		return fmt.Errorf("start source run: %w", err)
	}
	runErr := func() error {
		offset := 0
		const pageLimit = 50
		const maxOffset = 100000
		for {
			o.bumpRequest()
			page, err := o.Client.GetClosedPositions(ctx, polymarket.ClosedPositionsRequest{
				User:          identity.Effective(),
				Limit:         pageLimit,
				Offset:        offset,
				SortBy:        "TIMESTAMP",
				SortDirection: "ASC",
			})
			if err != nil {
				return fmt.Errorf("closed positions fetch offset=%d: %w", offset, err)
			}
			var rawID int64
			if !o.DryRun {
				po := offset
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
					PageOffset:    &po,
				})
				if err != nil {
					return fmt.Errorf("insert raw closed-positions page: %w", err)
				}
				o.bumpRaw()
			}
			for _, raw := range page.Rows {
				row, err := normalize.PositionClosed(normalize.PositionContext{
					SourceRunID:    runID,
					AccountAddress: identity.Input,
					ProxyWallet:    identity.ProxyWallet,
					RawResponseID:  rawID,
				}, raw)
				if err != nil {
					return fmt.Errorf("normalize closed position: %w", err)
				}
				if o.DryRun {
					o.bumpNormalized()
					continue
				}
				if err := o.Store.InsertPositionClosed(ctx, row); err != nil {
					return fmt.Errorf("insert closed position: %w", err)
				}
				o.bumpNormalized()
			}
			if len(page.Rows) < pageLimit {
				return nil
			}
			next := offset + pageLimit
			if next >= maxOffset {
				o.logger().Warn("closed positions paging reached docs cap; marking partial", "offset", next)
				o.markPartial()
				return nil
			}
			offset = next
		}
	}()
	if ferr := o.finish(ctx, runID, runErr); ferr != nil && runErr == nil {
		return ferr
	}
	return runErr
}
