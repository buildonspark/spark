package common

import (
	"context"
	"log"

	"github.com/lightsparkdev/spark-go/so/ent"
	"google.golang.org/grpc"
)

type ContextKey string

const TxKey ContextKey = "tx"

// Middleware to manage database sessions for each gRPC call.
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
			err = tx.Rollback()
			if err != nil {
				log.Printf("Failed to rollback in %s: %s.\n", info.FullMethod, err)
			}
		} else {
			err = tx.Commit()
			if err != nil {
				log.Printf("Failed to commit in %s: %s.\n", info.FullMethod, err)
			}
		}

		return resp, err
	}
}

func GetDbFromContext(ctx context.Context) *ent.Tx {
	return ctx.Value(TxKey).(*ent.Tx)
}
