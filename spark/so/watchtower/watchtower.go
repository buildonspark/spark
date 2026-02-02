package watchtower

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"go.uber.org/zap"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/transferleaf"
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
func BroadcastTransaction(ctx context.Context, btcClient bitcoinClient, nodeID uuid.UUID, txBytes []byte) error {
	logger := logging.GetLoggerFromContext(ctx)

	tx, err := common.TxFromRawTxBytes(txBytes)
	if err != nil {
		return fmt.Errorf("watchtower failed to parse transaction for node %s: %w", nodeID, err)
	}
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

// QueryBroadcastableNodes returns nodes that are eligible for broadcast.
func QueryBroadcastableNodes(ctx context.Context, dbClient *ent.Client, blockHeight int64, network btcnetwork.Network) ([]*ent.TreeNode, error) {
	var childNodes, refundNodes []*ent.TreeNode

	// 1. Child nodes whose parent is confirmed but the node itself is not.
	childNodes, err := dbClient.TreeNode.Query().
		Where(
			treenode.HasParentWith(
				treenode.NodeConfirmationHeightNotNil(),
				treenode.NodeConfirmationHeightGT(0),
			),
			treenode.NodeConfirmationHeightIsNil(),
			treenode.NetworkEQ(network),
		).
		WithParent().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query broadcastable child nodes: %w", err)
	}

	// 2. Nodes with confirmed node tx but unconfirmed refund tx.
	refundNodes, err = dbClient.TreeNode.Query().
		Where(
			treenode.NodeConfirmationHeightNotNil(),
			treenode.RefundConfirmationHeightIsNil(),
			treenode.NetworkEQ(network),
		).
		WithParent().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query refund-pending nodes: %w", err)
	}

	// Deduplicate nodes.
	allNodes := slices.Concat(childNodes, refundNodes)

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

// QueryBroadcastableTransferLeaves returns transfer leaves that are eligible for broadcast.
func QueryBroadcastableTransferLeaves(ctx context.Context, dbClient *ent.Client, network btcnetwork.Network) ([]*ent.TransferLeaf, error) {
	eligibleNodeIDs, err := dbClient.TreeNode.Query().
		Where(
			treenode.NodeConfirmationHeightNotNil(),
			treenode.RefundConfirmationHeightIsNil(),
			treenode.NetworkEQ(network),
		).
		IDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query eligible tree nodes: %w", err)
	}

	if len(eligibleNodeIDs) == 0 {
		return []*ent.TransferLeaf{}, nil
	}

	excludedStatuses := []st.TransferStatus{
		st.TransferStatusCompleted,
		st.TransferStatusReturned,
		st.TransferStatusExpired,
	}

	transferLeaves, err := dbClient.TransferLeaf.Query().
		Where(
			transferleaf.HasLeafWith(treenode.IDIn(eligibleNodeIDs...)),
			transferleaf.HasTransferWith(transfer.StatusNotIn(excludedStatuses...)),
		).
		WithLeaf().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query transfer leaves for eligible nodes: %w", err)
	}

	return transferLeaves, nil
}

// CheckExpiredTimeLocks checks for TXs with expired time locks and broadcasts them if needed.
func CheckExpiredTimeLocks(ctx context.Context, bitcoinClient *rpcclient.Client, node *ent.TreeNode, blockHeight int64, network btcnetwork.Network) error {

	if len(node.DirectTx) > 0 && node.NodeConfirmationHeight == 0 {
		return checkAndBroadcastNodeTx(ctx, bitcoinClient, node, network, blockHeight)
	}

	if node.NodeConfirmationHeight > 0 && node.RefundConfirmationHeight == 0 {
		return checkAndBroadcastRefundTx(ctx, bitcoinClient, node, blockHeight, network)
	}

	return nil
}

func checkAndBroadcastNodeTx(ctx context.Context, bitcoinClient *rpcclient.Client, node *ent.TreeNode, network btcnetwork.Network, blockHeight int64) error {
	// Sanity check since we cast this to uint64 later.
	if blockHeight < 0 {
		return fmt.Errorf("watchtower invalid block height: %d", blockHeight)
	}

	logger := logging.GetLoggerFromContext(ctx)

	directTx, err := common.TxFromRawTxBytes(node.DirectTx)
	if err != nil {
		return fmt.Errorf("watchtower failed to parse node tx for node %s: %w", node.ID, err)
	}

	if len(directTx.TxIn) != 1 {
		return fmt.Errorf("watchtower invalid node tx for node %s: expected 1 input, got %d", node.ID, len(directTx.TxIn))
	}

	sequence := directTx.TxIn[0].Sequence

	// Check if bit 31 is set (SequenceLockTimeDisabled). If so, timelock is disabled.
	if (sequence & wire.SequenceLockTimeDisabled) != 0 {
		return fmt.Errorf("watchtower invalid node tx for node %s: timelock disabled", node.ID)
	}

	// Verify it is a block-based relative timelock (bit 22 is NOT set)
	if (sequence & wire.SequenceLockTimeIsSeconds) != 0 {
		return fmt.Errorf("watchtower invalid node tx for node %s: expected block-based timelock, got time-based", node.ID)
	}

	parent := node.Edges.Parent
	if parent == nil {
		var err error
		parent, err = node.QueryParent().Only(ctx)
		if ent.IsNotFound(err) {
			logger.With(zap.Error(err)).Sugar().Infof("No parent found for node %s, skipping", node.ID)
			return nil
		} else if err != nil {
			return fmt.Errorf("watchtower failed to query parent for node %s: %w", node.ID, err)
		}
	}

	if parent.NodeConfirmationHeight > 0 {
		timelockExpiryHeight := uint64(directTx.TxIn[0].Sequence&wire.SequenceLockTimeMask) + parent.NodeConfirmationHeight
		if timelockExpiryHeight <= uint64(blockHeight) {
			if err := broadcastWithMetric(ctx, bitcoinClient, node.ID, node.DirectTx, network, nodeTxBroadcastCounter); err != nil {
				logger.With(zap.Error(err)).Sugar().Infof("Failed to broadcast node tx for node %s", node.ID)
				return fmt.Errorf("watchtower failed to broadcast node tx for node %s: %w", node.ID, err)
			}
		}
	}
	return nil
}

func checkAndBroadcastRefundTx(ctx context.Context, bitcoinClient *rpcclient.Client, node *ent.TreeNode, blockHeight int64, network btcnetwork.Network) error {
	// Sanity check since we cast this to uint64 later.
	if blockHeight < 0 {
		return fmt.Errorf("watchtower invalid block height: %d", blockHeight)
	}

	logger := logging.GetLoggerFromContext(ctx)

	candidates := [][]byte{node.DirectRefundTx, node.DirectFromCpfpRefundTx}

	var lastErr error
	attempted := false

	// Attempt to broadcast direct refund tx first, then direct from CPFP refund tx.
	for _, txBytes := range candidates {
		if len(txBytes) == 0 {
			continue
		}

		tx, err := common.TxFromRawTxBytes(txBytes)
		if err != nil {
			attempted = true
			logger.With(zap.Error(err)).Sugar().Infof("Failed to parse refund candidate for node %s, trying next", node.ID)
			lastErr = err
			continue
		}

		if len(tx.TxIn) == 0 {
			attempted = true
			err := fmt.Errorf("watchtower refund tx has no inputs for node %s", node.ID)
			logger.With(zap.Error(err)).Sugar().Infof("Invalid refund candidate for node %s, trying next", node.ID)
			lastErr = err
			continue
		}

		sequence := tx.TxIn[0].Sequence

		// Check if bit 31 is set (SequenceLockTimeDisabled). If so, timelock is disabled.
		if (sequence & wire.SequenceLockTimeDisabled) != 0 {
			attempted = true
			lastErr = fmt.Errorf("watchtower invalid refund tx for node %s: timelock disabled", node.ID)
			logger.With(zap.Error(lastErr)).Sugar().Infof("Invalid refund candidate for node %s, trying next", node.ID)
			continue
		}

		// Verify it is a block-based relative timelock (bit 22 is NOT set)
		if (sequence & wire.SequenceLockTimeIsSeconds) != 0 {
			attempted = true
			lastErr = fmt.Errorf("watchtower invalid refund tx for node %s: expected block-based timelock, got time-based", node.ID)
			logger.With(zap.Error(lastErr)).Sugar().Infof("Invalid refund candidate for node %s, trying next", node.ID)
			continue
		}

		// If we reached here, the candidate passed validation. We clear any previous lastErr
		// so that we don't return an error if we simply end up waiting for this valid candidate to expire.
		lastErr = nil

		timelockExpiryHeight := uint64(sequence&wire.SequenceLockTimeMask) + node.NodeConfirmationHeight
		if timelockExpiryHeight <= uint64(blockHeight) {
			attempted = true
			err := broadcastWithMetric(ctx, bitcoinClient, node.ID, txBytes, network, refundTxBroadcastCounter)
			if err == nil {
				return nil
			}

			logger.With(zap.Error(err)).Sugar().Infof("Failed to broadcast refund candidate for node %s", node.ID)
			lastErr = err
		}
	}

	if attempted && lastErr != nil {
		return fmt.Errorf("watchtower failed to broadcast any refund tx for node %s: %w", node.ID, lastErr)
	}
	return nil
}

func broadcastWithMetric(
	ctx context.Context,
	btcClient bitcoinClient,
	nodeID uuid.UUID,
	txBytes []byte,
	network btcnetwork.Network,
	counter metric.Int64Counter,
) error {
	err := BroadcastTransaction(ctx, btcClient, nodeID, txBytes)
	if err != nil {
		if counter != nil {
			counter.Add(ctx, 1, metric.WithAttributes(
				attribute.String("network", network.String()),
				attribute.String("result", "failure"),
			))
		}
		return err
	}

	if counter != nil {
		counter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("network", network.String()),
			attribute.String("result", "success"),
		))
	}
	return nil
}

// BroadcastTransferLeafRefund attempts to broadcast the refund transactions for a transfer leaf.
// The intermediate refund timelocks are relative to nodeConfirmationHeight,
// the block height at which the leaf's Spark node was confirmed.
// The timelock is expired when blockHeight >= nodeConfirmationHeight + timelock.
func BroadcastTransferLeafRefund(ctx context.Context, bitcoinClient *rpcclient.Client, transferLeaf *ent.TransferLeaf, nodeConfirmationHeight uint64, network btcnetwork.Network, blockHeight int64) error {
	logger := logging.GetLoggerFromContext(ctx)

	// A confirmed node has confirmation height much greater than zero.
	if nodeConfirmationHeight == 0 {
		return nil
	}

	directRefundExpiryHeight := nodeConfirmationHeight + transferLeaf.IntermediateDirectRefundTimelock
	directRefundTimelockExpired := transferLeaf.IntermediateDirectRefundTimelock > 0 && directRefundExpiryHeight <= uint64(blockHeight)

	directFromCpfpRefundExpiryHeight := nodeConfirmationHeight + transferLeaf.IntermediateDirectFromCpfpRefundTimelock
	directFromCpfpRefundTimelockExpired := transferLeaf.IntermediateDirectFromCpfpRefundTimelock > 0 && directFromCpfpRefundExpiryHeight <= uint64(blockHeight)

	// If neither timelock is expired, return early
	if !directRefundTimelockExpired && !directFromCpfpRefundTimelockExpired {
		return nil
	}

	var broadcastErr error

	if directRefundTimelockExpired && len(transferLeaf.IntermediateDirectRefundTx) > 0 {
		broadcastErr = BroadcastTransaction(ctx, bitcoinClient, transferLeaf.ID, transferLeaf.IntermediateDirectRefundTx)
		if broadcastErr == nil {
			if refundTxBroadcastCounter != nil {
				refundTxBroadcastCounter.Add(ctx, 1, metric.WithAttributes(
					attribute.String("network", network.String()),
					attribute.String("result", "success"),
				))
			}
			return nil
		}
		logger.With(zap.Error(broadcastErr)).Sugar().Infof("Failed to broadcast intermediate direct refund tx for transfer leaf %s, trying fallback", transferLeaf.ID)
	}

	if directFromCpfpRefundTimelockExpired && len(transferLeaf.IntermediateDirectFromCpfpRefundTx) > 0 {
		broadcastErr = BroadcastTransaction(ctx, bitcoinClient, transferLeaf.ID, transferLeaf.IntermediateDirectFromCpfpRefundTx)
		if broadcastErr == nil {
			if refundTxBroadcastCounter != nil {
				refundTxBroadcastCounter.Add(ctx, 1, metric.WithAttributes(
					attribute.String("network", network.String()),
					attribute.String("result", "success"),
				))
			}
			return nil
		}
	}

	if refundTxBroadcastCounter != nil {
		refundTxBroadcastCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("network", network.String()),
			attribute.String("result", "failure"),
		))
	}
	logger.With(zap.Error(broadcastErr)).Sugar().Infof("Failed to broadcast refund txs for transfer leaf %s", transferLeaf.ID)
	return fmt.Errorf("watchtower failed to broadcast refund txs for transfer leaf %s: %w", transferLeaf.ID.String(), broadcastErr)
}
