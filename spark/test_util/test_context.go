package test_util

import (
	"context"

	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
)

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

	return context.WithValue(ctx, common.TxKey, tx), nil
}
