package watchtower

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

var (
	meter = otel.Meter("watchtower")

	// Metrics
	nodeTxBroadcastCounter   metric.Int64Counter
	refundTxBroadcastCounter metric.Int64Counter
)

func init() {
	var err error

	nodeTxBroadcastCounter, err = meter.Int64Counter(
		"watchtower.node_tx.broadcast_total",
		metric.WithDescription("Total number of node transactions broadcast by watchtower"),
	)
	if err != nil {
		otel.Handle(err)
		nodeTxBroadcastCounter = noop.Int64Counter{}
	}

	refundTxBroadcastCounter, err = meter.Int64Counter(
		"watchtower.refund_tx.broadcast_total",
		metric.WithDescription("Total number of refund transactions broadcast by watchtower"),
	)
	if err != nil {
		otel.Handle(err)
		refundTxBroadcastCounter = noop.Int64Counter{}
	}
}

type bitcoinClient interface {
	SendRawTransaction(tx *wire.MsgTx, allowHighFees bool) (*chainhash.Hash, error)
}

// BroadcastTransaction broadcasts a transaction to the network
func BroadcastTransaction(ctx context.Context, btcClient bitcoinClient, nodeID string, txBytes []byte) error {
	logger := logging.GetLoggerFromContext(ctx)

	tx, err := common.TxFromRawTxBytes(txBytes)
	if err != nil {
		return fmt.Errorf("watchtower failed to parse transaction for node %s: %w", nodeID, err)
	}
	// TODO: Broadcast Direct Refund TX.
	logger.Sugar().Infof("Attempting to broadcast transaction with txid %s for node %s", tx.TxID(), nodeID)
	txHash, err := btcClient.SendRawTransaction(tx, false)
	if err != nil {
		if alreadyBroadcasted(err) {
			logger.Sugar().Infof("Transaction %s already in mempool for node %s", tx.TxID(), nodeID)
			return nil
		}
		return fmt.Errorf("watchtower failed to broadcast transaction for node %s: %w", nodeID, err)
	}

	logger.Sugar().Infof("Successfully broadcast transaction for %s (txhash: %x)", nodeID, txHash[:])
	return nil
}

// alreadyBroadcast returns true if the given error indicates another SO has already broadcasted the tx.
func alreadyBroadcasted(err error) bool {
	var rpcErr *btcjson.RPCError

	return errors.As(err, &rpcErr) && rpcErr.Code == btcjson.ErrRPCVerifyAlreadyInChain
}

// QueryNodesWithExpiredTimeLocks returns nodes that are eligible for broadcast.
func QueryNodesWithExpiredTimeLocks(ctx context.Context, dbTx *ent.Tx, blockHeight int64, network common.Network) ([]*ent.TreeNode, error) {
	var rootNodes, childNodes, refundNodes []*ent.TreeNode

	// 1. Root nodes needing confirmation
	rootNodes, err := dbTx.TreeNode.Query().
		Where(
			treenode.Not(treenode.HasParent()),
			treenode.Or(
				treenode.NodeConfirmationHeightIsNil(),
				treenode.RefundConfirmationHeightIsNil(),
			),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query root nodes: %w", err)
	}

	// 2. Child nodes whose parent is confirmed but the node itself is not.
	childNodes, err = dbTx.TreeNode.Query().
		Where(
			treenode.HasParentWith(
				treenode.And(
					treenode.NodeConfirmationHeightNotNil(),
					treenode.NodeConfirmationHeightGT(0),
				),
			),
			treenode.NodeConfirmationHeightIsNil(),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query broadcastable child nodes: %w", err)
	}

	// 3. Nodes with confirmed node tx but unconfirmed refund tx.
	refundNodes, err = dbTx.TreeNode.Query().
		Where(
			treenode.NodeConfirmationHeightNotNil(),
			treenode.RefundConfirmationHeightIsNil(),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query refund-pending nodes: %w", err)
	}

	// Deduplicate nodes.
	allNodes := make([]*ent.TreeNode, 0, len(rootNodes)+len(childNodes)+len(refundNodes))
	allNodes = append(allNodes, rootNodes...)
	allNodes = append(allNodes, childNodes...)
	allNodes = append(allNodes, refundNodes...)

	uniqueNodes := make([]*ent.TreeNode, 0, len(allNodes))
	seen := make(map[uuid.UUID]struct{})
	for _, n := range allNodes {
		if _, ok := seen[n.ID]; ok {
			continue
		}
		seen[n.ID] = struct{}{}
		uniqueNodes = append(uniqueNodes, n)
	}

	return uniqueNodes, nil
}

// CheckExpiredTimeLocks checks for TXs with expired time locks and broadcasts them if needed.
func CheckExpiredTimeLocks(ctx context.Context, bitcoinClient *rpcclient.Client, node *ent.TreeNode, blockHeight int64, network common.Network) error {
	logger := logging.GetLoggerFromContext(ctx)

	if node.NodeConfirmationHeight == 0 {
		nodeTx, err := common.TxFromRawTxBytes(node.RawTx)
		if err != nil {
			return fmt.Errorf("watchtower failed to parse node tx for node %s: %w", node.ID.String(), err)
		}
		// Check if node TX has a timelock and has parent
		if nodeTx.TxIn[0].Sequence <= 0xFFFFFFFE {
			// Check if parent is confirmed and timelock has expired
			parent, err := node.QueryParent().Only(ctx)
			if ent.IsNotFound(err) {
				// Exit gracefully if the node is a root node and has no parent
				return nil
			} else if err != nil {
				return fmt.Errorf("watchtower failed to query parent for node %s: %w", node.ID.String(), err)
			}
			if parent.NodeConfirmationHeight > 0 {
				timelockExpiryHeight := uint64(nodeTx.TxIn[0].Sequence&0xFFFF) + parent.NodeConfirmationHeight
				if len(node.DirectTx) > 0 && timelockExpiryHeight+spark.WatchtowerTimeLockBuffer <= uint64(blockHeight) {
					if err := BroadcastTransaction(ctx, bitcoinClient, node.ID.String(), node.DirectTx); err != nil {
						// Record node tx broadcast failure
						if nodeTxBroadcastCounter != nil {
							nodeTxBroadcastCounter.Add(ctx, 1, metric.WithAttributes(
								attribute.String("network", network.String()),
								attribute.String("result", "failure"),
							))
						}
						logger.With(zap.Error(err)).Sugar().Infof("Failed to broadcast node tx for node %s", node.ID)
						return fmt.Errorf("watchtower failed to broadcast node tx for node %s: %w", node.ID.String(), err)
					}

					// Record successful node tx broadcast
					if nodeTxBroadcastCounter != nil {
						nodeTxBroadcastCounter.Add(ctx, 1, metric.WithAttributes(
							attribute.String("network", network.String()),
							attribute.String("result", "success"),
						))
					}
				}
			}
		}
	} else if len(node.RawRefundTx) > 0 && node.RefundConfirmationHeight == 0 {
		refundTx, err := common.TxFromRawTxBytes(node.RawRefundTx)
		if err != nil {
			return fmt.Errorf("watchtower failed to parse refund tx for node %s: %w", node.ID.String(), err)
		}

		timelockExpiryHeight := uint64(refundTx.TxIn[0].Sequence&0xFFFF) + node.NodeConfirmationHeight
		if len(node.DirectRefundTx) > 0 && timelockExpiryHeight+spark.WatchtowerTimeLockBuffer <= uint64(blockHeight) {
			if err := BroadcastTransaction(ctx, bitcoinClient, node.ID.String(), node.DirectRefundTx); err != nil {
				// Try broadcasting the DirectFromCpfpRefundTx as a fallback
				if len(node.DirectFromCpfpRefundTx) > 0 {
					if err := BroadcastTransaction(ctx, bitcoinClient, node.ID.String(), node.DirectFromCpfpRefundTx); err != nil {
						// Record refund tx broadcast failure
						if refundTxBroadcastCounter != nil {
							refundTxBroadcastCounter.Add(ctx, 1, metric.WithAttributes(
								attribute.String("network", network.String()),
								attribute.String("result", "failure"),
							))
						}
						logger.With(zap.Error(err)).Sugar().Infof(
							"Failed to broadcast both direct refund tx and direct from cpfp refund tx for node %s",
							node.ID,
						)
						return fmt.Errorf("watchtower failed to broadcast refund txs for node %s: %w", node.ID.String(), err)
					}
					// Record successful refund tx broadcast
					if refundTxBroadcastCounter != nil {
						refundTxBroadcastCounter.Add(ctx, 1, metric.WithAttributes(
							attribute.String("network", network.String()),
							attribute.String("result", "success"),
						))
					}
					return nil
				}
				// Record refund tx broadcast failure if no DirectFromCpfpRefundTx available
				if refundTxBroadcastCounter != nil {
					refundTxBroadcastCounter.Add(ctx, 1, metric.WithAttributes(
						attribute.String("network", network.String()),
						attribute.String("result", "failure"),
					))
				}
				logger.With(zap.Error(err)).Sugar().Infof("Failed to broadcast direct refund tx for node %s", node.ID)
				return fmt.Errorf("watchtower failed to broadcast refund tx for node %s: %w", node.ID.String(), err)
			}

			// Record successful refund tx broadcast
			if refundTxBroadcastCounter != nil {
				refundTxBroadcastCounter.Add(ctx, 1, metric.WithAttributes(
					attribute.String("network", network.String()),
					attribute.String("result", "success"),
				))
			}
		}
	}

	return nil
}
