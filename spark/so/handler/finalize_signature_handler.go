package handler

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/logging"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/blockheight"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	entutxo "github.com/lightsparkdev/spark/so/ent/utxo"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/tree"
	"github.com/lightsparkdev/spark/so/utils"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// FinalizeSignatureHandler is the handler for the FinalizeNodeSignatures RPC.
type FinalizeSignatureHandler struct {
	config *so.Config
}

const maxFinalizeNodeSignatures = MaxLeavesToSend

// NewFinalizeSignatureHandler creates a new FinalizeSignatureHandler.
func NewFinalizeSignatureHandler(config *so.Config) *FinalizeSignatureHandler {
	return &FinalizeSignatureHandler{config: config}
}

// FinalizeNodeSignaturesV2 verifies the node signatures and updates the node.
func (o *FinalizeSignatureHandler) FinalizeNodeSignaturesV2(ctx context.Context, req *pb.FinalizeNodeSignaturesRequest) (*pb.FinalizeNodeSignaturesResponse, error) {
	return o.finalizeNodeSignatures(ctx, req, true)
}

// FinalizeNodeSignatures verifies the node signatures and updates the node.
func (o *FinalizeSignatureHandler) FinalizeNodeSignatures(ctx context.Context, req *pb.FinalizeNodeSignaturesRequest) (*pb.FinalizeNodeSignaturesResponse, error) {
	return o.finalizeNodeSignatures(ctx, req, false)
}

// FinalizeNodeSignatures verifies the node signatures and updates the node.
func (o *FinalizeSignatureHandler) finalizeNodeSignatures(ctx context.Context, req *pb.FinalizeNodeSignaturesRequest, requireDirectTx bool) (*pb.FinalizeNodeSignaturesResponse, error) {
	if req.GetIntent() == pbcommon.SignatureIntent_REFRESH || req.GetIntent() == pbcommon.SignatureIntent_EXTEND {
		return nil, fmt.Errorf("operation has been deprecated: %s", req.GetIntent())
	}

	if len(req.GetNodeSignatures()) > maxFinalizeNodeSignatures {
		return nil, sparkerrors.InvalidArgumentOutOfRange(
			fmt.Errorf("too many node signatures in request: got %d, max %d", len(req.GetNodeSignatures()), maxFinalizeNodeSignatures),
		)
	}

	if len(req.GetNodeSignatures()) == 0 {
		return &pb.FinalizeNodeSignaturesResponse{Nodes: []*pb.TreeNode{}}, nil
	}

	if err := o.validateNodeOwnership(ctx, req); err != nil {
		return nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	var nodeTree *ent.Tree
	// For CREATION intent, verify ALL nodes belong to the same tree before processing.
	// This prevents attacks where nodes from different trees (built from different
	// outputs of the same transaction) are submitted together to bypass validation.
	if req.GetIntent() == pbcommon.SignatureIntent_CREATION {
		nodeIDs := make([]uuid.UUID, 0, len(req.GetNodeSignatures()))
		for _, nodeSignatures := range req.GetNodeSignatures() {
			nodeID, err := uuid.Parse(nodeSignatures.GetNodeId())
			if err != nil {
				return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid node id in request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err))
			}
			nodeIDs = append(nodeIDs, nodeID)
		}
		treeNodes, err := db.TreeNode.Query().Where(treenode.IDIn(nodeIDs...)).WithTree().All(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get nodes for request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err)
		}
		if len(treeNodes) != len(nodeIDs) {
			return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("not all nodes found: expected %d, got %d", len(nodeIDs), len(treeNodes)))
		}
		nodeTree = treeNodes[0].Edges.Tree
		if nodeTree == nil {
			return nil, fmt.Errorf("failed to get tree for first node %s", treeNodes[0].ID)
		}
		for _, node := range treeNodes[1:] {
			if node.Edges.Tree == nil || node.Edges.Tree.ID != nodeTree.ID {
				return nil, fmt.Errorf("node %s does not belong to the same tree as first node", node.ID)
			}
		}

		if nodeTree.Status == st.TreeStatusPending {
			for _, nodeSignatures := range req.GetNodeSignatures() {
				nodeID, err := uuid.Parse(nodeSignatures.GetNodeId())
				if err != nil {
					return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid node id in request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err))
				}
				node, err := db.TreeNode.Get(ctx, nodeID)
				if err != nil {
					if ent.IsNotFound(err) {
						return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("failed to get node for request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err))
					}
					return nil, fmt.Errorf("failed to get node for request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err)
				}
				signingKeyshare, err := node.QuerySigningKeyshare().Only(ctx)
				if err != nil {
					return nil, fmt.Errorf("failed to get signing keyshare: %w", err)
				}
				address, err := db.DepositAddress.Query().Where(depositaddress.HasSigningKeyshareWith(signingkeyshare.IDEQ(signingKeyshare.ID))).Only(ctx)
				if err != nil {
					return nil, fmt.Errorf("failed to get deposit address: %w", err)
				}
				if address.ConfirmationHeight != 0 {
					blockHeight, err := db.BlockHeight.Query().
						Where(blockheight.NetworkEQ(address.Network)).
						Order(ent.Desc(blockheight.FieldHeight)).
						First(ctx)
					if err != nil {
						if ent.IsNotFound(err) {
							return nil, fmt.Errorf("no block height present in db; cannot determine number of confirmations")
						}
						return nil, fmt.Errorf("failed to get max block height: %w", err)
					}
					numConfirmations := blockHeight.Height - address.ConfirmationHeight
					requiredConfirmations := int64(knobs.GetKnobsService(ctx).GetValue(knobs.KnobNumRequiredConfirmations, 3))
					if numConfirmations >= requiredConfirmations {
						if len(address.ConfirmationTxid) > 0 && address.ConfirmationTxid != nodeTree.BaseTxid.String() {
							return nil, fmt.Errorf("confirmation txid does not match tree base txid")
						}
						_, err = nodeTree.Update().SetStatus(st.TreeStatusAvailable).Save(ctx)
						if err != nil {
							return nil, fmt.Errorf("failed to update tree: %w", err)
						}
					}
					break
				}
			}
		}
	}

	var transfer *ent.Transfer
	if req.GetIntent() == pbcommon.SignatureIntent_TRANSFER {
		transfer, err = o.verifyAndUpdateTransfer(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("failed to verify and update transfer for request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err)
		}
	}

	nodes, internalNodes, err := o.updateNodesFromSignatures(ctx, req.GetNodeSignatures(), req.GetIntent(), requireDirectTx)
	if err != nil {
		return nil, fmt.Errorf("failed to update node for request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err)
	}

	// Send gossip message to other SOs
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	participants, err := selection.OperatorIdentifierList(o.config)
	if err != nil {
		return nil, fmt.Errorf("unable to get operator list: %w", err)
	}
	sendGossipHandler := NewSendGossipHandler(o.config)

	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Sending finalize node signatures gossip message (intent: %s)", req.GetIntent())

	switch req.GetIntent() {
	case pbcommon.SignatureIntent_CREATION:
		protoNetwork, err := nodeTree.Network.ToProtoNetwork()
		if err != nil {
			return nil, err
		}

		logger.Info("Sending finalize tree creation gossip message")
		_, err = sendGossipHandler.CreateCommitAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
			Message: &pbgossip.GossipMessage_FinalizeTreeCreation{
				FinalizeTreeCreation: &pbgossip.GossipMessageFinalizeTreeCreation{
					InternalNodes: internalNodes,
					ProtoNetwork:  protoNetwork,
				},
			},
		}, participants)
		if err != nil {
			return nil, fmt.Errorf("unable to create and send gossip message: %w", err)
		}

	case pbcommon.SignatureIntent_TRANSFER:
		transferID := transfer.ID.String()
		completionTimestamp := timestamppb.New(*transfer.CompletionTime)

		logger.Sugar().Infof("Sending finalize transfer gossip message for transfer %s", transferID)

		_, err = sendGossipHandler.CreateCommitAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
			Message: &pbgossip.GossipMessage_FinalizeTransfer{
				FinalizeTransfer: &pbgossip.GossipMessageFinalizeTransfer{
					TransferId:          transferID,
					InternalNodes:       internalNodes,
					CompletionTimestamp: completionTimestamp,
				},
			},
		}, participants)
		if err != nil {
			return nil, fmt.Errorf("unable to create and send gossip message: %w", err)
		}
	default:
		return nil, fmt.Errorf("invalid intent %s", req.GetIntent())
	}
	return &pb.FinalizeNodeSignaturesResponse{Nodes: nodes}, nil
}

func (o *FinalizeSignatureHandler) validateNodeOwnership(ctx context.Context, req *pb.FinalizeNodeSignaturesRequest) error {
	if !o.config.IsAuthzEnforced() {
		return nil
	}

	nodeIDs := make([]uuid.UUID, 0, len(req.GetNodeSignatures()))
	for _, nodeSignatures := range req.GetNodeSignatures() {
		nodeID, err := uuid.Parse(nodeSignatures.GetNodeId())
		if err != nil {
			return fmt.Errorf("invalid node id in request: %w", err)
		}
		nodeIDs = append(nodeIDs, nodeID)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	nodes, err := db.TreeNode.Query().Where(treenode.IDIn(nodeIDs...)).All(ctx)
	if err != nil {
		return fmt.Errorf("failed to get nodes: %w", err)
	}

	session, err := authn.GetSessionFromContext(ctx)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if !node.OwnerIdentityPubkey.Equals(session.IdentityPublicKey()) {
			return fmt.Errorf("node %s is not owned by the authenticated identity public key %x", node.ID, session.IdentityPublicKey())
		}
	}
	return nil
}

func (o *FinalizeSignatureHandler) verifyDepositBackedRootNodeSignature(ctx context.Context, node *ent.TreeNode, treeEnt *ent.Tree, signedRootTxBytes []byte) error {
	depositAddress, err := treeEnt.QueryDepositAddress().Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to query deposit address for root node %s: %w", node.ID, err)
	}

	signedRootTx, err := common.TxFromRawTxBytes(signedRootTxBytes)
	if err != nil {
		return fmt.Errorf("unable to deserialize root node tx: %w", err)
	}
	if len(signedRootTx.TxIn) == 0 {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("root node tx for node %s must have at least one input", node.ID))
	}
	if treeEnt.Vout < 0 {
		return sparkerrors.InternalDataInconsistency(fmt.Errorf("tree %s has invalid negative vout %d", treeEnt.ID, treeEnt.Vout))
	}
	baseOutpoint := wire.OutPoint{
		Hash:  treeEnt.BaseTxid.Hash(),
		Index: uint32(treeEnt.Vout),
	}

	networkParams, err := treeEnt.Network.Params()
	if err != nil {
		return fmt.Errorf("failed to get network params for tree %s: %w", treeEnt.ID, err)
	}
	address, err := btcutil.DecodeAddress(depositAddress.Address, networkParams)
	if err != nil {
		return fmt.Errorf("failed to decode deposit address %s for root node %s: %w", depositAddress.Address, node.ID, err)
	}
	depositPkScript, err := txscript.PayToAddrScript(address)
	if err != nil {
		return fmt.Errorf("failed to build deposit pkscript for root node %s: %w", node.ID, err)
	}
	if node.Value > uint64(math.MaxInt64) {
		return sparkerrors.InternalDataInconsistency(fmt.Errorf("root node %s value %d exceeds int64 max", node.ID, node.Value))
	}

	prevOuts := make(map[wire.OutPoint]*wire.TxOut, len(signedRootTx.TxIn))
	spendsBaseOutpoint := false
	for inputIndex, txIn := range signedRootTx.TxIn {
		if txIn == nil {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("root node tx input %d is required", inputIndex))
		}
		outpoint := txIn.PreviousOutPoint
		if outpoint == baseOutpoint {
			spendsBaseOutpoint = true
		}

		txidBytes, err := hex.DecodeString(outpoint.Hash.String())
		if err != nil {
			return fmt.Errorf("failed to encode root node tx input %d txid: %w", inputIndex, err)
		}
		utxoEntity, err := depositAddress.QueryUtxo().
			Where(entutxo.Txid(txidBytes)).
			Where(entutxo.Vout(outpoint.Index)).
			Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) && len(signedRootTx.TxIn) == 1 && outpoint == baseOutpoint {
				prevOuts[outpoint] = wire.NewTxOut(int64(node.Value), depositPkScript)
				continue
			}
			if ent.IsNotFound(err) {
				return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("root node tx input %d spends outpoint %s that is not recorded for deposit address %s", inputIndex, outpoint, depositAddress.Address))
			}
			return fmt.Errorf("failed to query root node tx input %d utxo: %w", inputIndex, err)
		}
		if utxoEntity.Amount > uint64(math.MaxInt64) {
			return sparkerrors.InternalDataInconsistency(fmt.Errorf("utxo %s value %d exceeds int64 max", outpoint, utxoEntity.Amount))
		}
		prevOuts[outpoint] = wire.NewTxOut(int64(utxoEntity.Amount), utxoEntity.PkScript)
	}
	if !spendsBaseOutpoint {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("root node tx for node %s must spend tree base outpoint %s", node.ID, baseOutpoint))
	}

	if err := common.ValidateBitcoinTxVersion(signedRootTx); err != nil {
		return fmt.Errorf("root node tx version validation failed: %w", err)
	}
	prevOutFetcher := txscript.NewMultiPrevOutFetcher(prevOuts)
	if len(signedRootTx.TxIn) == 1 {
		err = common.VerifySignatureInput(signedRootTx, 0, prevOutFetcher)
	} else {
		err = common.VerifySignatureMultiInput(signedRootTx, prevOutFetcher)
	}
	if err != nil {
		return sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("unable to verify root node tx signature: %w", err))
	}

	return nil
}

func (o *FinalizeSignatureHandler) verifyAndUpdateTransfer(ctx context.Context, req *pb.FinalizeNodeSignaturesRequest) (*ent.Transfer, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	// Extract leaf IDs from node signatures, rejecting duplicates.
	leafIDs := make([]uuid.UUID, 0, len(req.GetNodeSignatures()))
	leafIDsSeen := make(map[uuid.UUID]struct{}, len(req.GetNodeSignatures()))
	for _, nodeSignatures := range req.GetNodeSignatures() {
		leafID, err := uuid.Parse(nodeSignatures.GetNodeId())
		if err != nil {
			return nil, fmt.Errorf("invalid node id in request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err)
		}
		if _, dup := leafIDsSeen[leafID]; dup {
			return nil, fmt.Errorf("duplicate leaf %s in request", leafID)
		}
		leafIDsSeen[leafID] = struct{}{}
		leafIDs = append(leafIDs, leafID)
	}

	// Convert UUIDs to []any for SQL IN clause
	leafIDsAny := make([]any, len(leafIDs))
	for i, id := range leafIDs {
		leafIDsAny[i] = id
	}

	// Find all ongoing transfers that involves any of these leaves. All these leaves should be
	// part of a **single** transfer so we expect one result.
	transfer, err := db.Transfer.Query().
		Select(enttransfer.FieldID, enttransfer.FieldStatus, enttransfer.FieldReceiverIdentityPubkey).
		Where(
			enttransfer.StatusNotIn(st.TransferStatusCompleted, st.TransferStatusExpired, st.TransferStatusReturned),
			func(s *sql.Selector) {
				// Check transfer_leafs FK directly, avoiding tree_nodes join
				s.Where(sql.Exists(
					sql.Select("transfer_leaf_transfer").
						From(sql.Table("transfer_leafs")).
						Where(sql.ColumnsEQ(
							s.C(enttransfer.FieldID),
							"transfer_leaf_transfer",
						)).
						Where(sql.In("transfer_leaf_leaf", leafIDsAny...)),
				))
			},
		).
		ForUpdate().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("failed to find pending transfer for leaves %s: %w", leafIDs, err))
		}
		return nil, fmt.Errorf("failed to find pending transfer for leaves %s: %w", leafIDs, err)
	}
	if transfer == nil {
		return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("failed to find pending transfer for leaves %s", leafIDs))
	}
	if transfer.Status != st.TransferStatusReceiverRefundSigned {
		return nil, fmt.Errorf("transfer %s is not in receiver refund signed status", transfer.ID)
	}

	session, err := authn.GetSessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !transfer.ReceiverIdentityPubkey.Equals(session.IdentityPublicKey()) {
		return nil, fmt.Errorf("transfer %s is not owned by the authenticated identity public key %s", transfer.ID, session.IdentityPublicKey())
	}

	// Mirror the coop-exit confirmation guard that receiver SOs apply in
	// InternalTransferHandler.FinalizeTransfer. Without this, the coordinator
	// completes the transfer and marks leaves AVAILABLE before the on-chain
	// coop-exit tx has reached the required confirmations, while receivers
	// reject the FinalizeTransfer gossip with FailedPrecondition and stay at
	// TRANSFER_LOCKED — producing permanent state divergence (SP-2961).
	if err := checkCoopExitTxBroadcasted(ctx, db, transfer); err != nil {
		return nil, err
	}

	// Verify that every submitted leaf belongs to this transfer (set equality, not just count).
	transferLeafIDs, err := transfer.QueryTransferLeaves().QueryLeaf().IDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query transfer leaf IDs for transfer %s: %w", transfer.ID, err)
	}
	if len(leafIDs) != len(transferLeafIDs) {
		return nil, fmt.Errorf("signature count %d does not match transfer leaf count %d for transfer %s", len(leafIDs), len(transferLeafIDs), transfer.ID)
	}
	transferLeafIDSet := make(map[uuid.UUID]struct{}, len(transferLeafIDs))
	for _, id := range transferLeafIDs {
		transferLeafIDSet[id] = struct{}{}
	}
	for _, leafID := range leafIDs {
		if _, ok := transferLeafIDSet[leafID]; !ok {
			return nil, fmt.Errorf("leaf %s does not belong to transfer %s", leafID, transfer.ID)
		}
	}
	if err := validateFinalizeNodeSignatureTransferLeafStates(ctx, db, transfer.ID, leafIDs); err != nil {
		return nil, err
	}

	receiverCount, err := transfer.QueryTransferReceivers().Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to count receivers for transfer %s: %w", transfer.ID, err)
	}
	if receiverCount > 1 {
		return nil, fmt.Errorf("transfer %s has %d receivers; FinalizeNodeSignatures does not support multi-receiver transfers", transfer.ID, receiverCount)
	}

	completionTime := time.Now()
	updatedTransfer, err := transfer.Update().SetStatus(st.TransferStatusCompleted).SetCompletionTime(completionTime).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update transfer %s: %w", transfer.ID, err)
	}

	if err := syncReceiversToTerminalStatus(ctx, transfer.ID, st.TransferStatusCompleted, completionTime); err != nil {
		return nil, fmt.Errorf("failed to sync receiver statuses for transfer %s: %w", transfer.ID, err)
	}

	return updatedTransfer, nil
}

func validateFinalizeNodeSignatureTransferLeafStates(ctx context.Context, db *ent.Client, transferID uuid.UUID, leafIDs []uuid.UUID) error {
	leaves, err := db.TreeNode.Query().
		Where(treenode.IDIn(leafIDs...)).
		ForUpdate().
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to lock transfer leaves for transfer %s: %w", transferID, err)
	}
	if len(leaves) != len(leafIDs) {
		return sparkerrors.NotFoundMissingEntity(fmt.Errorf("not all transfer leaves found for transfer %s: expected %d, got %d", transferID, len(leafIDs), len(leaves)))
	}
	for _, leaf := range leaves {
		// Leaves that exited to L1 mid-transfer stay claimable; the finalize
		// path preserves their on-chain status — see claimLeafTweakKey.
		if leaf.Status == st.TreeNodeStatusTransferLocked || leaf.Status == st.TreeNodeStatusAvailable || leaf.Status.IsExitedToL1() {
			continue
		}
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("leaf %s for transfer %s has status %s, expected TRANSFER_LOCKED, AVAILABLE, or exited to L1", leaf.ID, transferID, leaf.Status))
	}
	return nil
}

// loadNodesForFinalize batch-loads every tree node referenced by
// nodeSignatures with the edges the finalize path needs (children, parent,
// tree, signing keyshare), returned in request order. Loading everything up
// front keeps updateLoadedNode free of per-node queries — the consensus claim
// commit runs it for hundreds of leaves inside one request tx, where N+1
// loads dominated large-claim latency.
func loadNodesForFinalize(ctx context.Context, nodeSignatures []*pb.NodeSignatures) ([]*ent.TreeNode, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	nodeIDs := make([]uuid.UUID, 0, len(nodeSignatures))
	seen := make(map[uuid.UUID]struct{}, len(nodeSignatures))
	for _, sig := range nodeSignatures {
		nodeID, err := uuid.Parse(sig.GetNodeId())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid node id in %s: %w", logging.FormatProto("node_signatures", sig), err))
		}
		// Callers reject duplicates upstream; this guard is defense in depth
		// because duplicates would alias one preloaded node and apply the
		// second entry against a stale snapshot of the first's write.
		if _, dup := seen[nodeID]; dup {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("duplicate node id %s in request", nodeID))
		}
		seen[nodeID] = struct{}{}
		nodeIDs = append(nodeIDs, nodeID)
	}

	nodes, err := db.TreeNode.Query().
		Where(treenode.IDIn(nodeIDs...)).
		// Children are only consulted for a has-children check, so load IDs
		// only rather than materializing full child rows.
		WithChildren(func(q *ent.TreeNodeQuery) { q.Select(treenode.FieldID) }).
		WithParent().
		WithTree().
		WithSigningKeyshare().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get nodes: %w", err)
	}
	nodesByID := make(map[uuid.UUID]*ent.TreeNode, len(nodes))
	for _, node := range nodes {
		nodesByID[node.ID] = node
	}

	ordered := make([]*ent.TreeNode, 0, len(nodeSignatures))
	for i, sig := range nodeSignatures {
		node, ok := nodesByID[nodeIDs[i]]
		if !ok {
			return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("failed to get node in %s", logging.FormatProto("node_signatures", sig)))
		}
		ordered = append(ordered, node)
	}
	return ordered, nil
}

// updateNodesFromSignatures applies each NodeSignatures entry to its tree
// node, with all node data batch-loaded once up front and all writes flushed
// in one batch at the end.
func (o *FinalizeSignatureHandler) updateNodesFromSignatures(ctx context.Context, nodeSignatures []*pb.NodeSignatures, intent pbcommon.SignatureIntent, requireDirectTx bool) ([]*pb.TreeNode, []*pbinternal.TreeNode, error) {
	loadedNodes, err := loadNodesForFinalize(ctx, nodeSignatures)
	if err != nil {
		return nil, nil, err
	}
	// Microsecond precision so the marshaled timestamps equal what Postgres
	// stores.
	updateTime := utils.ToMicrosecondPrecision(time.Now().UTC())
	statusGroups := make(map[st.TreeNodeStatus][]uuid.UUID)
	for i, sig := range nodeSignatures {
		newStatus, err := o.updateLoadedNode(ctx, sig, loadedNodes[i], intent, requireDirectTx, updateTime)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to update node %s: %w", sig.GetNodeId(), err)
		}
		if newStatus != nil {
			statusGroups[*newStatus] = append(statusGroups[*newStatus], loadedNodes[i].ID)
		}
	}
	if err := persistFinalizedNodes(ctx, loadedNodes, statusGroups, updateTime); err != nil {
		return nil, nil, err
	}

	nodes := make([]*pb.TreeNode, 0, len(nodeSignatures))
	internalNodes := make([]*pbinternal.TreeNode, 0, len(nodeSignatures))
	for _, node := range loadedNodes {
		nodeSparkProto, err := node.MarshalSparkProto(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to marshal node %s on spark: %w", node.ID, err)
		}
		internalNode, err := node.MarshalInternalProto(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to marshal node %s on internal: %w", node.ID, err)
		}
		nodes = append(nodes, nodeSparkProto)
		internalNodes = append(internalNodes, internalNode)
	}
	return nodes, internalNodes, nil
}

// persistFinalizedNodes flushes the in-memory finalize mutations with chunked
// CreateBulk+OnConflict upserts (ent's bulk-UPDATE workaround). The rows are
// row-locked and counted first so the conflict path always takes the UPDATE
// branch — without that, a node deleted by a concurrent transaction would be
// resurrected through the INSERT branch with its status applied guard-free.
// Status is deliberately excluded from the upsert and applied through grouped
// Update statements instead: the AVAILABLE-transition guard only fires on
// update mutations, and it must keep vetting every transition (SP-3049).
func persistFinalizedNodes(ctx context.Context, nodes []*ent.TreeNode, statusGroups map[st.TreeNodeStatus][]uuid.UUID, updateTime time.Time) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db: %w", err)
	}
	idSet := make(map[uuid.UUID]struct{}, len(nodes))
	for _, node := range nodes {
		idSet[node.ID] = struct{}{}
	}
	for _, ids := range statusGroups {
		for _, id := range ids {
			idSet[id] = struct{}{}
		}
	}
	lockedStatus := make(map[uuid.UUID]st.TreeNodeStatus, len(idSet))
	if len(idSet) > 0 {
		nodeIDs := make([]uuid.UUID, 0, len(idSet))
		for id := range idSet {
			nodeIDs = append(nodeIDs, id)
		}
		// Deterministic order to avoid lock-order deadlocks between
		// concurrent batch lockers.
		lockedRows, err := db.TreeNode.Query().
			Where(treenode.IDIn(nodeIDs...)).
			Order(ent.Asc(treenode.FieldID)).
			ForUpdate().
			Select(treenode.FieldID, treenode.FieldStatus).
			All(ctx)
		if err != nil {
			return fmt.Errorf("unable to lock tree nodes for finalize: %w", err)
		}
		if len(lockedRows) != len(nodeIDs) {
			return sparkerrors.NotFoundMissingEntity(fmt.Errorf(
				"expected %d tree nodes to finalize but locked %d", len(nodeIDs), len(lockedRows)))
		}
		for _, row := range lockedRows {
			lockedStatus[row.ID] = row.Status
		}
	}
	// Re-check the planned transitions against the now-locked statuses: the
	// groups were computed from an unlocked read, and the schema guard only
	// vets transitions to AVAILABLE — without this, a terminal transition
	// committed since the read (e.g. the chain watcher exiting a leaf to L1)
	// would be overwritten with a stale SPLITTED. Same-status writes stay
	// permitted so redelivered finalizes remain idempotent.
	for status, ids := range statusGroups {
		for _, id := range ids {
			current := lockedStatus[id]
			if current != status && !current.CanBecomeAvailable() {
				return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
					"tree node %s in terminal status %s cannot transition to %s", id, current, status))
			}
		}
	}
	builders := make([]*ent.TreeNodeCreate, 0, len(nodes))
	for _, node := range nodes {
		builders = append(builders, db.TreeNode.Create().
			SetID(node.ID).
			SetTree(node.Edges.Tree).
			// The tree's network, matching applyTreeNodeOwnerKeyUpdates: the
			// column is not in the conflict update set, and the create hook's
			// network check must not start rejecting stored rows the old
			// update path accepted.
			SetNetwork(node.Edges.Tree.Network).
			SetSigningKeyshare(node.Edges.SigningKeyshare).
			SetValue(node.Value).
			SetVerifyingPubkey(node.VerifyingPubkey).
			SetOwnerIdentityPubkey(node.OwnerIdentityPubkey).
			SetOwnerSigningPubkey(node.OwnerSigningPubkey).
			SetVout(node.Vout).
			SetStatus(node.Status).
			SetUpdateTime(node.UpdateTime).
			SetRawTx(node.RawTx).
			SetRawRefundTx(node.RawRefundTx).
			SetDirectTx(node.DirectTx).
			SetDirectRefundTx(node.DirectRefundTx).
			SetDirectFromCpfpRefundTx(node.DirectFromCpfpRefundTx))
	}
	const maxBatchSize = 1000
	for chunk := range slices.Chunk(builders, maxBatchSize) {
		if err := db.TreeNode.CreateBulk(chunk...).
			OnConflictColumns(treenode.FieldID).
			Update(func(u *ent.TreeNodeUpsert) {
				u.UpdateRawTx()
				u.UpdateRawRefundTx()
				u.UpdateDirectTx()
				u.UpdateDirectRefundTx()
				u.UpdateDirectFromCpfpRefundTx()
				// The create hooks computed txids from this batch's tx bytes;
				// carry them so the indexed columns track the new txs.
				u.UpdateRawTxid()
				u.UpdateRawRefundTxid()
				u.UpdateDirectTxid()
				u.UpdateDirectRefundTxid()
				u.UpdateDirectFromCpfpRefundTxid()
				// Carried explicitly: custom upsert resolvers bypass ent's
				// UpdateDefault.
				u.UpdateUpdateTime()
			}).
			Exec(ctx); err != nil {
			return fmt.Errorf("unable to batch update finalized tree nodes: %w", err)
		}
	}
	for status, ids := range statusGroups {
		for chunk := range slices.Chunk(ids, maxBatchSize) {
			if err := db.TreeNode.Update().
				Where(treenode.IDIn(chunk...)).
				SetStatus(status).
				// Pinned to the batch timestamp (instead of UpdateDefault's
				// fresh now) so the rows match the marshaled protos.
				SetUpdateTime(updateTime).
				Exec(ctx); err != nil {
				return fmt.Errorf("unable to batch update tree node status to %s: %w", status, err)
			}
		}
	}
	return nil
}

// updateLoadedNode validates one NodeSignatures entry and applies the
// resulting fields to the preloaded tree node in memory; the caller persists
// all nodes in one batch and marshals them afterwards. The returned status is
// the transition finalize decided for the node (nil for none), which the
// caller must apply through an update mutation so the AVAILABLE-transition
// guard vets it. It must not re-query data available on the node's
// eager-loaded edges (see loadNodesForFinalize); the query fallbacks below
// exist only for defense in depth if an edge is missing.
func (o *FinalizeSignatureHandler) updateLoadedNode(ctx context.Context, nodeSignatures *pb.NodeSignatures, node *ent.TreeNode, intent pbcommon.SignatureIntent, requireDirectTx bool, updateTime time.Time) (*st.TreeNodeStatus, error) {
	signingKeyshare := node.Edges.SigningKeyshare
	if signingKeyshare == nil {
		var err error
		signingKeyshare, err = node.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get signing keyshare for node %s: %w", node.ID, err)
		}
	}
	treeEnt := node.Edges.Tree
	if treeEnt == nil {
		var err error
		treeEnt, err = node.QueryTree().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get tree for node %s: %w", node.ID, err)
		}
	}
	// A nil parent with a loaded edge means the node is a root; only fall
	// back to a query when the edge wasn't loaded at all.
	nodeParent := node.Edges.Parent
	if nodeParent == nil {
		if _, edgeErr := node.Edges.ParentOrErr(); ent.IsNotLoaded(edgeErr) {
			p, qErr := node.QueryParent().Only(ctx)
			if qErr != nil && !ent.IsNotFound(qErr) {
				return nil, fmt.Errorf("failed to get parent for node %s: %w", node.ID, qErr)
			}
			nodeParent = p
		}
	}

	var err error
	var hasChildren bool
	if children, childrenErr := node.Edges.ChildrenOrErr(); childrenErr == nil {
		hasChildren = len(children) > 0
	} else if hasChildren, err = node.QueryChildren().Exist(ctx); err != nil {
		return nil, fmt.Errorf("failed to check node children in %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
	}
	nodeCanBecomeAvailable := treeEnt.Status == st.TreeStatusAvailable && tree.TreeNodeCanBecomeAvailable(node) && !hasChildren

	var cpfpNodeTxBytes []byte
	var directNodeTxBytes []byte

	if intent == pbcommon.SignatureIntent_CREATION {
		cpfpNodeTxBytes, err = common.UpdateTxWithSignature(node.RawTx, 0, nodeSignatures.GetNodeTxSignature())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to update cpfp tx with signature %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err))
		}
		if len(node.DirectTx) > 0 && len(nodeSignatures.GetDirectNodeTxSignature()) > 0 {
			directNodeTxBytes, err = common.UpdateTxWithSignature(node.DirectTx, 0, nodeSignatures.GetDirectNodeTxSignature())
			if err != nil {
				return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to update direct tx with signature %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err))
			}
		} else if len(nodeSignatures.GetDirectNodeTxSignature()) == 0 && requireDirectTx && len(node.DirectTx) > 0 {
			return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("DirectNodeTxSignature is required. Please upgrade to the latest SDK version"))
		}
		// Node may not have a parent if it is the root node.
		if nodeParent != nil {
			cpfpTreeNodeTx, err := common.TxFromRawTxBytes(cpfpNodeTxBytes)
			if err != nil {
				return nil, fmt.Errorf("unable to deserialize node tx: %w", err)
			}
			treeNodeParentTx, err := common.TxFromRawTxBytes(nodeParent.RawTx)
			if err != nil {
				return nil, fmt.Errorf("unable to deserialize parent tx: %w", err)
			}
			if len(treeNodeParentTx.TxOut) <= int(node.Vout) {
				return nil, fmt.Errorf("vout out of bounds")
			}
			err = common.VerifySignatureSingleInput(cpfpTreeNodeTx, 0, treeNodeParentTx.TxOut[node.Vout])
			if err != nil {
				return nil, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("unable to verify node tx signature: %w", err))
			}
			if len(directNodeTxBytes) > 0 {
				directTreeNodeTx, err := common.TxFromRawTxBytes(directNodeTxBytes)
				if err != nil {
					return nil, fmt.Errorf("unable to deserialize node tx: %w", err)
				}
				err = common.VerifySignatureSingleInput(directTreeNodeTx, 0, treeNodeParentTx.TxOut[node.Vout])
				if err != nil {
					return nil, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("unable to verify node tx signature: %w", err))
				}
			}
		} else {
			if err := o.verifyDepositBackedRootNodeSignature(ctx, node, treeEnt, cpfpNodeTxBytes); err != nil {
				return nil, err
			}
			if len(directNodeTxBytes) > 0 {
				if err := o.verifyDepositBackedRootNodeSignature(ctx, node, treeEnt, directNodeTxBytes); err != nil {
					return nil, err
				}
			}
		}
	} else {
		cpfpNodeTxBytes = node.RawTx
		directNodeTxBytes = node.DirectTx
	}
	var cpfpRefundTxBytes []byte
	var directRefundTxBytes []byte
	var directFromCpfpRefundTxBytes []byte
	if len(nodeSignatures.GetRefundTxSignature()) > 0 {
		cpfpRefundTxBytes, err = common.UpdateTxWithSignature(node.RawRefundTx, 0, nodeSignatures.GetRefundTxSignature())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to update refund tx with signature %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err))
		}

		cpfpRefundTx, err := common.TxFromRawTxBytes(cpfpRefundTxBytes)
		if err != nil {
			return nil, fmt.Errorf("unable to deserialize refund tx %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
		}
		cpfpTreeNodeTx, err := common.TxFromRawTxBytes(cpfpNodeTxBytes)
		if err != nil {
			return nil, fmt.Errorf("unable to deserialize cpfp leaf tx: %w", err)
		}
		if len(cpfpTreeNodeTx.TxOut) == 0 {
			return nil, fmt.Errorf("cpfp vout out of bounds")
		}
		err = common.VerifySignatureSingleInput(cpfpRefundTx, 0, cpfpTreeNodeTx.TxOut[0])
		if err != nil {
			return nil, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("unable to verify cpfprefund tx signature: %w", err))
		}
		if len(nodeSignatures.GetDirectRefundTxSignature()) > 0 {
			directRefundTxBytes, err = common.UpdateTxWithSignature(node.DirectRefundTx, 0, nodeSignatures.GetDirectRefundTxSignature())
			if err != nil {
				return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to update refund tx with signature %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err))
			}
			directRefundTx, err := common.TxFromRawTxBytes(directRefundTxBytes)
			if err != nil {
				return nil, fmt.Errorf("unable to deserialize refund tx %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
			}
			directTreeNodeTx, err := common.TxFromRawTxBytes(directNodeTxBytes)
			if err != nil {
				return nil, fmt.Errorf("unable to deserialize direct leaf tx: %w", err)
			}
			if len(directTreeNodeTx.TxOut) == 0 {
				return nil, fmt.Errorf("direct vout out of bounds")
			}
			err = common.VerifySignatureSingleInput(directRefundTx, 0, directTreeNodeTx.TxOut[0])
			if err != nil {
				return nil, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("unable to verify direct refund tx signature: %w", err))
			}
		} else if requireDirectTx && len(node.DirectTx) > 0 {
			isZeroNode, err := bitcointransaction.IsZeroNode(node)
			if err != nil {
				return nil, fmt.Errorf("failed to determine if node is zero node: %w", err)
			}

			if !isZeroNode {
				return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("DirectRefundTxSignature is required. Please upgrade to the latest SDK version"))
			}
		}
		if len(nodeSignatures.GetDirectFromCpfpRefundTxSignature()) > 0 {
			directFromCpfpRefundTxBytes, err = common.UpdateTxWithSignature(node.DirectFromCpfpRefundTx, 0, nodeSignatures.GetDirectFromCpfpRefundTxSignature())
			if err != nil {
				return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to update refund tx with signature %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err))
			}
			directFromCpfpRefundTx, err := common.TxFromRawTxBytes(directFromCpfpRefundTxBytes)
			if err != nil {
				return nil, fmt.Errorf("unable to deserialize refund tx %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
			}
			err = common.VerifySignatureSingleInput(directFromCpfpRefundTx, 0, cpfpTreeNodeTx.TxOut[0])
			if err != nil {
				return nil, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("unable to verify direct from cpfp refund tx signature: %w", err))
			}
		} else if requireDirectTx {
			if len(node.DirectTx) > 0 {
				return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("DirectFromCpfpRefundTxSignature is required. Please upgrade to the latest SDK version"))
			}
		}
	} else {
		requiresSignature, err := requiresFinalizeRefundSignature(node, intent)
		if err != nil {
			return nil, err
		}
		if nodeCanBecomeAvailable && requiresSignature {
			return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("RefundTxSignature is required for unsigned refund transaction on node %s", node.ID))
		}
		cpfpRefundTxBytes = node.RawRefundTx
		directRefundTxBytes = node.DirectRefundTx
		directFromCpfpRefundTxBytes = node.DirectFromCpfpRefundTx
	}

	// Decide the status transition against the pre-update field values (the
	// refund-tx check below must see the stored bytes, not this entry's),
	// then apply everything in memory.
	var newStatus *st.TreeNodeStatus
	if treeEnt.Status == st.TreeStatusAvailable && tree.TreeNodeCanBecomeAvailable(node) {
		if len(node.RawRefundTx) == 0 || hasChildren {
			s := st.TreeNodeStatusSplitted
			newStatus = &s
		} else if (intent == pbcommon.SignatureIntent_CREATION && node.Status == st.TreeNodeStatusCreating) || intent == pbcommon.SignatureIntent_TRANSFER {
			s := st.TreeNodeStatusAvailable
			newStatus = &s
		}
	}
	node.RawTx = cpfpNodeTxBytes
	node.RawRefundTx = cpfpRefundTxBytes
	node.DirectTx = directNodeTxBytes
	node.DirectRefundTx = directRefundTxBytes
	node.DirectFromCpfpRefundTx = directFromCpfpRefundTxBytes
	node.UpdateTime = updateTime
	if newStatus != nil {
		node.Status = *newStatus
	}
	// Ensure the edges are set for downstream marshaling even when a fallback
	// query loaded them.
	node.Edges.SigningKeyshare = signingKeyshare
	node.Edges.Tree = treeEnt
	if nodeParent != nil {
		node.Edges.Parent = nodeParent
	}
	return newStatus, nil
}

func txHasWitness(rawTx []byte) (bool, error) {
	if len(rawTx) == 0 {
		return false, nil
	}
	tx, err := common.TxFromRawTxBytes(rawTx)
	if err != nil {
		return false, err
	}
	for _, txIn := range tx.TxIn {
		if len(txIn.Witness) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func requiresFinalizeRefundSignature(node *ent.TreeNode, intent pbcommon.SignatureIntent) (bool, error) {
	if len(node.RawRefundTx) == 0 {
		return false, nil
	}
	hasWitness, err := txHasWitness(node.RawRefundTx)
	if err != nil {
		return false, sparkerrors.InternalDataInconsistency(fmt.Errorf("stored raw refund tx for node %s is malformed: %w", node.ID, err))
	}
	if hasWitness {
		return false, nil
	}
	switch intent {
	case pbcommon.SignatureIntent_CREATION:
		return node.Status == st.TreeNodeStatusCreating, nil
	case pbcommon.SignatureIntent_TRANSFER:
		return node.Status != st.TreeNodeStatusAvailable && node.Status.CanBecomeAvailable(), nil
	default:
		return false, nil
	}
}
