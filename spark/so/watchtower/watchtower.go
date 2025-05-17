package watchtower

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/so/ent"
)

// BroadcastTransaction broadcasts a transaction to the network
func BroadcastTransaction(ctx context.Context, bitcoinClient *rpcclient.Client, nodeID string, txBytes []byte) error {
	tx, err := common.TxFromRawTxBytes(txBytes)
	if err != nil {
		return fmt.Errorf("failed to parse transaction: %v", err)
	}

	// TODO: Broadcast Direct Refund TX.
	txHash, err := bitcoinClient.SendRawTransaction(tx, false)
	if err != nil {
		if rpcErr, ok := err.(*btcjson.RPCError); ok && rpcErr.Code == -27 {
			// This means another SO has already broadcasted the tx
			slog.InfoContext(ctx, "Transaction already in mempool", "node_id", nodeID)
			return nil
		}
		return fmt.Errorf("failed to broadcast transaction: %v", err)
	}

	slog.InfoContext(ctx, "Successfully broadcast transaction", "tx_hash", hex.EncodeToString(txHash[:]))
	return nil
}

// CheckExpiredTimeLocks checks for TXs with expired time locks and broadcasts them if needed.
func CheckExpiredTimeLocks(ctx context.Context, bitcoinClient *rpcclient.Client, node *ent.TreeNode, blockHeight int64) error {
	if node.NodeConfirmationHeight == 0 {
		nodeTx, err := common.TxFromRawTxBytes(node.RawTx)
		if err != nil {
			return fmt.Errorf("failed to parse node tx: %v", err)
		}
		// Check if node TX has a timelock and has parent
		if nodeTx.TxIn[0].Sequence <= 0xFFFFFFFE {
			// Check if parent is confirmed and timelock has expired
			parent, err := node.QueryParent().Only(ctx)
			if err != nil {
				return fmt.Errorf("failed to query parent: %v", err)
			}
			if parent.NodeConfirmationHeight > 0 {
				timelockExpiryHeight := uint64(nodeTx.TxIn[0].Sequence&0xFFFF) + parent.NodeConfirmationHeight
				if timelockExpiryHeight <= uint64(blockHeight) {
					if err := BroadcastTransaction(ctx, bitcoinClient, node.ID.String(), node.RawTx); err != nil {
						return fmt.Errorf("failed to broadcast node tx: %v", err)
					}
				}
			}
		}
	} else if len(node.RawRefundTx) > 0 {
		refundTx, err := common.TxFromRawTxBytes(node.RawRefundTx)
		if err != nil {
			return fmt.Errorf("failed to parse refund tx: %v", err)
		}

		timelockExpiryHeight := uint64(refundTx.TxIn[0].Sequence&0xFFFF) + node.NodeConfirmationHeight
		if timelockExpiryHeight <= uint64(blockHeight) {
			if err := BroadcastTransaction(ctx, bitcoinClient, node.ID.String(), node.RawRefundTx); err != nil {
				return fmt.Errorf("failed to broadcast refund tx: %v", err)
			}
		}
	}

	return nil
}
