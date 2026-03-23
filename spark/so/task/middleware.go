package task

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/entephemeral"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
)

var (
	errTaskDisabled = fmt.Errorf("task is disabled")
	errTaskTimeout  = fmt.Errorf("task timed out")
	errTaskPanic    = fmt.Errorf("task panicked")
)

type TaskMiddleware func(context.Context, *so.Config, *BaseTaskSpec, knobs.Knobs) error

func LogMiddleware() TaskMiddleware {
	return func(ctx context.Context, config *so.Config, task *BaseTaskSpec, knobsService knobs.Knobs) error {
		tracer := otel.Tracer("gocron")

		ctx, span := tracer.Start(ctx, task.Name)
		defer span.End()

		ctx, logger := logging.WithAttrs(ctx,
			zap.String("task.name", task.Name),
			zap.Stringer("task.id", uuid.New()),
			zap.Stringer("task.trace_id", span.SpanContext().TraceID()),
		)

		logger.Info("Executing task")

		err := task.Task(ctx, config, knobsService)
		if err != nil && !errors.Is(err, errTaskDisabled) {
			span.SetStatus(codes.Error, err.Error())
			logger.Error("Task execution failed", zap.Error(err))
			return err
		}

		logger.Info("Task executed successfully")
		return nil
	}
}

func TimeoutMiddleware() TaskMiddleware {
	return func(ctx context.Context, config *so.Config, task *BaseTaskSpec, knobsService knobs.Knobs) error {
		logger := logging.GetLoggerFromContext(ctx)

		enabled := knobsService.RolloutRandomTarget(knobs.KnobSoTaskEnabled, &task.Name, 100)
		if !enabled {
			return errTaskDisabled
		}

		timeout := knobsService.GetDurationTarget(knobs.KnobSoTaskTimeout, &task.Name, task.getTimeout())

		ctx, cancel := context.WithTimeoutCause(ctx, timeout, errTaskTimeout)
		defer cancel()

		ctx = knobs.InjectKnobsService(ctx, knobsService)

		done := make(chan error)

		go func() {
			defer close(done)

			err := task.Task(ctx, config, knobsService)

			select {
			case done <- err:
			case <-ctx.Done():
			}
		}()

		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			err := context.Cause(ctx)
			if errors.Is(err, errTaskTimeout) {
				logger.Warn("Task timed out!")
				return err
			}
			if err != nil {
				logger.With(zap.Error(err)).Warn("Context done before task completion! Are we shutting down?")
			} else {
				logger.Warn("Context done before task completion! Are we shutting down?")
			}
			return err
		}
	}
}

func RawDBClientMiddleware(dbClient *ent.Client) TaskMiddleware {
	return func(ctx context.Context, config *so.Config, task *BaseTaskSpec, knobsService knobs.Knobs) error {
		if !task.RequiresRawDBClient {
			return task.Task(ctx, config, knobsService)
		}
		rawDB, err := dbClient.RawDB()
		if err != nil {
			return fmt.Errorf("failed to get raw database client: %w", err)
		}
		return task.Task(InjectRawClient(ctx, rawDB), config, knobsService)
	}
}

// DatabaseMiddleware manages per-task main and ephemeral DB sessions.
// On successful task execution, ephemeral commits are attempted before main commits.
// If ephemeral commit fails, this middleware returns an error even when the task function
// returned success to avoid acknowledging completion without durable ephemeral state.
//
// Task functions must be idempotent: if the ephemeral commit fails after task execution, the
// error returned to the caller may trigger a retry that re-executes the task with all its
// side effects.
func DatabaseMiddleware(factory db.SessionFactory, ephemeralFactory db.EphemeralSessionFactory, beginTxTimeout *time.Duration) TaskMiddleware {
	return func(ctx context.Context, config *so.Config, task *BaseTaskSpec, knobsService knobs.Knobs) error {
		logger := logging.GetLoggerFromContext(ctx)
		sessionCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		opts := []db.SessionOption{
			db.WithMetricAttributes([]attribute.KeyValue{
				nameKey.String(task.Name),
			}),
		}

		// EphemeralSessionFactory only uses txBeginTimeout; metric attributes are not emitted.
		var ephemeralOpts []db.SessionOption
		if beginTxTimeout != nil {
			opts = append(opts, db.WithTxBeginTimeout(*beginTxTimeout))
			ephemeralOpts = append(ephemeralOpts, db.WithTxBeginTimeout(*beginTxTimeout))
		}

		session := factory.NewSession(sessionCtx, opts...)
		var ephemeralSession entephemeral.Session
		if ephemeralFactory != nil {
			ephemeralSession = ephemeralFactory.NewSession(sessionCtx, ephemeralOpts...)
		}

		ctx = ent.Inject(ctx, session)
		ctx = ent.InjectNotifier(ctx, session)
		if ephemeralSession != nil {
			ctx = entephemeral.Inject(ctx, ephemeralSession)
		}

		// Pre-register cleanup so rollbacks are guaranteed to fire even if the task panics.
		// txDone is set after all commits succeed; until then, the defer rolls back any
		// live transactions (Rollback is a no-op if already committed or rolled back).
		var txDone bool
		defer func() {
			if !txDone {
				if tx := session.GetTxIfExists(); tx != nil {
					if rollbackErr := tx.Rollback(); rollbackErr != nil {
						logger.With(zap.Error(rollbackErr)).Warn("Failed to rollback main task transaction")
					}
				}
				if ephemeralSession != nil {
					if etx := ephemeralSession.GetTxIfExists(); etx != nil {
						if rollbackErr := etx.Rollback(); rollbackErr != nil {
							logger.With(zap.Error(rollbackErr)).Warn("Failed to rollback ephemeral task transaction")
						}
					}
				}
			}
		}()

		err := task.Task(ctx, config, knobsService)

		if err == nil {
			// Detect in-handler DbCommit failures that were swallowed by the task.
			// When DbCommit fails in-handler, commitErr is set but currentTx is kept alive
			// for deferred rollback. Without this check, the middleware would re-attempt
			// Commit() on an already-failed transaction. Skip the commit entirely and
			// surface the earlier failure.
			ephemeralCommitted := false
			if ephemeralSession != nil {
				if commitErr := ephemeralSession.CommitError(); commitErr != nil {
					return fmt.Errorf("ephemeral transaction commit failed in task: %w", commitErr)
				}
				if ephemeralTx := ephemeralSession.GetTxIfExists(); ephemeralTx != nil {
					if commitErr := ephemeralTx.Commit(); commitErr != nil {
						return fmt.Errorf("failed to commit task ephemeral transaction: %w", commitErr)
					}
					ephemeralCommitted = true
				} else {
					// GetTxIfExists returns nil after an in-handler commit. Only treat this as a
					// committed ephemeral TX if GetOrBeginTx was actually called; otherwise the
					// session was injected but never used and there is nothing to track.
					ephemeralCommitted = ephemeralSession.TxWasStarted()
				}
			}
			if tx := session.GetTxIfExists(); tx != nil {
				if commitErr := tx.Commit(); commitErr != nil {
					if ephemeralCommitted {
						logger.With(zap.Error(commitErr)).Error("Main task transaction commit failed after ephemeral transaction commit")
						ephemeralDivergenceCounter().Add(ctx, 1, metric.WithAttributes(nameKey.String(task.Name)))
					}
					return fmt.Errorf("failed to commit task transaction: %w", commitErr)
				}
			}
			// txDone signals "the full success path completed", not "a TX was committed".
			// It is set unconditionally here because either no TX was active (no-op) or
			// all commits succeeded. The deferred cleanup skips rollbacks when txDone is true.
			txDone = true
		}

		return err
	}
}

func PanicRecoveryMiddleware() TaskMiddleware {
	return func(ctx context.Context, config *so.Config, task *BaseTaskSpec, knobsService knobs.Knobs) (err error) {
		logger := logging.GetLoggerFromContext(ctx)
		defer func() {
			if r := recover(); r != nil {
				logger.Error("Panic in task execution",
					zap.Any("panic", r),
					zap.Stack("stack"),
				)
				err = errTaskPanic
			}
		}()

		return task.Task(ctx, config, knobsService)
	}
}
