package main

import (
	"context"
	"time"

	"github.com/lightsparkdev/spark/so/ent"
	"go.uber.org/zap"
)

// waitForDatabaseReady attempts to start and roll back a transaction until it succeeds or the context is done.
func waitForDatabaseReady(ctx context.Context, client *ent.Client, logger *zap.Logger) error {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := func() error {
			tx, err := client.Tx(checkCtx)
			if err != nil {
				return err
			}
			_ = tx.Rollback()
			return nil
		}()
		cancel()

		if err == nil {
			return nil
		}

		logger.Warn("Database readiness check failed, retrying", zap.Error(err))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		if backoff < 5*time.Second {
			backoff *= 2
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
		}
	}
}
