package runtime

import (
	"context"
	"log"
	"time"
)

// Sweeper removes expired files and returns the number of processed records.
type Sweeper interface {
	SweepExpired(ctx context.Context, limit int) (int, error)
}

// StartSweeper starts a background ticker that periodically removes expired files.
func StartSweeper(
	ctx context.Context,
	logger *log.Logger,
	interval time.Duration,
	batchSize int,
	sweeper Sweeper,
) {
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				count, err := sweeper.SweepExpired(ctx, batchSize)
				if err != nil {
					logger.Printf("expiration sweep failed: %v", err)
					continue
				}
				if count > 0 {
					logger.Printf("expiration sweep removed %d file(s)", count)
				}
			}
		}
	}()
}
