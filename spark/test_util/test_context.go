package testutil

import (
	"context"
	"testing"

	_ "github.com/lib/pq" // postgres driver
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
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

func OnErrFatal(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("error: %v", err)
	}
}
