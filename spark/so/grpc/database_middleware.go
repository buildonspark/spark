package grpc

import (
	"context"
	"fmt"
	"time"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/grpcutil"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// DatabaseSessionMiddleware is a middleware to manage database sessions for each gRPC call.
func DatabaseSessionMiddleware(dbClient *ent.Client, factory db.SessionFactory, txBeginTimeout *time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info != nil &&
			(info.FullMethod == "/grpc.health.v1.Health/Check") {
			return handler(ctx, req)
		}

		logger := logging.GetLoggerFromContext(ctx)

		opts := []db.SessionOption{}
		if txBeginTimeout != nil {
			opts = append(opts, db.WithTxBeginTimeout(*txBeginTimeout))
		}

		if metricAttrs := grpcutil.ParseFullMethod(info.FullMethod); metricAttrs != nil {
			opts = append(opts, db.WithMetricAttributes(metricAttrs))
		}

		sessionCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		// Start a transaction or session
		session := factory.NewSession(
			sessionCtx,
			opts...,
		)

		// Attach the transaction to the context
		ctx = ent.Inject(ctx, session)
		ctx = ent.InjectNotifier(ctx, session)

		// Ensure rollback on panic
		defer func() {
			if r := recover(); r != nil {
				if tx := session.GetTxIfExists(); tx != nil {
					if dberr := tx.Rollback(); dberr != nil {
						logger.Error("Failed to rollback transaction", zap.Error(dberr))
					}
				}
				panic(r)
			}
		}()

		// Call the handler (the actual RPC method)
		resp, err := handler(ctx, req)

		if tx := session.GetTxIfExists(); tx != nil {
			// nolint:errcheck
			defer tx.Rollback() // Safe to call, will be a no-op if already committed or rolled back.

			if err == nil {
				dberr := tx.Commit()
				if dberr != nil {
					return nil, fmt.Errorf("failed to commit transaction: %w", dberr)
				}
			}
		}

		return resp, err
	}
}
