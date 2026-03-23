package grpc

import (
	"context"
	"fmt"
	"time"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/entephemeral"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

var readinessService = "spark.SparkService"

type dbTx interface {
	Rollback() error
}

type dbClient[T dbTx] interface {
	Tx(context.Context) (T, error)
}

func NewHealthServer(ctx context.Context, dbClient *ent.Client, ephemeralDBClient *entephemeral.Client) *health.Server {
	healthServer := health.NewServer()

	// "" service is used for liveness checks; it always returns SERVING.
	healthServer.SetServingStatus(
		"",
		grpc_health_v1.HealthCheckResponse_SERVING,
	)

	// "spark.SparkService" is used for readiness checks. It will be initialized to
	// `NOT_SERVING` and set to `SERVING` once the server is ready to accept requests.
	healthServer.SetServingStatus(
		readinessService,
		grpc_health_v1.HealthCheckResponse_NOT_SERVING,
	)

	go func() {
		logger := logging.GetLoggerFromContext(ctx)
		readinessChecks := []func(context.Context) error{
			func(readinessCtx context.Context) error {
				return waitForDatabaseReady(readinessCtx, "main", dbClient)
			},
		}
		if ephemeralDBClient != nil {
			readinessChecks = append(readinessChecks, func(readinessCtx context.Context) error {
				return waitForDatabaseReady(readinessCtx, "ephemeral", ephemeralDBClient)
			})
		}

		if err := waitForDatabasesReady(ctx, readinessChecks...); err != nil {
			logger.With(zap.Error(err)).Warn("Database readiness checks stopped before completion")
			return
		}

		healthServer.SetServingStatus(
			readinessService,
			grpc_health_v1.HealthCheckResponse_SERVING,
		)
	}()

	return healthServer
}

func waitForDatabasesReady(ctx context.Context, checks ...func(context.Context) error) error {
	// errgroup fail-fast behavior is intentional here: if any readiness loop exits with
	// error (including cancellation), we stop the readiness goroutine and keep NOT_SERVING.
	errGroup, readinessCtx := errgroup.WithContext(ctx)
	for _, check := range checks {
		errGroup.Go(func() error {
			return check(readinessCtx)
		})
	}

	return errGroup.Wait()
}

func waitForDatabaseReady[T dbTx](ctx context.Context, databaseName string, client dbClient[T]) error {
	logger := logging.GetLoggerFromContext(ctx)
	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return fmt.Errorf("%s database readiness check canceled: %w", databaseName, ctx.Err())
		}

		err := func() error {
			checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			tx, err := client.Tx(checkCtx)
			if err != nil {
				return err
			}
			_ = tx.Rollback()
			return nil
		}()

		if err == nil {
			return nil
		}

		logger.With(zap.Error(err)).Sugar().Warnf("Database readiness check failed for %s, retrying in %s...", databaseName, backoff)

		select {
		case <-ctx.Done():
			return fmt.Errorf("%s database readiness check canceled: %w", databaseName, ctx.Err())
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
