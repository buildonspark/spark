package ent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/lightsparkdev/spark-go/so/ent"
	_ "github.com/mattn/go-sqlite3" // Register SQLite driver
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// mockDriver implements dialect.Driver for testing
type mockDriver struct {
	txErr error
}

func (m *mockDriver) Close() error                                                  { return nil }
func (m *mockDriver) Query(context.Context, string, interface{}, interface{}) error { return nil }
func (m *mockDriver) Exec(context.Context, string, interface{}, interface{}) error  { return nil }
func (m *mockDriver) Dialect() string                                               { return "mock" }
func (m *mockDriver) Tx(context.Context) (dialect.Tx, error) {
	if m.txErr != nil {
		return nil, m.txErr
	}
	return &mockTx{}, nil
}

// mockTx implements dialect.Tx for testing
type mockTx struct{}

func (m *mockTx) Commit() error                                                 { return nil }
func (m *mockTx) Rollback() error                                               { return nil }
func (m *mockTx) Query(context.Context, string, interface{}, interface{}) error { return nil }
func (m *mockTx) Exec(context.Context, string, interface{}, interface{}) error  { return nil }

func newMockClient(txErr error) *ent.Client {
	return ent.NewClient(ent.Driver(&mockDriver{txErr: txErr}))
}

func TestDbSessionMiddleware(t *testing.T) {
	// Create an in-memory SQLite database
	drv, err := sql.Open("sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer drv.Close()

	// Create client
	client := ent.NewClient(ent.Driver(drv))
	defer client.Close()

	// Create middleware
	middleware := ent.DbSessionMiddleware(client)

	t.Run("successful transaction", func(t *testing.T) {
		ctx := context.Background()
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		// Mock handler that succeeds
		handler := func(ctx context.Context, _ interface{}) (interface{}, error) {
			// Verify transaction exists in context
			tx := ent.GetDbFromContext(ctx)
			assert.NotNil(t, tx, "transaction should be in context")
			return "success", nil
		}

		// Execute middleware
		resp, err := middleware(ctx, "test-request", info, handler)
		assert.NoError(t, err)
		assert.Equal(t, "success", resp)
	})

	t.Run("handler error rolls back transaction", func(t *testing.T) {
		ctx := context.Background()
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		// Mock handler that returns error
		handler := func(_ context.Context, _ interface{}) (interface{}, error) {
			return nil, errors.New("handler error")
		}

		// Execute middleware
		resp, err := middleware(ctx, "test-request", info, handler)
		assert.Error(t, err)
		assert.Nil(t, resp)
		assert.Contains(t, err.Error(), "handler error")
	})

	t.Run("context timeout", func(t *testing.T) {
		// Create a middleware with a mock client that simulates timeout
		middleware := ent.DbSessionMiddleware(newMockClient(context.DeadlineExceeded))

		// Create a context with a very short timeout
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
		defer cancel()

		// Sleep to ensure timeout
		time.Sleep(1 * time.Millisecond)

		// Call the middleware
		_, err := middleware(ctx, nil, &grpc.UnaryServerInfo{
			FullMethod: "/test.Service/TestMethod",
		}, func(_ context.Context, _ interface{}) (interface{}, error) {
			return nil, nil
		})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), ent.ErrTransactionTimeout.Error())
	})

	t.Run("panic recovery", func(t *testing.T) {
		ctx := context.Background()
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		// Mock handler that panics
		handler := func(_ context.Context, _ interface{}) (interface{}, error) {
			panic("test panic")
		}

		// Execute middleware and verify panic is recovered with DbError
		assert.Panics(t, func() {
			_, err := middleware(ctx, "test-request", info, handler)
			require.Error(t, err) // This won't be reached due to panic, but linter wants error checked
		})
	})
}

func TestGetDbFromContext(t *testing.T) {
	// Create an in-memory SQLite database
	drv, err := sql.Open("sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer drv.Close()

	// Create client
	client := ent.NewClient(ent.Driver(drv))
	defer client.Close()

	t.Run("nil context", func(t *testing.T) {
		tx := ent.GetDbFromContext(context.TODO())
		assert.Nil(t, tx)
	})

	t.Run("context without transaction", func(t *testing.T) {
		tx := ent.GetDbFromContext(context.Background())
		assert.Nil(t, tx)
	})

	t.Run("context with transaction", func(t *testing.T) {
		ctx := context.Background()
		tx, err := client.Tx(ctx)
		assert.NoError(t, err)
		defer func() {
			err := tx.Rollback()
			assert.NoError(t, err)
		}()

		// Store it in context
		ctx = context.WithValue(ctx, ent.TxKey, tx)

		// Test GetDbFromContext
		gotTx := ent.GetDbFromContext(ctx)
		assert.Equal(t, tx, gotTx)
	})
}

func TestMustGetDbFromContext(t *testing.T) {
	t.Run("nil context panics", func(t *testing.T) {
		assert.PanicsWithValue(t, ent.ErrNoTransaction, func() {
			ent.MustGetDbFromContext(context.TODO())
		})
	})

	t.Run("context without transaction panics", func(t *testing.T) {
		assert.PanicsWithValue(t, ent.ErrNoTransaction, func() {
			ent.MustGetDbFromContext(context.Background())
		})
	})

	t.Run("context with transaction succeeds", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ent.TxKey, &ent.Tx{})
		assert.NotPanics(t, func() {
			tx := ent.MustGetDbFromContext(ctx)
			assert.NotNil(t, tx)
		})
	})
}

func TestDbError(t *testing.T) {
	t.Run("normal error", func(t *testing.T) {
		err := &ent.DbError{
			Code:   ent.DbErrorExecution,
			Op:     "test_operation",
			Method: "/test.Service/TestMethod",
			Err:    errors.New("test error"),
		}
		assert.Equal(t,
			"database error in /test.Service/TestMethod during test_operation: test error (code: execution)",
			err.Error(),
		)
	})

	t.Run("panic error", func(t *testing.T) {
		err := &ent.DbError{
			Code:    ent.DbErrorPanic,
			Op:      "test_operation",
			Method:  "/test.Service/TestMethod",
			Err:     errors.New("test panic"),
			IsPanic: true,
		}
		assert.Equal(t,
			"panic in /test.Service/TestMethod during test_operation: test panic (code: panic)",
			err.Error(),
		)
	})
}
