package handler

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
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

	var nodes []*pb.TreeNode
	var internalNodes []*pbinternal.TreeNode
	for _, nodeSignatures := range req.GetNodeSignatures() {
		node, internalNode, err := o.updateNode(ctx, nodeSignatures, req.GetIntent(), requireDirectTx)
		if err != nil {
			return nil, fmt.Errorf("failed to update node for request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err)
		}
		nodes = append(nodes, node)
		internalNodes = append(internalNodes, internalNode)
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
				return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("root node tx input %d spends outpoint %s that is not recorded for deposit address %s", inputIndex, outpoint.String(), depositAddress.Address))
			}
			return fmt.Errorf("failed to query root node tx input %d utxo: %w", inputIndex, err)
		}
		if utxoEntity.Amount > uint64(math.MaxInt64) {
			return sparkerrors.InternalDataInconsistency(fmt.Errorf("utxo %s value %d exceeds int64 max", outpoint.String(), utxoEntity.Amount))
		}
		prevOuts[outpoint] = wire.NewTxOut(int64(utxoEntity.Amount), utxoEntity.PkScript)
	}
	if !spendsBaseOutpoint {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("root node tx for node %s must spend tree base outpoint %s", node.ID, baseOutpoint.String()))
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
		return nil, fmt.Errorf("transfer %s is not in receiver refund signed status", transfer.ID.String())
	}

	session, err := authn.GetSessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !transfer.ReceiverIdentityPubkey.Equals(session.IdentityPublicKey()) {
		return nil, fmt.Errorf("transfer %s is not owned by the authenticated identity public key %x", transfer.ID.String(), session.IdentityPublicKey())
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
		return nil, fmt.Errorf("failed to query transfer leaf IDs for transfer %s: %w", transfer.ID.String(), err)
	}
	if len(leafIDs) != len(transferLeafIDs) {
		return nil, fmt.Errorf("signature count %d does not match transfer leaf count %d for transfer %s", len(leafIDs), len(transferLeafIDs), transfer.ID.String())
	}
	transferLeafIDSet := make(map[uuid.UUID]struct{}, len(transferLeafIDs))
	for _, id := range transferLeafIDs {
		transferLeafIDSet[id] = struct{}{}
	}
	for _, leafID := range leafIDs {
		if _, ok := transferLeafIDSet[leafID]; !ok {
			return nil, fmt.Errorf("leaf %s does not belong to transfer %s", leafID, transfer.ID.String())
		}
	}

	receiverCount, err := transfer.QueryTransferReceivers().Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to count receivers for transfer %s: %w", transfer.ID.String(), err)
	}
	if receiverCount > 1 {
		return nil, fmt.Errorf("transfer %s has %d receivers; FinalizeNodeSignatures does not support multi-receiver transfers", transfer.ID.String(), receiverCount)
	}

	completionTime := time.Now()
	updatedTransfer, err := transfer.Update().SetStatus(st.TransferStatusCompleted).SetCompletionTime(completionTime).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update transfer %s: %w", transfer.ID.String(), err)
	}

	if err := syncReceiversToTerminalStatus(ctx, transfer.ID, st.TransferStatusCompleted, completionTime); err != nil {
		return nil, fmt.Errorf("failed to sync receiver statuses for transfer %s: %w", transfer.ID.String(), err)
	}

	return updatedTransfer, nil
}

func (o *FinalizeSignatureHandler) updateNode(ctx context.Context, nodeSignatures *pb.NodeSignatures, intent pbcommon.SignatureIntent, requireDirectTx bool) (*pb.TreeNode, *pbinternal.TreeNode, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	nodeID, err := uuid.Parse(nodeSignatures.GetNodeId())
	if err != nil {
		return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid node id in %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err))
	}

	// Read the tree node
	node, err := db.TreeNode.Query().
		Where(treenode.ID(nodeID)).
		WithChildren().
		WithTree().
		WithSigningKeyshare().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("failed to get node in %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err))
		}
		return nil, nil, fmt.Errorf("failed to get node in %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
	}
	if node == nil {
		return nil, nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("node not found in %s", logging.FormatProto("node_signatures", nodeSignatures)))
	}

	signingKeyshare := node.Edges.SigningKeyshare
	if signingKeyshare == nil {
		signingKeyshare, err = node.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get signing keyshare for node %s: %w", node.ID, err)
		}
	}
	treeEnt := node.Edges.Tree
	if treeEnt == nil {
		treeEnt, err = node.QueryTree().Only(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get tree for node %s: %w", node.ID, err)
		}
	}

	hasChildren, err := node.QueryChildren().Exist(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to check node children in %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
	}
	nodeCanBecomeAvailable := treeEnt.Status == st.TreeStatusAvailable && tree.TreeNodeCanBecomeAvailable(node) && !hasChildren

	var cpfpNodeTxBytes []byte
	var directNodeTxBytes []byte

	if intent == pbcommon.SignatureIntent_CREATION {
		cpfpNodeTxBytes, err = common.UpdateTxWithSignature(node.RawTx, 0, nodeSignatures.GetNodeTxSignature())
		if err != nil {
			return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to update cpfp tx with signature %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err))
		}
		if len(node.DirectTx) > 0 && len(nodeSignatures.GetDirectNodeTxSignature()) > 0 {
			directNodeTxBytes, err = common.UpdateTxWithSignature(node.DirectTx, 0, nodeSignatures.GetDirectNodeTxSignature())
			if err != nil {
				return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to update direct tx with signature %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err))
			}
		} else if len(nodeSignatures.GetDirectNodeTxSignature()) == 0 && requireDirectTx && len(node.DirectTx) > 0 {
			return nil, nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("DirectNodeTxSignature is required. Please upgrade to the latest SDK version"))
		}
		// Node may not have parent if it is the root node
		nodeParent := node.Edges.Parent
		if node.Edges.Parent == nil {
			p, err := node.QueryParent().Only(ctx)
			if err == nil {
				nodeParent = p
			}
		}
		if nodeParent != nil {
			cpfpTreeNodeTx, err := common.TxFromRawTxBytes(cpfpNodeTxBytes)
			if err != nil {
				return nil, nil, fmt.Errorf("unable to deserialize node tx: %w", err)
			}
			treeNodeParentTx, err := common.TxFromRawTxBytes(nodeParent.RawTx)
			if err != nil {
				return nil, nil, fmt.Errorf("unable to deserialize parent tx: %w", err)
			}
			if len(treeNodeParentTx.TxOut) <= int(node.Vout) {
				return nil, nil, fmt.Errorf("vout out of bounds")
			}
			err = common.VerifySignatureSingleInput(cpfpTreeNodeTx, 0, treeNodeParentTx.TxOut[node.Vout])
			if err != nil {
				return nil, nil, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("unable to verify node tx signature: %w", err))
			}
			if len(directNodeTxBytes) > 0 {
				directTreeNodeTx, err := common.TxFromRawTxBytes(directNodeTxBytes)
				if err != nil {
					return nil, nil, fmt.Errorf("unable to deserialize node tx: %w", err)
				}
				err = common.VerifySignatureSingleInput(directTreeNodeTx, 0, treeNodeParentTx.TxOut[node.Vout])
				if err != nil {
					return nil, nil, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("unable to verify node tx signature: %w", err))
				}
			}
		} else {
			if err := o.verifyDepositBackedRootNodeSignature(ctx, node, treeEnt, cpfpNodeTxBytes); err != nil {
				return nil, nil, err
			}
			if len(directNodeTxBytes) > 0 {
				if err := o.verifyDepositBackedRootNodeSignature(ctx, node, treeEnt, directNodeTxBytes); err != nil {
					return nil, nil, err
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
			return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to update refund tx with signature %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err))
		}

		cpfpRefundTx, err := common.TxFromRawTxBytes(cpfpRefundTxBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to deserialize refund tx %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
		}
		cpfpTreeNodeTx, err := common.TxFromRawTxBytes(cpfpNodeTxBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to deserialize cpfp leaf tx: %w", err)
		}
		if len(cpfpTreeNodeTx.TxOut) == 0 {
			return nil, nil, fmt.Errorf("cpfp vout out of bounds")
		}
		err = common.VerifySignatureSingleInput(cpfpRefundTx, 0, cpfpTreeNodeTx.TxOut[0])
		if err != nil {
			return nil, nil, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("unable to verify cpfprefund tx signature: %w", err))
		}
		if len(nodeSignatures.GetDirectRefundTxSignature()) > 0 {
			directRefundTxBytes, err = common.UpdateTxWithSignature(node.DirectRefundTx, 0, nodeSignatures.GetDirectRefundTxSignature())
			if err != nil {
				return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to update refund tx with signature %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err))
			}
			directRefundTx, err := common.TxFromRawTxBytes(directRefundTxBytes)
			if err != nil {
				return nil, nil, fmt.Errorf("unable to deserialize refund tx %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
			}
			directTreeNodeTx, err := common.TxFromRawTxBytes(directNodeTxBytes)
			if err != nil {
				return nil, nil, fmt.Errorf("unable to deserialize direct leaf tx: %w", err)
			}
			if len(directTreeNodeTx.TxOut) == 0 {
				return nil, nil, fmt.Errorf("direct vout out of bounds")
			}
			err = common.VerifySignatureSingleInput(directRefundTx, 0, directTreeNodeTx.TxOut[0])
			if err != nil {
				return nil, nil, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("unable to verify direct refund tx signature: %w", err))
			}
		} else if requireDirectTx && len(node.DirectTx) > 0 {
			isZeroNode, err := bitcointransaction.IsZeroNode(node)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to determine if node is zero node: %w", err)
			}

			if !isZeroNode {
				return nil, nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("DirectRefundTxSignature is required. Please upgrade to the latest SDK version"))
			}
		}
		if len(nodeSignatures.GetDirectFromCpfpRefundTxSignature()) > 0 {
			directFromCpfpRefundTxBytes, err = common.UpdateTxWithSignature(node.DirectFromCpfpRefundTx, 0, nodeSignatures.GetDirectFromCpfpRefundTxSignature())
			if err != nil {
				return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to update refund tx with signature %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err))
			}
			directFromCpfpRefundTx, err := common.TxFromRawTxBytes(directFromCpfpRefundTxBytes)
			if err != nil {
				return nil, nil, fmt.Errorf("unable to deserialize refund tx %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
			}
			err = common.VerifySignatureSingleInput(directFromCpfpRefundTx, 0, cpfpTreeNodeTx.TxOut[0])
			if err != nil {
				return nil, nil, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("unable to verify direct from cpfp refund tx signature: %w", err))
			}
		} else if requireDirectTx {
			if len(node.DirectTx) > 0 {
				return nil, nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("DirectFromCpfpRefundTxSignature is required. Please upgrade to the latest SDK version"))
			}
		}
	} else {
		requiresSignature, err := requiresFinalizeRefundSignature(node, intent)
		if err != nil {
			return nil, nil, err
		}
		if nodeCanBecomeAvailable && requiresSignature {
			return nil, nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("RefundTxSignature is required for unsigned refund transaction on node %s", node.ID))
		}
		cpfpRefundTxBytes = node.RawRefundTx
		directRefundTxBytes = node.DirectRefundTx
		directFromCpfpRefundTxBytes = node.DirectFromCpfpRefundTx
	}

	// Update the tree node
	nodeMutator := node.Update().
		SetRawTx(cpfpNodeTxBytes).
		SetRawRefundTx(cpfpRefundTxBytes).
		SetDirectTx(directNodeTxBytes).
		SetDirectRefundTx(directRefundTxBytes).
		SetDirectFromCpfpRefundTx(directFromCpfpRefundTxBytes)
	if treeEnt.Status == st.TreeStatusAvailable && tree.TreeNodeCanBecomeAvailable(node) {
		if len(node.RawRefundTx) == 0 || hasChildren {
			nodeMutator.SetStatus(st.TreeNodeStatusSplitted)
		} else if (intent == pbcommon.SignatureIntent_CREATION && node.Status == st.TreeNodeStatusCreating) || intent == pbcommon.SignatureIntent_TRANSFER {
			nodeMutator.SetStatus(st.TreeNodeStatusAvailable)
		}
	}
	node, err = nodeMutator.Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to update node: %w", err)
	}
	// Preserve eagerly-loaded edges for downstream marshaling logic.
	node.Edges.SigningKeyshare = signingKeyshare
	node.Edges.Tree = treeEnt

	nodeSparkProto, err := node.MarshalSparkProto(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to marshal node %s on spark: %w", node.ID.String(), err)
	}
	internalNode, err := node.MarshalInternalProto(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to marshal node %s on internal: %w", node.ID.String(), err)
	}
	return nodeSparkProto, internalNode, nil
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
