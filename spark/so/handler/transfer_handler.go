package handler

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/google/uuid"
	secretsharing "github.com/lightsparkdev/spark-go/common/secret_sharing"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/transfer"
)

// TransferHandler is a helper struct to handle leaves transfer request.
type TransferHandler struct {
	config *so.Config
}

// NewTransferHandler creates a new TransferHandler.
func NewTransferHandler(config *so.Config) *TransferHandler {
	return &TransferHandler{config: config}
}

// SendTransfer handles a request to initiate a transfer of leaves.
func (h *TransferHandler) SendTransfer(ctx context.Context, req *pb.SendTransferRequest) (*pb.SendTransferResponse, error) {
	transferID, err := uuid.Parse(req.TransferId)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %v", req.TransferId, err)
	}

	expiryTime := req.ExpiryTime.AsTime()
	if expiryTime.Before(time.Now()) {
		return nil, fmt.Errorf("invalid expiry_time %s: %v", expiryTime.String(), err)
	}

	db := ent.GetDbFromContext(ctx)
	transfer, err := db.Transfer.Create().
		SetID(transferID).
		SetInitiatorIdentityPubkey(req.OwnerIdentityPublicKey).
		SetReceiverIdentityPubkey(req.ReceiverIdentityPublicKey).
		SetStatus(schema.TransferStatusInitiated).
		SetTotalValue(0).
		SetExpiryTime(expiryTime).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to create transfer: %v", err)
	}

	for _, transferReq := range req.Tranfers {
		transfer, err = h.initLeafTransfer(ctx, transfer, transferReq)
		if err != nil {
			return nil, fmt.Errorf("unable to init transfer for leaf %s: %v", transferReq.LeafId, err)
		}
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer: %v", err)
	}
	return &pb.SendTransferResponse{Transfer: transferProto}, nil
}

func (h *TransferHandler) initLeafTransfer(ctx context.Context, transfer *ent.Transfer, req *pb.LeafTransferRequest) (*ent.Transfer, error) {
	// Use Feldman's verifiable secret sharing to verify the share.
	err := secretsharing.ValidateShare(
		&secretsharing.VerifiableSecretShare{
			SecretShare: secretsharing.SecretShare{
				FieldModulus: secp256k1.S256().N,
				Threshold:    int(h.config.Threshold),
				Index:        big.NewInt(int64(h.config.Index + 1)),
				Share:        new(big.Int).SetBytes(req.SecretShareTweak.Tweak),
			},
			Proofs: req.SecretShareTweak.Proofs,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("unable to validate share: %v", err)
	}

	// TODO (zhen): Verify possession

	// Find leaves in db
	leafID, err := uuid.Parse(req.LeafId)
	if err != nil {
		return nil, fmt.Errorf("unable to parse leaf_id %s: %v", req.LeafId, err)
	}

	db := ent.GetDbFromContext(ctx)
	leaf, err := db.TreeNode.Get(ctx, leafID)
	if err != nil || leaf == nil {
		return nil, fmt.Errorf("unable to find leaf %s: %v", req.LeafId, err)
	}
	if leaf.Status != schema.TreeNodeStatusAvailable || !bytes.Equal(leaf.OwnerIdentityPubkey, transfer.InitiatorIdentityPubkey) {
		return nil, fmt.Errorf("leaf %s is not available to transfer", req.LeafId)
	}

	// Tweak keyshare
	keyshare, err := leaf.QuerySigningKeyshare().First(ctx)
	if err != nil || keyshare == nil {
		return nil, fmt.Errorf("unable to load keyshare for leaf %s: %v", req.LeafId, err)
	}
	keyshare, err = keyshare.TweakKeyShare(
		ctx,
		req.SecretShareTweak.Tweak,
		req.SecretShareTweak.Proofs[0],
		req.PubkeySharesTweak,
	)
	if err != nil || keyshare == nil {
		return nil, fmt.Errorf("unable to tweak keyshare %s for leaf %s: %v", keyshare.ID.String(), req.LeafId, err)
	}

	// Lock leaf
	leaf, err = leaf.Update().SetStatus(schema.TreeNodeStatusTransferLocked).Save(ctx)
	if err != nil || leaf == nil {
		return nil, fmt.Errorf("unable to lock leaf %s: %v", req.LeafId, err)
	}

	// Create TransferLeaf and update Transfer.TotalValue
	_, err = db.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetSecretCipher(req.SecretCipher).
		SetSignature(req.Signature).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to create transfer leaf: %v", err)
	}
	return db.Transfer.UpdateOne(transfer).SetTotalValue(transfer.TotalValue + leaf.Value).Save(ctx)
}

// QueryPendingTransfers queries the pending transfers to claim.
func (h *TransferHandler) QueryPendingTransfers(ctx context.Context, req *pb.QueryPendingTransfersRequest) (*pb.QueryPendingTransfersResponse, error) {
	db := ent.GetDbFromContext(ctx)
	transfers, err := db.Transfer.Query().
		Where(
			transfer.And(
				transfer.ReceiverIdentityPubkeyEQ(req.ReceiverIdentityPublicKey),
				transfer.StatusEQ(schema.TransferStatusInitiated),
				transfer.ExpiryTimeGT(time.Now()),
			),
		).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query pending transfers: %v", err)
	}

	transferProtos := []*pb.Transfer{}
	for _, transfer := range transfers {
		transferProto, err := transfer.MarshalProto(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal transfer: %v", err)
		}
		transferProtos = append(transferProtos, transferProto)
	}
	return &pb.QueryPendingTransfersResponse{Transfers: transferProtos}, nil
}
