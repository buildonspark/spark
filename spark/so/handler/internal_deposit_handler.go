package handler

import (
	"context"
	"log"

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
func (h *InternalDepositHandler) MarkKeyshareForDepositAddress(ctx context.Context, req *pbinternal.MarkKeyshareForDepositAddressRequest) error {
	log.Printf("Marking keyshare for deposit address: %v", req.KeyshareId)

	keyshareID, err := uuid.Parse(req.KeyshareId)
	if err != nil {
		log.Printf("Failed to parse keyshare ID: %v", err)
		return err
	}

	_, err = ent.GetDbFromContext(ctx).DepositAddress.Create().
		SetSigningKeyshareID(keyshareID).
		SetOwnerIdentityPubkey(req.OwnerIdentityPublicKey).
		SetOwnerSigningPubkey(req.OwnerSigningPublicKey).
		SetAddress(req.Address).
		Save(ctx)
	if err != nil {
		log.Printf("Failed to link keyshare to deposit address: %v", err)
		return err
	}

	log.Printf("Marked keyshare for deposit address")
	return nil
}

// FinalizeTreeCreation finalizes a tree creation during deposit
func (h *InternalDepositHandler) FinalizeTreeCreation(ctx context.Context, req *pbinternal.FinalizeTreeCreationRequest) error {
	db := ent.GetDbFromContext(ctx)
	treeID, err := uuid.Parse(req.RootNode.TreeId)
	if err != nil {
		return err
	}
	tree, err := db.Tree.
		Create().
		SetID(treeID).
		SetOwnerIdentityPubkey(req.RootNode.OwnerIdentityPubkey).
		Save(ctx)
	if err != nil {
		return err
	}

	nodeID, err := uuid.Parse(req.RootNode.Id)
	if err != nil {
		return err
	}
	signingKeyshareID, err := uuid.Parse(req.RootNode.SigningKeyshareId)
	if err != nil {
		return err
	}
	root, err := db.TreeNode.
		Create().
		SetID(nodeID).
		SetTree(tree).
		SetStatus(schema.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(req.RootNode.OwnerIdentityPubkey).
		SetOwnerSigningPubkey(req.RootNode.OwnerSigningPubkey).
		SetValue(req.RootNode.Value).
		SetVerifyingPubkey(req.RootNode.VerifyingPubkey).
		SetSigningKeyshareID(signingKeyshareID).
		SetVout(uint16(req.RootNode.Vout)).
		SetRawTx(req.RootNode.RawTx).
		SetRawRefundTx(req.RootNode.RawRefundTx).
		Save(ctx)
	if err != nil {
		return err
	}
	_, err = tree.Update().SetRoot(root).Save(ctx)
	if err != nil {
		return err
	}
	return nil
}
