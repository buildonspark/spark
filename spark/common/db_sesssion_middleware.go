package common

import (
	"context"
	"log"

	"github.com/lightsparkdev/spark-go/so/ent"
	"google.golang.org/grpc"
)

// ContextKey is a type for context keys.
type ContextKey string

// TxKey is the context key for the database transaction.
const TxKey ContextKey = "tx"

// DbSessionMiddleware is a middleware to manage database sessions for each gRPC call.
func DbSessionMiddleware(dbClient *ent.Client) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Start a transaction or session
		tx, err := dbClient.Tx(ctx)
		if err != nil {
			return nil, err
		}

		// Attach the transaction to the context
		ctx = context.WithValue(ctx, TxKey, tx)

		// Call the handler (the actual RPC method)
		resp, err := handler(ctx, req)

		// If there was an error, rollback the transaction, otherwise commit
		if err != nil {
			dberr := tx.Rollback()
			if dberr != nil {
				log.Printf("Failed to rollback in %s: %s.\n", info.FullMethod, dberr)
			}
		} else {
			dberr := tx.Commit()
			if dberr != nil {
				log.Printf("Failed to commit in %s: %s.\n", info.FullMethod, dberr)
			}
		}

		return resp, err
	}
}

// GetDbFromContext returns the database transaction from the context.
func GetDbFromContext(ctx context.Context) *ent.Tx {
	return ctx.Value(TxKey).(*ent.Tx)
}
