package testutil

import (
	"context"

	_ "github.com/lib/pq" // postgres driver
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
)

// TestContext returns a context with a database client that can be used for testing.
func TestContext(config *so.Config) (context.Context, *ent.Client, error) {
	dbDriver := config.DatabaseDriver()
	dbClient, err := ent.Open(dbDriver, config.DatabasePath)
	if err != nil {
		return nil, nil, err
	}

	ctx := context.Background()

	tx, err := dbClient.Tx(ctx)
	if err != nil {
		return nil, nil, err
	}

	return context.WithValue(ctx, ent.TxKey, tx), dbClient, nil
}
