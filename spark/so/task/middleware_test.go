package task

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

func TestTimeoutMiddleware_TestSlowTask(t *testing.T) {
	config, err := sparktesting.TestConfig()
	require.NoError(t, err)
	ctx, _ := db.NewTestSQLiteContext(t)

	timeout := 200 * time.Millisecond
	task := BaseTaskSpec{
		Name:    "Test",
		Timeout: &timeout,
		Task: func(ctx context.Context, _ *so.Config, _ knobs.Knobs) error {
			select {
			case <-time.After(10 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}

			return nil
		},
	}

	taskWithTimeout := task.wrapMiddleware(TimeoutMiddleware())

	err = taskWithTimeout.Task(ctx, config, knobs.NewFixedKnobs(map[string]float64{}))
	require.ErrorIs(t, err, errTaskTimeout)
}

func TestTimeoutMiddleware_TestTaskFinishes(t *testing.T) {
	config, err := sparktesting.TestConfig()
	require.NoError(t, err)
	ctx, _ := db.NewTestSQLiteContext(t)

	timeout := 10 * time.Second
	task := BaseTaskSpec{
		Name:    "Test",
		Timeout: &timeout,
		Task: func(ctx context.Context, _ *so.Config, _ knobs.Knobs) error {
			select {
			case <-time.After(200 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}

			return nil
		},
	}

	taskWithTimeout := task.wrapMiddleware(TimeoutMiddleware())

	err = taskWithTimeout.Task(ctx, config, knobs.NewFixedKnobs(map[string]float64{}))
	require.NoError(t, err)
}

func TestTimeoutMiddleware_TestContextCancelled(t *testing.T) {
	config, err := sparktesting.TestConfig()
	require.NoError(t, err)
	ctx, _ := db.NewTestSQLiteContext(t)

	timeout := 10 * time.Second
	task := BaseTaskSpec{
		Name:    "Test",
		Timeout: &timeout,
		Task: func(ctx context.Context, _ *so.Config, _ knobs.Knobs) error {
			select {
			case <-time.After(10 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}

			return nil
		},
	}

	taskWithTimeout := task.wrapMiddleware(TimeoutMiddleware())

	cancelCtx, cancelCause := context.WithCancelCause(ctx)

	errChan := make(chan error)
	go func() {
		defer close(errChan)

		select {
		case errChan <- taskWithTimeout.Task(cancelCtx, config, knobs.NewFixedKnobs(map[string]float64{})):
		case <-ctx.Done():
		}
	}()

	// Give the task some time to start running...
	select {
	case err := <-errChan:
		t.Fatalf("Received error before context was cancelled: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	// Now cancel it because our application is shutting down.
	errShutdown := errors.New("shutting down")
	cancelCause(errShutdown)

	select {
	case err := <-errChan:
		require.ErrorIs(t, err, errShutdown)
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("Didn't receive error after context was cancelled.")
	}
}

func TestDatabaseMiddleware_TestCommitWhenTaskSuccessful(t *testing.T) {
	config, err := sparktesting.TestConfig()
	require.NoError(t, err)

	dbClient := db.NewTestSQLiteClient(t)
	dbSession := db.NewSession(t.Context(), dbClient)

	// Seed a transaction in the session that we can verify is committed.
	dbTx, err := dbSession.GetOrBeginTx(t.Context())
	require.NoError(t, err)

	txChan := make(chan struct{})
	dbTx.OnCommit(func(fn ent.Committer) ent.Committer {
		return ent.CommitFunc(func(ctx context.Context, tx *ent.Tx) error {
			defer close(txChan)

			if err := fn.Commit(ctx, tx); err != nil {
				return err
			}

			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled before commit: %w", ctx.Err())
			case txChan <- struct{}{}:
			}

			return nil
		})
	})

	task := BaseTaskSpec{
		Name:    "Test",
		Timeout: nil,
		Task: func(_ context.Context, _ *so.Config, _ knobs.Knobs) error {
			return nil
		},
	}

	errChan := make(chan error)
	go func() {
		defer close(errChan)

		taskWithDb := task.wrapMiddleware(DatabaseMiddleware(&db.TestSessionFactory{Session: dbSession}, nil))
		err = taskWithDb.Task(t.Context(), config, knobs.NewFixedKnobs(map[string]float64{}))

		select {
		case <-t.Context().Done():
		case errChan <- err:
		}
	}()

	select {
	case <-txChan:
		// Transaction was committed successfully. This is what we wanted!
	case err := <-errChan:
		t.Fatalf("Expected transaction to be committed before task returned (got %v)", err)
	case <-time.After(200 * time.Millisecond):
		require.Fail(t, "Expected transaction to be committed, but it took too long")
	}

	// Also make sure that the wrapped task completed successfully without error.
	select {
	case err := <-errChan:
		require.NoError(t, err, "Expected task to complete successfully without error")
	case <-time.After(200 * time.Millisecond):
		require.Fail(t, "Expected task to complete successfully, but it took too long")
	}

	require.Nil(t, dbSession.GetTxIfExists(), "Expected no current transaction after task completed.")
}

func TestDatabaseMiddleware_TestRollbackWhenTaskUnsuccessful(t *testing.T) {
	config, err := sparktesting.TestConfig()
	require.NoError(t, err)

	dbClient := db.NewTestSQLiteClient(t)
	dbSession := db.NewSession(t.Context(), dbClient)

	// Seed a transaction in the session that we can verify is committed.
	dbTx, err := dbSession.GetOrBeginTx(t.Context())
	require.NoError(t, err)

	rollbackChan := make(chan struct{})
	dbTx.OnRollback(func(fn ent.Rollbacker) ent.Rollbacker {
		return ent.RollbackFunc(func(ctx context.Context, tx *ent.Tx) error {
			defer close(rollbackChan)

			if err := fn.Rollback(ctx, tx); err != nil {
				return err
			}

			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled before commit: %w", ctx.Err())
			case rollbackChan <- struct{}{}:
			}

			return nil
		})
	})

	taskErr := errors.New("oh no, task failed")
	task := BaseTaskSpec{
		Name:    "Test",
		Timeout: nil,
		Task: func(_ context.Context, _ *so.Config, _ knobs.Knobs) error {
			return taskErr
		},
	}

	errChan := make(chan error)
	go func() {
		defer close(errChan)

		taskWithDb := task.wrapMiddleware(DatabaseMiddleware(&db.TestSessionFactory{Session: dbSession}, nil))
		err = taskWithDb.Task(t.Context(), config, knobs.NewFixedKnobs(map[string]float64{}))
		if err != nil {
			errChan <- err
		}
	}()

	select {
	case <-rollbackChan:
		// Rollback was called successfully. This is what we wanted!
	case err := <-errChan:
		t.Fatalf("Expected task to rollback before returning error, but got error: %v", err)
	case <-time.After(200 * time.Millisecond):
		require.Fail(t, "Expected transaction to be rolled back, but it took too long")
	}

	// Also make sure that we get the error from the task and it's not swallowed by the middleware.
	select {
	case err := <-errChan:
		require.ErrorIs(t, err, taskErr, "Expected task to return the original error")
	case <-time.After(200 * time.Millisecond):
		require.Fail(t, "Expected task to return an error, but it took too long")
	}

	require.Nil(t, dbSession.GetTxIfExists(), "Expected no current transaction after task completed.")
}

func TestDatabaseMiddleware_TestTaskCanCommitTransaction(t *testing.T) {
	config, err := sparktesting.TestConfig()
	require.NoError(t, err)

	dbClient := db.NewTestSQLiteClient(t)
	dbSession := db.NewSession(t.Context(), dbClient)

	task := BaseTaskSpec{
		Name:    "Test",
		Timeout: nil,
		Task: func(ctx context.Context, _ *so.Config, _ knobs.Knobs) error {
			tx, err := ent.GetDbFromContext(ctx)
			if err != nil {
				return err
			}

			return tx.Commit()
		},
	}

	taskWithDb := task.wrapMiddleware(DatabaseMiddleware(&db.TestSessionFactory{Session: dbSession}, nil))

	err = taskWithDb.Task(t.Context(), config, knobs.NewFixedKnobs(map[string]float64{}))
	require.NoError(t, err, "Expected task to commit transaction successfully")

	require.Nil(t, dbSession.GetTxIfExists(), "Expected no current transaction after task completed.")
}

func TestPanicRecoveryInterceptor_TestNoPanic(t *testing.T) {
	config, err := sparktesting.TestConfig()
	require.NoError(t, err)
	ctx, _ := db.NewTestSQLiteContext(t)

	task := BaseTaskSpec{
		Name:    "Test",
		Timeout: nil,
		Task: func(_ context.Context, _ *so.Config, _ knobs.Knobs) error {
			return nil // No panic, just cool calm task execution.
		},
	}

	taskWithRecovery := task.wrapMiddleware(PanicRecoveryMiddleware())

	err = taskWithRecovery.Task(ctx, config, knobs.NewFixedKnobs(map[string]float64{}))
	require.NoError(t, err)
}

func TestPanicRecoveryInterceptor_TestPanic(t *testing.T) {
	config, err := sparktesting.TestConfig()
	require.NoError(t, err)
	ctx, _ := db.NewTestSQLiteContext(t)

	task := BaseTaskSpec{
		Name:    "Test",
		Timeout: nil,
		Task: func(_ context.Context, _ *so.Config, _ knobs.Knobs) error {
			panic("AHHHHHHHHHHHHHH!")
		},
	}

	taskWithRecovery := task.wrapMiddleware(PanicRecoveryMiddleware())

	err = taskWithRecovery.Task(ctx, config, knobs.NewFixedKnobs(map[string]float64{}))
	require.ErrorIs(t, err, errTaskPanic)
}
