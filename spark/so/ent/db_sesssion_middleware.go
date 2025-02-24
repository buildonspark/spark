package ent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ContextKey is a type for context keys.
type ContextKey string

// TxKey is the context key for the database transaction.
const TxKey ContextKey = "tx"

// Common errors that can be checked with errors.Is
var (
	ErrTransactionTimeout = errors.New("database transaction timeout")
	ErrNoTransaction      = errors.New("no transaction in context")
	ErrTransactionStarted = errors.New("transaction already started")
	ErrInvalidContext     = errors.New("invalid or nil context")
)

// DbErrorCode represents specific database error scenarios
type DbErrorCode string

const (
	DbErrorBeginTx   DbErrorCode = "begin_transaction"
	DbErrorCommit    DbErrorCode = "commit"
	DbErrorRollback  DbErrorCode = "rollback"
	DbErrorExecution DbErrorCode = "execution"
	DbErrorPanic     DbErrorCode = "panic"
)

// DbError represents database-specific errors
type DbError struct {
	Code    DbErrorCode // Specific error code
	Op      string      // Operation that failed
	Method  string      // gRPC method where the error occurred
	Err     error       // Original error
	IsPanic bool        // Whether this error was from a panic
	Stack   string      // Stack trace for panic errors
}

func (e *DbError) Error() string {
	if e.IsPanic {
		return fmt.Sprintf("panic in %s during %s: %v (code: %s)", e.Method, e.Op, e.Err, e.Code)
	}
	return fmt.Sprintf("database error in %s during %s: %v (code: %s)", e.Method, e.Op, e.Err, e.Code)
}

func (e *DbError) Unwrap() error {
	return e.Err
}

// IsTransactionError returns true if the error is a database transaction error
func IsTransactionError(err error) bool {
	var dbErr *DbError
	return errors.As(err, &dbErr)
}

// DbSessionMiddleware is a middleware to manage database sessions for each gRPC call.
func DbSessionMiddleware(dbClient *Client) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if ctx == nil {
			return nil, status.Error(codes.Internal, ErrInvalidContext.Error())
		}

		// Add transaction timeout
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		logger := slog.Default().With(
			"method", info.FullMethod,
			"request_id", ctx.Value("request_id"), // Capture request ID if present
		)

		// Check if transaction already exists
		if existingTx := GetDbFromContext(ctx); existingTx != nil {
			return nil, status.Error(codes.Internal, ErrTransactionStarted.Error())
		}

		// Start a transaction or session
		tx, err := dbClient.Tx(ctx)
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				logger.Error("Transaction start timeout", "error", err)
				return nil, status.Error(codes.DeadlineExceeded, ErrTransactionTimeout.Error())
			}
			logger.Error("Failed to start transaction", "error", err)
			return nil, status.Error(codes.Internal, (&DbError{
				Code:   DbErrorBeginTx,
				Op:     "begin_transaction",
				Method: info.FullMethod,
				Err:    err,
			}).Error())
		}

		// Attach the transaction to the context
		ctx = context.WithValue(ctx, TxKey, tx)

		// Ensure rollback on panic with detailed logging
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				logger.Error("Panic recovered in database transaction",
					"panic", r,
					"stack", string(stack),
				)

				if rbErr := tx.Rollback(); rbErr != nil {
					logger.Error("Failed to rollback transaction after panic",
						"rollback_error", rbErr,
						"original_panic", r,
					)
				}

				// Re-panic with more context
				panic(&DbError{
					Code:    DbErrorPanic,
					Op:      "transaction_execution",
					Method:  info.FullMethod,
					Err:     fmt.Errorf("panic: %v", r),
					IsPanic: true,
					Stack:   string(stack),
				})
			}
		}()

		// Call the handler (the actual RPC method)
		resp, err := handler(ctx, req)

		// Check for timeout before commit/rollback
		if ctx.Err() == context.DeadlineExceeded {
			if rbErr := tx.Rollback(); rbErr != nil {
				logger.Error("Failed to rollback timed out transaction",
					"rollback_error", rbErr,
				)
			}
			return nil, status.Error(codes.DeadlineExceeded, ErrTransactionTimeout.Error())
		}

		// Handle transaction commit/rollback
		if err != nil {
			if dberr := tx.Rollback(); dberr != nil {
				logger.Error("Failed to rollback transaction",
					"method", info.FullMethod,
					"original_error", err,
					"rollback_error", dberr,
				)
				// Return a combined error
				return nil, status.Error(codes.Internal, (&DbError{
					Code:   DbErrorRollback,
					Op:     "rollback",
					Method: info.FullMethod,
					Err:    fmt.Errorf("rollback failed: %v (original error: %v)", dberr, err),
				}).Error())
			}
			return nil, err
		}

		if dberr := tx.Commit(); dberr != nil {
			logger.Error("Failed to commit transaction",
				"method", info.FullMethod,
				"error", dberr,
			)
			return nil, status.Error(codes.Internal, (&DbError{
				Code:   DbErrorCommit,
				Op:     "commit",
				Method: info.FullMethod,
				Err:    dberr,
			}).Error())
		}

		return resp, nil
	}
}

// GetDbFromContext returns the database transaction from the context.
// It returns nil if no transaction is found in the context.
func GetDbFromContext(ctx context.Context) *Tx {
	if ctx == nil {
		return nil
	}
	tx, ok := ctx.Value(TxKey).(*Tx)
	if !ok {
		return nil
	}
	return tx
}

// MustGetDbFromContext returns the database transaction from the context.
// It panics if the context is invalid or no transaction is found in the context.
func MustGetDbFromContext(ctx context.Context) *Tx {
	tx := GetDbFromContext(ctx)
	if tx == nil {
		panic(ErrNoTransaction)
	}
	return tx
}
