package handler

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	secretsharing "github.com/lightsparkdev/spark-go/common/secret_sharing"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/transfer"
	"github.com/lightsparkdev/spark-go/so/helper"
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
		SetSenderIdentityPubkey(req.OwnerIdentityPublicKey).
		SetReceiverIdentityPubkey(req.ReceiverIdentityPublicKey).
		SetStatus(schema.TransferStatusInitiated).
		SetTotalValue(0).
		SetExpiryTime(expiryTime).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to create transfer: %v", err)
	}

	for _, leaf := range req.LeavesToSend {
		transfer, err = h.initLeafTransfer(ctx, transfer, leaf)
		if err != nil {
			return nil, fmt.Errorf("unable to init transfer for leaf %s: %v", leaf.LeafId, err)
		}
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer: %v", err)
	}
	return &pb.SendTransferResponse{Transfer: transferProto}, nil
}

func (h *TransferHandler) initLeafTransfer(ctx context.Context, transfer *ent.Transfer, req *pb.SendLeafKeyTweak) (*ent.Transfer, error) {
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
	if leaf.Status != schema.TreeNodeStatusAvailable || !bytes.Equal(leaf.OwnerIdentityPubkey, transfer.SenderIdentityPubkey) {
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

// ClaimTransferTweakKeys starts claiming a pending transfer by tweaking keys of leaves.
func (h *TransferHandler) ClaimTransferTweakKeys(ctx context.Context, req *pb.ClaimTransferTweakKeysRequest) error {
	transferID, err := uuid.Parse(req.TransferId)
	if err != nil {
		return fmt.Errorf("unable to parse transfer_id as a uuid %s: %v", req.TransferId, err)
	}
	db := ent.GetDbFromContext(ctx)
	transfer, err := db.Transfer.Get(ctx, transferID)
	if err != nil {
		return fmt.Errorf("unable to find pending transfer %s: %v", req.TransferId, err)
	}
	// TODO (yun): Check with other SO if expires
	if !bytes.Equal(transfer.ReceiverIdentityPubkey, req.OwnerIdentityPublicKey) || transfer.Status != schema.TransferStatusInitiated || transfer.ExpiryTime.Before(time.Now()) {
		return fmt.Errorf("transfer cannot be claimed %s", req.TransferId)
	}
	// Update transfer status
	_, err = transfer.Update().SetStatus(schema.TransferStatusKeyTweaked).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status %s: %v", transfer.ID.String(), err)
	}

	// Validate leaves count
	leaves, err := h.getLeavesFromTransfer(ctx, transfer)
	if err != nil {
		return fmt.Errorf("unable to get leaves from transfer %s: %v", req.TransferId, err)
	}
	if len(*leaves) != len(req.LeavesToReceive) {
		return fmt.Errorf("inconsistent leaves to claim for transfer %s", req.TransferId)
	}

	// Tweak keys
	for _, req := range req.LeavesToReceive {
		leaf, exists := (*leaves)[req.LeafId]
		if !exists {
			return fmt.Errorf("unexpected leaf id %s", req.LeafId)
		}
		err = h.claimLeafTweakKey(ctx, leaf, req)
		if err != nil {
			return fmt.Errorf("unable to tweak key for leaf %s: %v", req.LeafId, err)
		}
	}

	return nil
}

func (h *TransferHandler) claimLeafTweakKey(ctx context.Context, leaf *ent.TreeNode, req *pb.ClaimLeafKeyTweak) error {
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
		return fmt.Errorf("unable to validate share: %v", err)
	}

	if leaf.Status != schema.TreeNodeStatusTransferLocked {
		return fmt.Errorf("unable to transfer leaf %s", leaf.ID.String())
	}

	// Tweak keyshare
	keyshare, err := leaf.QuerySigningKeyshare().First(ctx)
	if err != nil {
		return fmt.Errorf("unable to load keyshare for leaf %s: %v", leaf.ID.String(), err)
	}
	_, err = keyshare.TweakKeyShare(
		ctx,
		req.SecretShareTweak.Tweak,
		req.SecretShareTweak.Proofs[0],
		req.PubkeySharesTweak,
	)
	if err != nil {
		return fmt.Errorf("unable to tweak keyshare %s for leaf %s: %v", keyshare.ID.String(), leaf.ID.String(), err)
	}
	return nil
}

func (h *TransferHandler) getLeavesFromTransfer(ctx context.Context, transfer *ent.Transfer) (*map[string]*ent.TreeNode, error) {
	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get leaves for transfer %s: %v", transfer.ID.String(), err)
	}
	leaves := make(map[string]*ent.TreeNode)
	for _, transferLeaf := range transferLeaves {
		leaf, err := transferLeaf.QueryLeaf().First(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get leaf %s: %v", transferLeaf.ID.String(), err)
		}
		leaves[leaf.ID.String()] = leaf
	}
	return &leaves, nil
}

// ClaimTransferSignRefunds signs new refund transactions as part of the transfer.
func (h *TransferHandler) ClaimTransferSignRefunds(ctx context.Context, req *pb.ClaimTransferSignRefundsRequest) (*pb.ClaimTransferSignRefundsResponse, error) {
	transferID, err := uuid.Parse(req.TransferId)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %v", req.TransferId, err)
	}
	db := ent.GetDbFromContext(ctx)
	transfer, err := db.Transfer.Get(ctx, transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to find pending transfer %s: %v", req.TransferId, err)
	}
	if !bytes.Equal(transfer.ReceiverIdentityPubkey, req.OwnerIdentityPublicKey) || transfer.Status != schema.TransferStatusKeyTweaked {
		return nil, fmt.Errorf("transfer %s is expected to be at status TransferStatusKeyTweaked but %s found", req.TransferId, transfer.Status)
	}

	// Validate leaves count
	leavesToTransfer, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load leaves to transfer for transfer %s: %v", req.TransferId, err)
	}
	if len(leavesToTransfer) != len(req.SigningJobs) {
		return nil, fmt.Errorf("inconsistent leaves to claim for transfer %s", req.TransferId)
	}

	leaves, err := h.getLeavesFromTransfer(ctx, transfer)
	if err != nil {
		return nil, err
	}
	signingJobs := []*helper.SigningJob{}
	jobToLeafMap := make(map[string]uuid.UUID)
	for _, job := range req.SigningJobs {
		leaf, exists := (*leaves)[job.LeafId]
		if !exists {
			return nil, fmt.Errorf("unexpected leaf id %s", job.LeafId)
		}

		leaf, err := leaf.Update().SetRawRefundTx(job.RefundTxSigningJob.RawTx).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update leaf refund tx %s: %v", leaf.ID.String(), err)
		}

		signingJob, err := h.getRefundTxSigningJob(ctx, leaf, job.RefundTxSigningJob)
		if err != nil {
			return nil, fmt.Errorf("unable to create signing job for leaf %s: %v", leaf.ID.String(), err)
		}
		signingJobs = append(signingJobs, signingJob)
		jobToLeafMap[signingJob.JobID] = leaf.ID
	}

	// Signing
	signingResults, err := helper.SignFrost(ctx, h.config, signingJobs)
	if err != nil {
		return nil, err
	}
	signingResultProtos := []*pb.ClaimLeafSigningResult{}
	for _, signingResult := range signingResults {
		leafID := jobToLeafMap[signingResult.JobID]
		leaf := (*leaves)[leafID.String()]
		signingCommitments, err := common.ConvertObjectMapToProtoMap(signingResult.SigningCommitments)
		if err != nil {
			return nil, err
		}
		signingResultProtos = append(signingResultProtos, &pb.ClaimLeafSigningResult{
			LeafId: leafID.String(),
			RefundTxSigningResult: &pb.SigningResult{
				PublicKeys:              signingResult.PublicKeys,
				SigningNonceCommitments: signingCommitments,
				SignatureShares:         signingResult.SignatureShares,
			},
			VerifyingKey: leaf.VerifyingPubkey,
		})
	}

	// Update transfer status
	_, err = transfer.Update().SetStatus(schema.TransferStatusRefundSigned).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status %s: %v", transfer.ID.String(), err)
	}
	return &pb.ClaimTransferSignRefundsResponse{SigningResults: signingResultProtos}, nil
}

func (h *TransferHandler) getRefundTxSigningJob(ctx context.Context, leaf *ent.TreeNode, job *pb.SigningJob) (*helper.SigningJob, error) {
	keyshare, err := leaf.QuerySigningKeyshare().First(ctx)
	if err != nil || keyshare == nil {
		return nil, fmt.Errorf("unable to load keyshare for leaf %s: %v", leaf.ID.String(), err)
	}
	leafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, fmt.Errorf("unable to load leaf tx for leaf %s: %v", leaf.ID.String(), err)
	}
	refundSigningJob, _, err := helper.NewSigningJob(keyshare, job, leafTx.TxOut[leaf.Vout])
	if err != nil {
		return nil, fmt.Errorf("unable to create signing job for leaf %s: %v", leaf.ID.String(), err)
	}
	return refundSigningJob, nil
}
