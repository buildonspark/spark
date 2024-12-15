package testutil

import (
	"context"

	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
)

// TestContext returns a context with a database client that can be used for testing.
func TestContext(config *so.Config) (context.Context, error) {
	dbClient, err := ent.Open(config.DatabaseDriver(), config.DatabasePath+"?_fk=1")
	if err != nil {
		return nil, err
	}

	ctx := context.Background()

	tx, err := dbClient.Tx(ctx)
	if err != nil {
		return nil, err
	}

	return context.WithValue(ctx, ent.TxKey, tx), nil
}
