package handler

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/blockheight"
	entutxo "github.com/lightsparkdev/spark/so/ent/utxo"
)

// isDepositUtxoAvailableForTreeCreation determines whether the requested
// single-input deposit should start as AVAILABLE even if the deposit address's
// availability_confirmed_at flag is stale inside the current transaction.
//
// This can happen when FinalizeDepositTreeCreation holds a FOR UPDATE lock on
// the deposit address row while the chain watcher has already observed enough
// confirmations and is blocked on the UPDATE that sets availability_confirmed_at.
// In that interleaving, confirmation_height/current block height still reflect
// the durable on-chain state, so we prefer them over the mutable flag.
func isDepositUtxoAvailableForTreeCreation(
	ctx context.Context,
	config *so.Config,
	network btcnetwork.Network,
	depositAddress *ent.DepositAddress,
	onChainTx *wire.MsgTx,
	vout uint32,
) (bool, error) {
	if !depositAddress.AvailabilityConfirmedAt.IsZero() {
		return true, nil
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get database: %w", err)
	}

	requestedTxid := onChainTx.TxHash().String()
	if depositAddress.ConfirmationHeight > 0 &&
		depositAddress.ConfirmationTxid == requestedTxid {
		currentBlockHeight, err := db.BlockHeight.Query().
			Where(blockheight.NetworkEQ(network)).
			Only(ctx)
		if err != nil {
			if !ent.IsNotFound(err) {
				return false, fmt.Errorf("failed to get current block height: %w", err)
			}
		} else {
			threshold := resolveConfirmationThreshold(nil, config, network)
			if depositAddress.ConfirmationHeight <= currentBlockHeight.Height-int64(threshold)+1 {
				return true, nil
			}
		}
	}

	txidBytes, err := hex.DecodeString(requestedTxid)
	if err != nil {
		return false, fmt.Errorf("failed to decode txid: %w", err)
	}

	utxoEntity, err := db.Utxo.Query().
		Where(entutxo.NetworkEQ(network)).
		Where(entutxo.Txid(txidBytes)).
		Where(entutxo.Vout(vout)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to query utxo confirmation: %w", err)
	}

	return utxoEntity.AvailabilityConfirmedAt != nil, nil
}
