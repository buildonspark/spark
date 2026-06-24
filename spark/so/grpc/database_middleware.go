package grpc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/entephemeral"
	"github.com/lightsparkdev/spark/so/grpcutil"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/partner"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// DatabaseSessionMiddleware manages per-request main and ephemeral DB sessions.
// On successful handler execution, ephemeral commits are attempted before main commits.
// If ephemeral commit fails, this interceptor returns an error even when the handler returned
// success, and the handler response is discarded to avoid acknowledging a request that did not
// durably persist required ephemeral state.
//
// Handlers must be idempotent: if the ephemeral commit fails after handler execution, the gRPC
// error returned to the client may trigger a retry that re-executes the handler with all its
// side effects.
func DatabaseSessionMiddleware(
	dbClient *ent.Client,
	factory db.SessionFactory,
	ephemeralFactory db.EphemeralSessionFactory,
	txBeginTimeout *time.Duration,
) grpc.UnaryServerInterceptor {

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info != nil && info.FullMethod == "/grpc.health.v1.Health/Check" {
			return handler(ctx, req)
		}

		logger := logging.GetLoggerFromContext(ctx)

		var opts []db.SessionOption
		if txBeginTimeout != nil {
			opts = append(opts, db.WithTxBeginTimeout(*txBeginTimeout))
		}

		if metricAttrs := grpcutil.ParseFullMethod(info.FullMethod); metricAttrs != nil {
			opts = append(opts, db.WithMetricAttributes(metricAttrs))
		}

		sessionCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		// Use a read-only session for read-only endpoints (knob-gated rollout) and always for the
		// partner-facing read API. A read-only session also makes IdempotencyInterceptor skip the
		// request, which matters for SparkPartnerService: it authenticates via Basic Auth (no authn
		// session), so a writable session would cache every partner under the empty idempotency
		// identity and could return one partner's response to another on a colliding key.
		knobsService := knobs.GetKnobsService(ctx)
		var session ent.Session
		var ephemeralSession entephemeral.Session
		isPartnerRead := strings.HasPrefix(info.FullMethod, partner.SparkPartnerServiceMethodPrefix)
		if isPartnerRead || knobsService.RolloutRandomTarget(knobs.KnobReadOnlyEndpoints, &info.FullMethod, 0) {
			session = db.NewReadOnlySession(sessionCtx, dbClient, opts...)
			if ephemeralFactory != nil {
				ephemeralSession = ephemeralFactory.NewReadOnlySession(sessionCtx, opts...)
			}
		} else {
			session = factory.NewSession(sessionCtx, opts...)
			if ephemeralFactory != nil {
				ephemeralSession = ephemeralFactory.NewSession(sessionCtx, opts...)
			}
		}

		// Attach the transaction to the context
		ctx = ent.Inject(ctx, session)
		ctx = ent.InjectNotifier(ctx, session)
		if ephemeralSession != nil {
			ctx = entephemeral.Inject(ctx, ephemeralSession)
		}

		// Ensure rollback on panic
		defer func() {
			if r := recover(); r != nil {
				if tx := session.GetTxIfExists(); tx != nil {
					if dberr := tx.Rollback(); dberr != nil {
						logger.Error("Failed to rollback transaction", zap.Error(dberr))
					}
				}
				if ephemeralSession != nil {
					if tx := ephemeralSession.GetTxIfExists(); tx != nil {
						if dberr := tx.Rollback(); dberr != nil {
							logger.Error("Failed to rollback ephemeral transaction", zap.Error(dberr))
						}
					}
				}
				panic(r)
			}
		}()

		// Call the handler (the actual RPC method)
		resp, err := handler(ctx, req)

		tx := session.GetTxIfExists()
		mainCommitted := false
		if tx != nil {
			defer func() {
				if !mainCommitted {
					_ = tx.Rollback()
				}
			}()
		}

		// GetTxIfExists is called after handler(ctx, req) returns so a tx committed inside
		// the handler has already cleared session currentTx to nil. This ensures the deferred
		// Rollback below never holds a stale reference to an already-committed transaction.
		var ephemeralTx *entephemeral.Tx
		ephemeralCommitted := false
		if ephemeralSession != nil {
			ephemeralTx = ephemeralSession.GetTxIfExists()
			if ephemeralTx != nil {
				defer func() {
					if !ephemeralCommitted {
						_ = ephemeralTx.Rollback()
					}
				}()
			}
		}

		if err == nil {
			// Detect in-handler DbCommit failures that were swallowed by the handler. When
			// DbCommit fails, currentTx is cleared (so ephemeralTx is nil above), but committing
			// the main TX would leave ephemeral state unpersisted.
			if ephemeralSession != nil && ephemeralSession.CommitError() != nil {
				return nil, fmt.Errorf("ephemeral transaction commit failed in handler: %w", ephemeralSession.CommitError())
			}
			// Ephemeral DB commits first to preserve behavior expected by existing workflows that
			// can tolerate ephemeral/main divergence and reconcile out-of-band if main commit fails.
			if ephemeralTx != nil {
				dberr := ephemeralTx.Commit()
				if dberr != nil {
					return nil, fmt.Errorf("failed to commit ephemeral transaction: %w", dberr)
				}
				ephemeralCommitted = true
			}
			if tx != nil {
				dberr := tx.Commit()
				if dberr != nil {
					if ephemeralCommitted {
						logger.With(zap.Error(dberr)).Error("Main transaction commit failed after ephemeral transaction commit")
					}
					return nil, fmt.Errorf("failed to commit transaction: %w", dberr)
				}
				mainCommitted = true
			}
		}

		return resp, err
	}
}
