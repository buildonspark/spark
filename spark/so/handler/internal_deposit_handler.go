package handler

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
)

// InternalDepositHandler is the deposit handler for so internal
type InternalDepositHandler struct {
	config *so.Config
}

// NewInternalDepositHandler creates a new InternalDepositHandler.
func NewInternalDepositHandler(config *so.Config) *InternalDepositHandler {
	return &InternalDepositHandler{config: config}
}

// MarkKeyshareForDepositAddress links the keyshare to a deposit address.
func (h *InternalDepositHandler) MarkKeyshareForDepositAddress(ctx context.Context, req *pbinternal.MarkKeyshareForDepositAddressRequest) (*pbinternal.MarkKeyshareForDepositAddressResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)

	logger.Info("Marking keyshare for deposit address", "keyshare_id", req.KeyshareId)

	keyshareID, err := uuid.Parse(req.KeyshareId)
	if err != nil {
		logger.Error("Failed to parse keyshare ID", "error", err)
		return nil, err
	}

	depositAddressMutator := ent.GetDbFromContext(ctx).DepositAddress.Create().
		SetSigningKeyshareID(keyshareID).
		SetOwnerIdentityPubkey(req.OwnerIdentityPublicKey).
		SetOwnerSigningPubkey(req.OwnerSigningPublicKey).
		SetAddress(req.Address)

	if req.IsStatic != nil && *req.IsStatic {
		depositAddressMutator.SetIsStatic(true)
	}

	_, err = depositAddressMutator.Save(ctx)
	if err != nil {
		logger.Error("Failed to link keyshare to deposit address", "error", err)
		return nil, err
	}

	logger.Info("Marked keyshare for deposit address", "keyshare_id", req.KeyshareId)

	signingKey := secp256k1.PrivKeyFromBytes(h.config.IdentityPrivateKey)
	addrHash := sha256.Sum256([]byte(req.Address))
	addressSignature := ecdsa.Sign(signingKey, addrHash[:])
	return &pbinternal.MarkKeyshareForDepositAddressResponse{
		AddressSignature: addressSignature.Serialize(),
	}, nil
}

// FinalizeTreeCreation finalizes a tree creation during deposit
func (h *InternalDepositHandler) FinalizeTreeCreation(ctx context.Context, req *pbinternal.FinalizeTreeCreationRequest) error {
	logger := logging.GetLoggerFromContext(ctx)

	treeNodeIDs := make([]string, len(req.Nodes))
	for i, node := range req.Nodes {
		treeNodeIDs[i] = node.Id
	}

	logger.Info("Finalizing tree creation", "tree_node_ids", treeNodeIDs)

	db := ent.GetDbFromContext(ctx)
	var tree *ent.Tree
	var selectedNode *pbinternal.TreeNode
	for _, node := range req.Nodes {
		if node.ParentNodeId == nil {
			logger.Info("Selected node", "tree_node_id", node.Id)
			selectedNode = node
			break
		}
		selectedNode = node
	}

	if selectedNode == nil {
		return fmt.Errorf("no node in the request")
	}
	markNodeAsAvailable := false
	if selectedNode.ParentNodeId == nil {
		treeID, err := uuid.Parse(selectedNode.TreeId)
		if err != nil {
			return err
		}
		network, err := common.NetworkFromProtoNetwork(req.Network)
		if err != nil {
			return err
		}
		if !h.config.IsNetworkSupported(network) {
			return fmt.Errorf("network not supported")
		}
		signingKeyshareID, err := uuid.Parse(selectedNode.SigningKeyshareId)
		if err != nil {
			return err
		}
		address, err := db.DepositAddress.Query().Where(depositaddress.HasSigningKeyshareWith(signingkeyshare.IDEQ(signingKeyshareID))).Only(ctx)
		if err != nil {
			return fmt.Errorf("failed to get deposit address: %v", err)
		}
		markNodeAsAvailable = address.ConfirmationHeight != 0
		logger.Info(fmt.Sprintf("Marking node as available: %v", markNodeAsAvailable))
		nodeTx, err := common.TxFromRawTxBytes(selectedNode.RawTx)
		if err != nil {
			return err
		}
		txid := nodeTx.TxIn[0].PreviousOutPoint.Hash

		schemaNetwork, err := common.SchemaNetworkFromNetwork(network)
		if err != nil {
			return err
		}

		treeMutator := db.Tree.
			Create().
			SetID(treeID).
			SetOwnerIdentityPubkey(selectedNode.OwnerIdentityPubkey).
			SetBaseTxid(txid[:]).
			SetVout(int16(nodeTx.TxIn[0].PreviousOutPoint.Index)).
			SetNetwork(schemaNetwork)

		if markNodeAsAvailable {
			treeMutator.SetStatus(schema.TreeStatusAvailable)
		} else {
			treeMutator.SetStatus(schema.TreeStatusPending)
		}

		tree, err = treeMutator.Save(ctx)
		if err != nil {
			return err
		}
	} else {
		treeID, err := uuid.Parse(selectedNode.TreeId)
		if err != nil {
			return err
		}
		tree, err = db.Tree.Get(ctx, treeID)
		if err != nil {
			return err
		}
		markNodeAsAvailable = tree.Status == schema.TreeStatusAvailable
	}

	for _, node := range req.Nodes {
		nodeID, err := uuid.Parse(node.Id)
		if err != nil {
			return err
		}
		signingKeyshareID, err := uuid.Parse(node.SigningKeyshareId)
		if err != nil {
			return err
		}
		nodeMutator := db.TreeNode.
			Create().
			SetID(nodeID).
			SetTree(tree).
			SetOwnerIdentityPubkey(node.OwnerIdentityPubkey).
			SetOwnerSigningPubkey(node.OwnerSigningPubkey).
			SetValue(node.Value).
			SetVerifyingPubkey(node.VerifyingPubkey).
			SetSigningKeyshareID(signingKeyshareID).
			SetVout(int16(node.Vout)).
			SetRawTx(node.RawTx).
			SetRawRefundTx(node.RawRefundTx)

		if node.ParentNodeId != nil {
			parentID, err := uuid.Parse(*node.ParentNodeId)
			if err != nil {
				return err
			}
			nodeMutator.SetParentID(parentID)
		}

		if markNodeAsAvailable {
			if len(node.RawRefundTx) > 0 {
				nodeMutator.SetStatus(schema.TreeNodeStatusAvailable)
			} else {
				nodeMutator.SetStatus(schema.TreeNodeStatusSplitted)
			}
		} else {
			nodeMutator.SetStatus(schema.TreeNodeStatusCreating)
		}

		_, err = nodeMutator.Save(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}
