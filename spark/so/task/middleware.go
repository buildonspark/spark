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
	"go.opentelemetry.io/otel/attribute"
)

var (
	errTaskTimeout = fmt.Errorf("task timed out")
	errTaskPanic   = fmt.Errorf("task panicked")
)

type TaskMiddleware func(context.Context, *so.Config, *BaseTaskSpec) error //nolint:revive

func LogMiddleware() TaskMiddleware {
	return func(ctx context.Context, config *so.Config, task *BaseTaskSpec) error {
		logger := logging.GetLoggerFromContext(ctx).
			With("task.name", task.Name).
			With("task.id", uuid.New().String())

		ctx = logging.Inject(ctx, logger)

		logger.Info("Executing task")

		err := task.Task(ctx, config)
		if err != nil {
			logger.Error("Task execution failed", "error", err)
			return err
		}

		logger.Info("Task executed successfully")
		return nil
	}
}

func TimeoutMiddleware() TaskMiddleware {
	return func(ctx context.Context, config *so.Config, task *BaseTaskSpec) error {
		logger := logging.GetLoggerFromContext(ctx)

		ctx, cancel := context.WithTimeoutCause(ctx, task.getTimeout(), errTaskTimeout)
		defer cancel()

		done := make(chan error)

		go func() {
			defer close(done)

			err := task.Task(ctx, config)

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

			logger.Warn("Context done before task completion! Are we shutting down?", "error", err)
			return err
		}
	}
}

func DatabaseMiddleware(factory db.SessionFactory, beginTxTimeout *time.Duration) TaskMiddleware {
	return func(ctx context.Context, config *so.Config, task *BaseTaskSpec) error {
		logger := logging.GetLoggerFromContext(ctx)

		sessionCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		opts := []db.SessionOption{
			db.WithMetricAttributes([]attribute.KeyValue{
				TaskNameKey.String(task.Name),
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
		err := task.Task(ctx, config)

		tx := session.GetTxIfExists()
		if tx != nil {
			if err != nil {
				rollbackErr := tx.Rollback()
				if rollbackErr != nil {
					logger.Warn("Failed to rollback transaction after task failure", "error", rollbackErr)
				}

				return err
			}

			return tx.Commit()
		}

		return err
	}
}

func PanicRecoveryMiddleware() TaskMiddleware {
	return func(ctx context.Context, config *so.Config, task *BaseTaskSpec) (err error) {
		logger := logging.GetLoggerFromContext(ctx)
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				logger.Error("Panic in task execution",
					"panic", fmt.Sprintf("%v", r),
					"stack", string(stack),
				)
				err = errTaskPanic
			}
		}()

		return task.Task(ctx, config)
	}
}
