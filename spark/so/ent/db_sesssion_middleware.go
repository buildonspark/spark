package ent

import (
	"context"
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

// DbError represents database-specific errors
type DbError struct {
	Op      string // Operation that failed
	Method  string // gRPC method where the error occurred
	Err     error  // Original error
	IsPanic bool   // Whether this error was from a panic
}

func (e *DbError) Error() string {
	if e.IsPanic {
		return fmt.Sprintf("panic in %s during %s: %v", e.Method, e.Op, e.Err)
	}
	return fmt.Sprintf("database error in %s during %s: %v", e.Method, e.Op, e.Err)
}

// DbSessionMiddleware is a middleware to manage database sessions for each gRPC call.
func DbSessionMiddleware(dbClient *Client) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Add transaction timeout
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		logger := slog.Default().With("method", info.FullMethod)

		// Start a transaction or session
		tx, err := dbClient.Tx(ctx)
		if err != nil {
			logger.Error("Failed to start transaction", "error", err)
			return nil, status.Error(codes.Internal, (&DbError{
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
					Op:      "transaction_execution",
					Method:  info.FullMethod,
					Err:     fmt.Errorf("panic: %v", r),
					IsPanic: true,
				})
			}
		}()

		// Call the handler (the actual RPC method)
		resp, err := handler(ctx, req)
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
// It panics if no transaction is found in the context.
func MustGetDbFromContext(ctx context.Context) *Tx {
	tx := GetDbFromContext(ctx)
	if tx == nil {
		panic("no database transaction found in context")
	}
	return tx
}
