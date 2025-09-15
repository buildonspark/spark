package task

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"
)

var (
	errTaskTimeout = fmt.Errorf("task timed out")
	errTaskPanic   = fmt.Errorf("task panicked")
)

type TaskMiddleware func(context.Context, *so.Config, *BaseTaskSpec, knobs.Knobs) error //nolint:revive

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
		if err != nil {
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

		ctx, cancel := context.WithTimeoutCause(ctx, task.getTimeout(), errTaskTimeout)
		defer cancel()

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

			logger.Warn("Context done before task completion! Are we shutting down?", zap.Error(err))
			return err
		}
	}
}

func DatabaseMiddleware(factory db.SessionFactory, beginTxTimeout *time.Duration) TaskMiddleware {
	return func(ctx context.Context, config *so.Config, task *BaseTaskSpec, knobsService knobs.Knobs) error {
		logger := logging.GetLoggerFromContext(ctx)

		sessionCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		opts := []db.SessionOption{
			db.WithMetricAttributes([]attribute.KeyValue{
				nameKey.String(task.Name),
			}),
		}

		if beginTxTimeout != nil {
			opts = append(opts, db.WithTxBeginTimeout(*beginTxTimeout))
		}

		session := factory.NewSession(
			sessionCtx,
			opts...,
		)

		ctx = ent.Inject(ctx, session)
		err := task.Task(ctx, config, knobsService)

		tx := session.GetTxIfExists()
		if tx != nil {
			if err != nil {
				rollbackErr := tx.Rollback()
				if rollbackErr != nil {
					logger.Warn("Failed to rollback transaction after task failure", zap.Error(rollbackErr))
				}

				return err
			}

			return tx.Commit()
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
					zap.String("panic", fmt.Sprintf("%v", r)),  // TODO(mhr): Probably a better way to do this.
					zap.String("stack", string(debug.Stack())), // TODO(mhr): zap.ByteString?
				)
				err = errTaskPanic
			}
		}()

		return task.Task(ctx, config, knobsService)
	}
}
