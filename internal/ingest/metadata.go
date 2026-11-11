package ingest

import (
	"context"
	"fmt"

	"rn1-strategy-extractor/internal/normalize"
	"rn1-strategy-extractor/internal/polymarket"
	"rn1-strategy-extractor/internal/store"
)

// Gamma /events accepts ?slug=A&slug=B... multi-value queries. We send 50
// slugs per request to stay well below practical URL length limits given
// that slugs can be 30-60+ chars each.
const eventsSlugBatchSize = 50

func (o *Orchestrator) IngestMetadata(ctx context.Context, identity AccountIdentity) error {
	o.resetCounters()
	runID, err := o.Store.StartSourceRun(ctx, "polymarket", "metadata", identity.Input, identity.ProxyWallet)
	if err != nil {
		return fmt.Errorf("start source run: %w", err)
	}
	runErr := func() error {
		slugs, err := o.Store.TouchedEventSlugs(ctx)
		if err != nil {
			return fmt.Errorf("touched event slugs: %w", err)
		}
		o.logger().Info("metadata: touched event_slugs", "count", len(slugs))
		for _, batch := range chunkStrings(slugs, eventsSlugBatchSize) {
			if err := o.enrichEventBatch(ctx, runID, batch); err != nil {
				return err
			}
		}
		return nil
	}()
	if ferr := o.finish(ctx, runID, runErr); ferr != nil && runErr == nil {
		return ferr
	}
	return runErr
}

// enrichEventBatch queries Gamma /events with multi-value slug params. Each
// event response includes a nested markets[] array; every market with a
// non-empty conditionId is upserted into markets_basic.
func (o *Orchestrator) enrichEventBatch(ctx context.Context, runID int64, slugs []string) error {
	o.bumpRequest()
	page, err := o.Client.GetEventsByMultiSlug(ctx, slugs, 100)
	if err != nil {
		return fmt.Errorf("events multi-slug: %w", err)
	}
	if !o.DryRun {
		_, err := o.Store.InsertRawResponse(ctx, store.InsertRawResponse{
			SourceRunID:   runID,
			SourceName:    "polymarket",
			Endpoint:      page.Endpoint,
			RequestURL:    page.Raw.URL,
			RequestParams: page.Params,
			ResponseBody:  page.Raw.Body,
			ResponseHash:  page.Raw.Hash,
			HTTPStatus:    page.Raw.StatusCode,
			FetchedAt:     page.Raw.FetchedAt,
		})
		if err != nil {
			return fmt.Errorf("insert raw events: %w", err)
		}
		o.bumpRaw()
	}
	for _, raw := range page.Rows {
		marketRows, err := normalize.MarketsFromEvent(raw)
		if err != nil {
			return fmt.Errorf("normalize event-nested markets: %w", err)
		}
		for _, row := range marketRows {
			if o.DryRun {
				o.bumpNormalized()
				continue
			}
			if err := o.Store.UpsertMarketBasic(ctx, row); err != nil {
				return fmt.Errorf("upsert markets_basic: %w", err)
			}
			o.bumpNormalized()
		}
	}
	return nil
}

func chunkStrings(in []string, size int) [][]string {
	if size <= 0 {
		size = 1
	}
	var out [][]string
	for i := 0; i < len(in); i += size {
		j := i + size
		if j > len(in) {
			j = len(in)
		}
		out = append(out, in[i:j])
	}
	return out
}

// Deprecated legacy path retained for documentation only; not called.
//
// keyset endpoints (/markets/keyset?condition_ids=..., /events/keyset?slug=...)
// return empty arrays in production even for known live identifiers, so the
// orchestrator no longer uses them. The active enrichment path is
// enrichEventBatch above. The Gamma client methods GetMarketsKeyset /
// GetEventsKeyset are still exposed in internal/polymarket for tests and any
// future use.
var _ = polymarket.MarketsKeysetRequest{}
