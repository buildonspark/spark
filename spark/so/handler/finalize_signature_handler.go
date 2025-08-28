package handler

import (
	"context"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/transferleaf"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// FinalizeSignatureHandler is the handler for the FinalizeNodeSignatures RPC.
type FinalizeSignatureHandler struct {
	config *so.Config
}

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
	if len(req.NodeSignatures) == 0 {
		return &pb.FinalizeNodeSignaturesResponse{Nodes: []*pb.TreeNode{}}, nil
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	firstNodeID, err := uuid.Parse(req.NodeSignatures[0].NodeId)
	if err != nil {
		return nil, fmt.Errorf("invalid node id in request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err)
	}
	firstNode, err := db.TreeNode.Get(ctx, firstNodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get first node for request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err)
	}
	tree, err := firstNode.QueryTree().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get tree for request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err)
	}
	network, err := common.NetworkFromSchemaNetwork(tree.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to get network for request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err)
	}

	if tree.Status != st.TreeStatusAvailable {
		for _, nodeSignatures := range req.NodeSignatures {
			nodeID, err := uuid.Parse(nodeSignatures.NodeId)
			if err != nil {
				return nil, fmt.Errorf("invalid node id in request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err)
			}
			node, err := db.TreeNode.Get(ctx, nodeID)
			if err != nil {
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
				if len(address.ConfirmationTxid) > 0 {
					var baseHash chainhash.Hash
					// Convert the tree.BaseTxid back to chainhash so it matches the format of address.ConfirmationTxid
					copy(baseHash[:], tree.BaseTxid)
					if address.ConfirmationTxid != baseHash.String() {
						return nil, fmt.Errorf("confirmation txid does not match tree base txid")
					}
				}
				_, err = tree.Update().SetStatus(st.TreeStatusAvailable).Save(ctx)
				if err != nil {
					return nil, fmt.Errorf("failed to update tree: %w", err)
				}
				break
			}
		}
	}

	var nodes []*pb.TreeNode
	var internalNodes []*pbinternal.TreeNode
	for _, nodeSignatures := range req.NodeSignatures {
		node, internalNode, err := o.updateNode(ctx, nodeSignatures, req.Intent, requireDirectTx)
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
	logger.Info("Sending finalize node signatures gossip message", "intent", req.Intent)

	switch req.Intent {
	case pbcommon.SignatureIntent_CREATION:
		protoNetwork, err := common.ProtoNetworkFromNetwork(network)
		if err != nil {
			return nil, err
		}

		logger.Info("Sending finalize tree creation gossip message")
		_, err = sendGossipHandler.CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
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
		transfer, err := o.verifyAndUpdateTransfer(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("failed to verify and update transfer for request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err)
		}
		transferID := transfer.ID.String()
		completionTimestamp := timestamppb.New(*transfer.CompletionTime)

		logger.Info("Sending finalize transfer gossip message")

		_, err = sendGossipHandler.CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
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

	case pbcommon.SignatureIntent_REFRESH:
		logger.Info("Sending finalize refresh timelock gossip message")

		_, err = sendGossipHandler.CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
			Message: &pbgossip.GossipMessage_FinalizeRefreshTimelock{
				FinalizeRefreshTimelock: &pbgossip.GossipMessageFinalizeRefreshTimelock{
					InternalNodes: internalNodes,
				},
			},
		}, participants)
		if err != nil {
			return nil, fmt.Errorf("unable to create and send gossip message: %w", err)
		}

	case pbcommon.SignatureIntent_EXTEND:
		if len(internalNodes) == 0 {
			return nil, fmt.Errorf("no nodes to extend")
		}

		logger.Info("Sending finalize extend leaf gossip message")

		_, err = sendGossipHandler.CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
			Message: &pbgossip.GossipMessage_FinalizeExtendLeaf{
				FinalizeExtendLeaf: &pbgossip.GossipMessageFinalizeExtendLeaf{
					InternalNodes: internalNodes,
				},
			},
		}, participants)
		if err != nil {
			return nil, fmt.Errorf("unable to create and send gossip message: %w", err)
		}

	default:
		return nil, fmt.Errorf("invalid intent %s", req.Intent)
	}
	return &pb.FinalizeNodeSignaturesResponse{Nodes: nodes}, nil
}

func (o *FinalizeSignatureHandler) verifyAndUpdateTransfer(ctx context.Context, req *pb.FinalizeNodeSignaturesRequest) (*ent.Transfer, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	// Extract leaf IDs from node signatures
	leafIDs := make([]uuid.UUID, 0, len(req.NodeSignatures))
	for _, nodeSignatures := range req.NodeSignatures {
		leafID, err := uuid.Parse(nodeSignatures.NodeId)
		if err != nil {
			return nil, fmt.Errorf("invalid node id in request %s: %w", logging.FormatProto("finalize_node_signatures_request", req), err)
		}
		leafIDs = append(leafIDs, leafID)
	}

	// Find all ongoing transfers that involves any of these leaves. All these leaves should be
	// part of a **single** transfer so we expect one result.
	transfer, err := db.Transfer.Query().
		WithTransferLeaves().
		Where(
			enttransfer.StatusEQ(st.TransferStatusReceiverRefundSigned),
			enttransfer.HasTransferLeavesWith(
				transferleaf.HasLeafWith(
					treenode.IDIn(leafIDs...),
				),
			),
		).
		ForUpdate().
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find pending transfer for leaves %s: %w", leafIDs, err)
	}

	numTransferLeaves := len(transfer.Edges.TransferLeaves)
	if len(req.NodeSignatures) != numTransferLeaves {
		return nil, fmt.Errorf("missing signatures for transfer %s", transfer.ID.String())
	}

	updatedTransfer, err := transfer.Update().SetStatus(st.TransferStatusCompleted).SetCompletionTime(time.Now()).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update transfer %s: %w", transfer.ID.String(), err)
	}
	return updatedTransfer, nil
}

func (o *FinalizeSignatureHandler) updateNode(ctx context.Context, nodeSignatures *pb.NodeSignatures, intent pbcommon.SignatureIntent, requireDirectTx bool) (*pb.TreeNode, *pbinternal.TreeNode, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	nodeID, err := uuid.Parse(nodeSignatures.NodeId)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid node id in %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
	}

	// Read the tree node
	node, err := db.TreeNode.Query().Where(treenode.ID(nodeID)).WithChildren().Only(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get node in %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
	}
	if node == nil {
		return nil, nil, fmt.Errorf("node not found in %s", logging.FormatProto("node_signatures", nodeSignatures))
	}

	var cpfpNodeTxBytes []byte
	var directNodeTxBytes []byte

	if intent == pbcommon.SignatureIntent_CREATION || ((intent == pbcommon.SignatureIntent_REFRESH || intent == pbcommon.SignatureIntent_EXTEND) && nodeSignatures.NodeTxSignature != nil) {
		cpfpNodeTxBytes, err = common.UpdateTxWithSignature(node.RawTx, 0, nodeSignatures.NodeTxSignature)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to update cpfp tx with signature %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
		}
		if len(node.DirectTx) > 0 && len(nodeSignatures.DirectNodeTxSignature) > 0 {
			directNodeTxBytes, err = common.UpdateTxWithSignature(node.DirectTx, 0, nodeSignatures.DirectNodeTxSignature)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to update direct tx with signature %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
			}
		} else if len(nodeSignatures.DirectNodeTxSignature) == 0 && requireDirectTx && len(node.DirectTx) > 0 {
			return nil, nil, fmt.Errorf("DirectNodeTxSignature is required. Please upgrade to the latest SDK version")
		}
		// Node may not have parent if it is the root node
		nodeParent, err := node.QueryParent().Only(ctx)
		if err == nil && nodeParent != nil {
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
				return nil, nil, fmt.Errorf("unable to verify node tx signature: %w", err)
			}
			if len(directNodeTxBytes) > 0 {
				directTreeNodeTx, err := common.TxFromRawTxBytes(directNodeTxBytes)
				if err != nil {
					return nil, nil, fmt.Errorf("unable to deserialize node tx: %w", err)
				}
				err = common.VerifySignatureSingleInput(directTreeNodeTx, 0, treeNodeParentTx.TxOut[node.Vout])
				if err != nil {
					return nil, nil, fmt.Errorf("unable to verify node tx signature: %w", err)
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
	if len(nodeSignatures.RefundTxSignature) > 0 {
		cpfpRefundTxBytes, err = common.UpdateTxWithSignature(node.RawRefundTx, 0, nodeSignatures.RefundTxSignature)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to update refund tx with signature %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
		}

		cpfpRefundTx, err := common.TxFromRawTxBytes(cpfpRefundTxBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to deserialize refund tx %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
		}
		cpfpTreeNodeTx, err := common.TxFromRawTxBytes(cpfpNodeTxBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to deserialize cpfp leaf tx: %w", err)
		}
		if len(cpfpTreeNodeTx.TxOut) <= 0 {
			return nil, nil, fmt.Errorf("cpfp vout out of bounds")
		}
		err = common.VerifySignatureSingleInput(cpfpRefundTx, 0, cpfpTreeNodeTx.TxOut[0])
		if err != nil {
			return nil, nil, fmt.Errorf("unable to verify cpfprefund tx signature: %w", err)
		}
		if len(nodeSignatures.DirectRefundTxSignature) > 0 && len(nodeSignatures.DirectFromCpfpRefundTxSignature) > 0 {
			directRefundTxBytes, err = common.UpdateTxWithSignature(node.DirectRefundTx, 0, nodeSignatures.DirectRefundTxSignature)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to update refund tx with signature %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
			}
			directFromCpfpRefundTxBytes, err = common.UpdateTxWithSignature(node.DirectFromCpfpRefundTx, 0, nodeSignatures.DirectFromCpfpRefundTxSignature)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to update refund tx with signature %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
			}
			directRefundTx, err := common.TxFromRawTxBytes(directRefundTxBytes)
			if err != nil {
				return nil, nil, fmt.Errorf("unable to deserialize refund tx %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
			}
			directFromCpfpRefundTx, err := common.TxFromRawTxBytes(directFromCpfpRefundTxBytes)
			if err != nil {
				return nil, nil, fmt.Errorf("unable to deserialize refund tx %s: %w", logging.FormatProto("node_signatures", nodeSignatures), err)
			}
			directTreeNodeTx, err := common.TxFromRawTxBytes(directNodeTxBytes)
			if err != nil {
				return nil, nil, fmt.Errorf("unable to deserialize direct leaf tx: %w", err)
			}
			if len(directTreeNodeTx.TxOut) <= 0 {
				return nil, nil, fmt.Errorf("direct vout out of bounds")
			}
			err = common.VerifySignatureSingleInput(directRefundTx, 0, directTreeNodeTx.TxOut[0])
			if err != nil {
				return nil, nil, fmt.Errorf("unable to verify direct refund tx signature: %w", err)
			}
			err = common.VerifySignatureSingleInput(directFromCpfpRefundTx, 0, cpfpTreeNodeTx.TxOut[0])
			if err != nil {
				return nil, nil, fmt.Errorf("unable to verify direct from cpfp refund tx signature: %w", err)
			}
		} else if requireDirectTx && len(node.DirectTx) > 0 {
			return nil, nil, fmt.Errorf("fields DirectRefundTxSignature and DirectFromCpfpRefundTxSignature are required. Please upgrade to the latest SDK version")
		}
	} else {
		cpfpRefundTxBytes = node.RawRefundTx
		directRefundTxBytes = node.DirectRefundTx
		directFromCpfpRefundTxBytes = node.DirectFromCpfpRefundTx
	}

	tree, err := node.QueryTree().Only(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get tree: %w", err)
	}

	// Update the tree node
	nodeMutator := node.Update().
		SetRawTx(cpfpNodeTxBytes).
		SetRawRefundTx(cpfpRefundTxBytes).
		SetDirectTx(directNodeTxBytes).
		SetDirectRefundTx(directRefundTxBytes).
		SetDirectFromCpfpRefundTx(directFromCpfpRefundTxBytes)
	if tree.Status == st.TreeStatusAvailable {
		if len(node.RawRefundTx) > 0 && len(node.Edges.Children) == 0 {
			nodeMutator.SetStatus(st.TreeNodeStatusAvailable)
		} else {
			nodeMutator.SetStatus(st.TreeNodeStatusSplitted)
		}
	}
	node, err = nodeMutator.Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to update node: %w", err)
	}

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
