package ingest

import (
	"context"
	"fmt"
	"time"
)

func (o *Orchestrator) IngestAll(ctx context.Context, accountAddress string, start, end time.Time) error {
	identity, err := o.IngestProfile(ctx, accountAddress)
	if err != nil {
		return fmt.Errorf("profile: %w", err)
	}
	if err := o.IngestActivity(ctx, identity, start, end); err != nil {
		return fmt.Errorf("activity: %w", err)
	}
	if err := o.IngestPositions(ctx, identity); err != nil {
		return fmt.Errorf("positions: %w", err)
	}
	if err := o.IngestClosedPositions(ctx, identity); err != nil {
		return fmt.Errorf("closed-positions: %w", err)
	}
	if err := o.IngestMetadata(ctx, identity); err != nil {
		return fmt.Errorf("metadata: %w", err)
	}
	return nil
}
