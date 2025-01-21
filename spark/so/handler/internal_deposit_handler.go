package handler

import (
	"context"
	"crypto/sha256"
	"log"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/google/uuid"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
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

	signingKey, _ := secp256k1.PrivKeyFromBytes(h.config.IdentityPrivateKey)
	addrHash := sha256.Sum256([]byte(req.Address))
	addressSignature, err := signingKey.Sign(addrHash[:])
	if err != nil {
		return nil, err
	}
	return &pbinternal.MarkKeyshareForDepositAddressResponse{
		AddressSignature: addressSignature.Serialize(),
	}, nil
}

// FinalizeTreeCreation finalizes a tree creation during deposit
func (h *InternalDepositHandler) FinalizeTreeCreation(ctx context.Context, req *pbinternal.FinalizeTreeCreationRequest) error {
	db := ent.GetDbFromContext(ctx)
	var tree *ent.Tree = nil
	if req.Nodes[0].ParentNodeId == nil {
		treeID, err := uuid.Parse(req.Nodes[0].TreeId)
		if err != nil {
			return err
		}
		tree, err = db.Tree.
			Create().
			SetID(treeID).
			SetOwnerIdentityPubkey(req.Nodes[0].OwnerIdentityPubkey).
			Save(ctx)
		if err != nil {
			return err
		}
	} else {
		treeID, err := uuid.Parse(req.Nodes[0].TreeId)
		if err != nil {
			return err
		}
		tree, err = db.Tree.Get(ctx, treeID)
		if err != nil {
			return err
		}
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
		mutatedNode := db.TreeNode.
			Create().
			SetID(nodeID).
			SetTree(tree).
			SetStatus(schema.TreeNodeStatusAvailable).
			SetOwnerIdentityPubkey(node.OwnerIdentityPubkey).
			SetOwnerSigningPubkey(node.OwnerSigningPubkey).
			SetValue(node.Value).
			SetVerifyingPubkey(node.VerifyingPubkey).
			SetSigningKeyshareID(signingKeyshareID).
			SetVout(uint16(node.Vout)).
			SetRawTx(node.RawTx).
			SetRawRefundTx(node.RawRefundTx)

		if node.ParentNodeId != nil {
			parentID, err := uuid.Parse(*node.ParentNodeId)
			if err != nil {
				return err
			}
			mutatedNode.SetParentID(parentID)
		}

		entNode, err := mutatedNode.Save(ctx)
		if err != nil {
			return err
		}

		if node.ParentNodeId == nil {
			_, err = entNode.Update().SetStatus(schema.TreeNodeStatusAvailable).Save(ctx)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
