package grpc

import (
	"context"
	"time"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"google.golang.org/grpc"
)

// DatabaseSessionMiddleware is a middleware to manage database sessions for each gRPC call.
func DatabaseSessionMiddleware(factory db.SessionFactory, txBeginTimeout *time.Duration) grpc.UnaryServerInterceptor {
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

		if metricAttrs := ParseFullMethod(info.FullMethod); metricAttrs != nil {
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
		// Ensure rollback on panic
		defer func() {
			if r := recover(); r != nil {
				if tx := session.GetTxIfExists(); tx != nil {
					if dberr := tx.Rollback(); dberr != nil {
						logger.Error("Failed to rollback transaction", "error", dberr)
					}
				}
				panic(r)
			}
		}()

		// Call the handler (the actual RPC method)
		resp, err := handler(ctx, req)
		// Handle transaction commit/rollback
		if err != nil {
			if tx := session.GetTxIfExists(); tx != nil {
				if dberr := tx.Rollback(); dberr != nil {
					logger.Error("Failed to rollback transaction", "error", dberr)
				}
			}
			return nil, err
		}

		if tx := session.GetTxIfExists(); tx != nil {
			if dberr := tx.Commit(); dberr != nil {
				logger.Error("Failed to commit transaction", "error", dberr)
				return nil, dberr
			}
		}

		return resp, nil
	}
}
