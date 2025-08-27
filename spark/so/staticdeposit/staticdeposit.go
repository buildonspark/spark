package staticdeposit

import (
	"context"

	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/utxo"
	"github.com/lightsparkdev/spark/so/ent/utxoswap"
)

// Fetch a utxo swap that claimed a given utxo from the database.
func GetRegisteredUtxoSwapForUtxo(ctx context.Context, dbTx *ent.Tx, targetUtxo *ent.Utxo) (*ent.UtxoSwap, error) {
	utxoSwap, err := dbTx.UtxoSwap.Query().
		Where(utxoswap.HasUtxoWith(utxo.IDEQ(targetUtxo.ID))).
		Where(utxoswap.StatusIn(st.UtxoSwapStatusCreated, st.UtxoSwapStatusCompleted)).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	return utxoSwap, nil
}
