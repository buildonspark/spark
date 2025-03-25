package handler

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/helper"
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
	log.Printf("Marking keyshare for deposit address: %v", req.KeyshareId)

	keyshareID, err := uuid.Parse(req.KeyshareId)
	if err != nil {
		log.Printf("Failed to parse keyshare ID: %v", err)
		return nil, err
	}

	_, err = ent.GetDbFromContext(ctx).DepositAddress.Create().
		SetSigningKeyshareID(keyshareID).
		SetOwnerIdentityPubkey(req.OwnerIdentityPublicKey).
		SetOwnerSigningPubkey(req.OwnerSigningPublicKey).
		SetAddress(req.Address).
		Save(ctx)
	if err != nil {
		log.Printf("Failed to link keyshare to deposit address: %v", err)
		return nil, err
	}

	log.Printf("Marked keyshare for deposit address")

	signingKey := secp256k1.PrivKeyFromBytes(h.config.IdentityPrivateKey)
	addrHash := sha256.Sum256([]byte(req.Address))
	addressSignature := ecdsa.Sign(signingKey, addrHash[:])
	return &pbinternal.MarkKeyshareForDepositAddressResponse{
		AddressSignature: addressSignature.Serialize(),
	}, nil
}

// FinalizeTreeCreation finalizes a tree creation during deposit
func (h *InternalDepositHandler) FinalizeTreeCreation(ctx context.Context, req *pbinternal.FinalizeTreeCreationRequest) error {
	log.Printf("Finalizing tree creation: %v", req.Nodes)
	db := ent.GetDbFromContext(ctx)
	var tree *ent.Tree
	var selectedNode *pbinternal.TreeNode
	for _, node := range req.Nodes {
		if node.ParentNodeId == nil {
			log.Printf("Selected node: %v", node)
			selectedNode = node
			break
		}
		selectedNode = node
	}

	if selectedNode == nil {
		return fmt.Errorf("no node in the request")
	}
	markNodeAsAvailalbe := false
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
		nodeTx, err := common.TxFromRawTxBytes(selectedNode.RawTx)
		if err != nil {
			return err
		}
		txid := nodeTx.TxIn[0].PreviousOutPoint.Hash
		markNodeAsAvailalbe = helper.CheckTxIDOnchain(h.config, txid[:], network)
		log.Printf("Marking node as available: %v", markNodeAsAvailalbe)

		schemaNetwork, err := common.SchemaNetworkFromNetwork(network)
		if err != nil {
			return err
		}

		treeMutator := db.Tree.
			Create().
			SetID(treeID).
			SetOwnerIdentityPubkey(selectedNode.OwnerIdentityPubkey).
			SetBaseTxid(txid[:]).
			SetNetwork(schemaNetwork)

		if markNodeAsAvailalbe {
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
		markNodeAsAvailalbe = tree.Status == schema.TreeStatusAvailable
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

		if markNodeAsAvailalbe {
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
