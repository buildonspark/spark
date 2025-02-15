package ent

import (
	"context"
	"log"

	"google.golang.org/grpc"
)

// ContextKey is a type for context keys.
type ContextKey string

// TxKey is the context key for the database transaction.
const TxKey ContextKey = "tx"

// DbSessionMiddleware is a middleware to manage database sessions for each gRPC call.
func DbSessionMiddleware(dbClient *Client) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Start a transaction or session
		tx, err := dbClient.Tx(ctx)
		if err != nil {
			return nil, err
		}

		// Attach the transaction to the context
		ctx = context.WithValue(ctx, TxKey, tx)
		// Ensure rollback on panic
		defer func() {
			if r := recover(); r != nil {
				_ = tx.Rollback()
				panic(r)
			}
		}()

		// Call the handler (the actual RPC method)
		resp, err := handler(ctx, req)
		// Handle transaction commit/rollback
		if err != nil {
			if dberr := tx.Rollback(); dberr != nil {
				log.Printf("Failed to rollback transaction in %s: %s.\n", info.FullMethod, dberr)
			}
			return nil, err
		}

		if dberr := tx.Commit(); dberr != nil {
			log.Printf("Failed to commit transaction in %s: %s.\n", info.FullMethod, dberr)
			return nil, dberr
		}

		return resp, nil
	}
}

// GetDbFromContext returns the database transaction from the context.
func GetDbFromContext(ctx context.Context) *Tx {
	return ctx.Value(TxKey).(*Tx)
}
