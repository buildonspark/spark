package helper

import (
	"context"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/depositaddress"
	"github.com/lightsparkdev/spark-go/so/ent/signingkeyshare"
)

func CheckUTXOOnchain(ctx context.Context, config *so.Config, utxo *pb.UTXO) bool {
	// Get the on chain tx
	onChainTx, err := common.TxFromRawTxBytes(utxo.RawTx)
	if err != nil {
		return false
	}
	if len(onChainTx.TxOut) <= int(utxo.Vout) {
		return false
	}

	// Verify that the on chain utxo is paid to the registered deposit address
	if len(onChainTx.TxOut) <= int(utxo.Vout) {
		return false
	}
	onChainOutput := onChainTx.TxOut[utxo.Vout]
	network, err := common.NetworkFromProtoNetwork(utxo.Network)
	if err != nil {
		return false
	}
	if !config.IsNetworkSupported(network) {
		return false
	}
	utxoAddress, err := common.P2TRAddressFromPkScript(onChainOutput.PkScript, network)
	if err != nil {
		return false
	}
	db := ent.GetDbFromContext(ctx)
	depositAddress, err := db.DepositAddress.Query().Where(depositaddress.Address(*utxoAddress)).First(ctx)
	if err != nil {
		return false
	}
	if depositAddress == nil {
		return false
	}
	return depositAddress.ConfirmationHeight != 0
}

func CheckOnchainWithKeyshareID(ctx context.Context, keyshareID string) bool {
	db := ent.GetDbFromContext(ctx)
	keyshareUUID, err := uuid.Parse(keyshareID)
	if err != nil {
		return false
	}
	depositAddress, err := db.DepositAddress.Query().Where(
		depositaddress.HasSigningKeyshareWith(
			signingkeyshare.ID(keyshareUUID))).First(ctx)
	if err != nil {
		return false
	}
	if depositAddress == nil {
		return false
	}
	return depositAddress.ConfirmationHeight != 0
}
