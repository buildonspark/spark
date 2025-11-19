package grpc

import (
	"context"
	"time"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	"go.uber.org/zap"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

var readinessService = "spark.SparkService"

func NewHealthServer(ctx context.Context, dbClient *ent.Client) *health.Server {
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
		waitForDatabaseReady(ctx, dbClient)
		healthServer.SetServingStatus(
			readinessService,
			grpc_health_v1.HealthCheckResponse_SERVING,
		)
	}()

	return healthServer
}

func waitForDatabaseReady(ctx context.Context, client *ent.Client) {
	logger := logging.GetLoggerFromContext(ctx)
	backoff := time.Second

	for {
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
			return
		}

		logger.With(zap.Error(err)).Sugar().Warnf("Database readiness check failed, retrying in %s...", backoff)

		select {
		case <-ctx.Done():
			return
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
