package handler

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	secretsharing "github.com/lightsparkdev/spark-go/common/secret_sharing"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/authz"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/blockheight"
	"github.com/lightsparkdev/spark-go/so/ent/cooperativeexit"
	"github.com/lightsparkdev/spark-go/so/ent/predicate"
	"github.com/lightsparkdev/spark-go/so/ent/preimagerequest"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	enttransfer "github.com/lightsparkdev/spark-go/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark-go/so/ent/transferleaf"
	enttreenode "github.com/lightsparkdev/spark-go/so/ent/treenode"
	"github.com/lightsparkdev/spark-go/so/helper"
	"github.com/lightsparkdev/spark-go/so/objects"
	"google.golang.org/protobuf/proto"
)

// TransferHandler is a helper struct to handle leaves transfer request.
type TransferHandler struct {
	BaseTransferHandler
	config *so.Config
}

// NewTransferHandler creates a new TransferHandler.
func NewTransferHandler(config *so.Config) *TransferHandler {
	return &TransferHandler{BaseTransferHandler: NewBaseTransferHandler(config), config: config}
}

// StartSendTransfer initiates a transfer from sender.
func (h *TransferHandler) StartSendTransfer(ctx context.Context, req *pb.StartSendTransferRequest) (*pb.StartSendTransferResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.OwnerIdentityPublicKey); err != nil {
		return nil, err
	}

	leafRefundMap := make(map[string][]byte)
	for _, leaf := range req.LeavesToSend {
		leafRefundMap[leaf.LeafId] = leaf.RefundTxSigningJob.RawTx
	}
	transfer, leafMap, err := h.createTransfer(ctx, req.TransferId, schema.TransferTypeTransfer, req.ExpiryTime.AsTime(), req.OwnerIdentityPublicKey, req.ReceiverIdentityPublicKey, leafRefundMap)
	if err != nil {
		return nil, err
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer: %v", err)
	}

	signingResults, err := h.sendTransferSignRefunds(ctx, req.LeavesToSend, leafMap)
	if err != nil {
		return nil, err
	}

	err = h.syncTransferInit(ctx, req)
	if err != nil {
		return nil, err
	}

	return &pb.StartSendTransferResponse{Transfer: transferProto, SigningResults: signingResults}, nil
}

// InitiateLeafSwap initiates a leaf swap.
func (h *TransferHandler) InitiateLeafSwap(ctx context.Context, req *pb.LeafSwapRequest) (*pb.LeafSwapResponse, error) {
	reqTransfer := req.Transfer
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqTransfer.OwnerIdentityPublicKey); err != nil {
		return nil, err
	}

	leafRefundMap := make(map[string][]byte)
	for _, leaf := range reqTransfer.LeavesToSend {
		leafRefundMap[leaf.LeafId] = leaf.RefundTxSigningJob.RawTx
	}
	transfer, leafMap, err := h.createTransfer(ctx, reqTransfer.TransferId, schema.TransferTypeTransfer, reqTransfer.ExpiryTime.AsTime(), reqTransfer.OwnerIdentityPublicKey, reqTransfer.ReceiverIdentityPublicKey, leafRefundMap)
	if err != nil {
		return nil, err
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer: %v", err)
	}

	signingResults, err := signRefunds(ctx, h.config, reqTransfer.LeavesToSend, leafMap, req.AdaptorPublicKey)
	if err != nil {
		return nil, err
	}

	err = h.syncTransferInit(ctx, reqTransfer)
	if err != nil {
		return nil, err
	}

	return &pb.LeafSwapResponse{Transfer: transferProto, SigningResults: signingResults}, nil
}

func (h *TransferHandler) syncTransferInit(ctx context.Context, req *pb.StartSendTransferRequest) error {
	leaves := make([]*pbinternal.InitiateTransferLeaf, 0)
	for _, leaf := range req.LeavesToSend {
		leaves = append(leaves, &pbinternal.InitiateTransferLeaf{
			LeafId:      leaf.LeafId,
			RawRefundTx: leaf.RefundTxSigningJob.RawTx,
		})
	}
	initTransferRequest := &pbinternal.InitiateTransferRequest{
		TransferId:                req.TransferId,
		SenderIdentityPublicKey:   req.OwnerIdentityPublicKey,
		ReceiverIdentityPublicKey: req.ReceiverIdentityPublicKey,
		ExpiryTime:                req.ExpiryTime,
		Leaves:                    leaves,
	}
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := common.NewGRPCConnectionWithCert(operator.Address, operator.CertPath)
		if err != nil {
			log.Printf("Failed to connect to operator: %v", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.InitiateTransfer(ctx, initTransferRequest)
	})
	return err
}

func (h *TransferHandler) sendTransferSignRefunds(ctx context.Context, requests []*pb.LeafRefundTxSigningJob, leafMap map[string]*ent.TreeNode) ([]*pb.LeafRefundTxSigningResult, error) {
	return signRefunds(ctx, h.config, requests, leafMap, nil)
}

func signRefunds(ctx context.Context, config *so.Config, requests []*pb.LeafRefundTxSigningJob, leafMap map[string]*ent.TreeNode, adaptorPubKey []byte) ([]*pb.LeafRefundTxSigningResult, error) {
	signingJobs := make([]*helper.SigningJob, 0)
	leafJobMap := make(map[string]*ent.TreeNode)
	for _, req := range requests {
		leaf := leafMap[req.LeafId]
		refundTx, err := common.TxFromRawTxBytes(req.RefundTxSigningJob.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to load new refund tx: %v", err)
		}

		leafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to load leaf tx: %v", err)
		}
		if len(leafTx.TxOut) <= 0 {
			return nil, fmt.Errorf("vout out of bounds")
		}
		refundTxSigHash, err := common.SigHashFromTx(refundTx, 0, leafTx.TxOut[0])
		if err != nil {
			return nil, fmt.Errorf("unable to calculate sighash from refund tx: %v", err)
		}

		userNonceCommitment, err := objects.NewSigningCommitment(req.RefundTxSigningJob.SigningNonceCommitment.Binding, req.RefundTxSigningJob.SigningNonceCommitment.Hiding)
		if err != nil {
			return nil, err
		}
		jobID := uuid.New().String()
		signingJobs = append(
			signingJobs,
			&helper.SigningJob{
				JobID:             jobID,
				SigningKeyshareID: leaf.QuerySigningKeyshare().FirstIDX(ctx),
				Message:           refundTxSigHash,
				VerifyingKey:      leaf.VerifyingPubkey,
				UserCommitment:    userNonceCommitment,
				AdaptorPublicKey:  adaptorPubKey,
			},
		)
		leafJobMap[jobID] = leaf
	}

	signingResults, err := helper.SignFrost(ctx, config, signingJobs)
	if err != nil {
		return nil, err
	}
	pbSigningResults := make([]*pb.LeafRefundTxSigningResult, 0)
	for _, signingResult := range signingResults {
		leaf := leafJobMap[signingResult.JobID]
		signingResultProto, err := signingResult.MarshalProto()
		if err != nil {
			return nil, err
		}
		pbSigningResults = append(pbSigningResults, &pb.LeafRefundTxSigningResult{
			LeafId:                leaf.ID.String(),
			RefundTxSigningResult: signingResultProto,
			VerifyingKey:          leaf.VerifyingPubkey,
		})
	}
	return pbSigningResults, nil
}

// CompleteSendTransfer completes a transfer from sender.
func (h *TransferHandler) CompleteSendTransfer(ctx context.Context, req *pb.CompleteSendTransferRequest) (*pb.CompleteSendTransferResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.OwnerIdentityPublicKey); err != nil {
		return nil, err
	}

	transferID, err := uuid.Parse(req.TransferId)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %v", req.TransferId, err)
	}

	db := ent.GetDbFromContext(ctx)
	transfer, err := db.Transfer.Get(ctx, transferID)
	if err != nil || transfer == nil {
		return nil, fmt.Errorf("unable to find transfer %s: %v", transferID, err)
	}
	if !bytes.Equal(transfer.SenderIdentityPubkey, req.OwnerIdentityPublicKey) || transfer.Status != schema.TransferStatusSenderInitiated {
		return nil, fmt.Errorf("send transfer cannot be completed %s", req.TransferId)
	}

	shouldTweakKey := true
	if transfer.Type == schema.TransferTypePreimageSwap {
		preimageRequest, err := db.PreimageRequest.Query().Where(preimagerequest.HasTransfersWith(enttransfer.ID(transfer.ID))).Only(ctx)
		if err != nil || preimageRequest == nil {
			return nil, fmt.Errorf("unable to find preimage request for transfer %s: %v", transfer.ID.String(), err)
		}
		shouldTweakKey = preimageRequest.Status == schema.PreimageRequestStatusPreimageShared
	}

	for _, leaf := range req.LeavesToSend {
		log.Printf("complete send leaf %s shouldTweakKey: %v", leaf.LeafId, shouldTweakKey)
		err = h.completeSendLeaf(ctx, transfer, leaf, shouldTweakKey)
		if err != nil {
			return nil, fmt.Errorf("unable to complete send leaf transfer for leaf %s: %v", leaf.LeafId, err)
		}
	}

	// Update transfer status
	statusToSet := schema.TransferStatusSenderKeyTweaked
	if !shouldTweakKey {
		statusToSet = schema.TransferStatusSenderKeyTweakPending
	}
	transfer, err = transfer.Update().SetStatus(statusToSet).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status %s: %v", transfer.ID.String(), err)
	}
	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer: %v", err)
	}
	return &pb.CompleteSendTransferResponse{Transfer: transferProto}, nil
}

func (h *TransferHandler) completeSendLeaf(ctx context.Context, transfer *ent.Transfer, req *pb.SendLeafKeyTweak, shouldTweakKey bool) error {
	// Use Feldman's verifiable secret sharing to verify the share.
	err := secretsharing.ValidateShare(
		&secretsharing.VerifiableSecretShare{
			SecretShare: secretsharing.SecretShare{
				FieldModulus: secp256k1.S256().N,
				Threshold:    int(h.config.Threshold),
				Index:        big.NewInt(int64(h.config.Index + 1)),
				Share:        new(big.Int).SetBytes(req.SecretShareTweak.SecretShare),
			},
			Proofs: req.SecretShareTweak.Proofs,
		},
	)
	if err != nil {
		return fmt.Errorf("unable to validate share: %v", err)
	}

	// TODO (zhen): Verify possession

	// Find leaves in db
	leafID, err := uuid.Parse(req.LeafId)
	if err != nil {
		return fmt.Errorf("unable to parse leaf_id %s: %v", req.LeafId, err)
	}

	db := ent.GetDbFromContext(ctx)
	leaf, err := db.TreeNode.Get(ctx, leafID)
	if err != nil || leaf == nil {
		return fmt.Errorf("unable to find leaf %s: %v", req.LeafId, err)
	}
	if leaf.Status != schema.TreeNodeStatusTransferLocked ||
		!bytes.Equal(leaf.OwnerIdentityPubkey, transfer.SenderIdentityPubkey) {
		return fmt.Errorf("leaf %s is not available to transfer", req.LeafId)
	}

	transferLeaf, err := db.TransferLeaf.
		Query().
		Where(
			enttransferleaf.HasTransferWith(enttransfer.IDEQ(transfer.ID)),
			enttransferleaf.HasLeafWith(enttreenode.IDEQ(leafID)),
		).
		Only(ctx)
	if err != nil || transferLeaf == nil {
		return fmt.Errorf("unable to get transfer leaf %s: %v", req.LeafId, err)
	}

	refundTxBytes, err := common.UpdateTxWithSignature(transferLeaf.IntermediateRefundTx, 0, req.RefundSignature)
	if err != nil {
		return fmt.Errorf("unable to update refund tx with signature: %v", err)
	}

	if transfer.Type != schema.TransferTypePreimageSwap {
		// Verify signature
		refundTx, err := common.TxFromRawTxBytes(refundTxBytes)
		if err != nil {
			return fmt.Errorf("unable to deserialize refund tx: %v", err)
		}
		leafNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
		if err != nil {
			return fmt.Errorf("unable to deserialize leaf tx: %v", err)
		}
		if len(leafNodeTx.TxOut) <= 0 {
			return fmt.Errorf("vout out of bounds")
		}
		err = common.VerifySignature(refundTx, 0, leafNodeTx.TxOut[0])
		if err != nil {
			return fmt.Errorf("unable to verify refund tx signature: %v", err)
		}
	}

	transferLeafMutator := db.TransferLeaf.
		UpdateOne(transferLeaf).
		SetIntermediateRefundTx(refundTxBytes).
		SetSecretCipher(req.SecretCipher).
		SetSignature(req.Signature)
	if !shouldTweakKey {
		keyTweak, err := proto.Marshal(req)
		if err != nil {
			return fmt.Errorf("unable to marshal key tweak: %v", err)
		}
		transferLeafMutator.SetKeyTweak(keyTweak)
	}
	_, err = transferLeafMutator.Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer leaf: %v", err)
	}

	if shouldTweakKey {
		err = h.tweakLeafKey(ctx, leaf, req, refundTxBytes)
		if err != nil {
			return fmt.Errorf("unable to tweak leaf key: %v", err)
		}
	}

	return nil
}

func (h *TransferHandler) tweakLeafKey(ctx context.Context, leaf *ent.TreeNode, req *pb.SendLeafKeyTweak, updatedRefundTx []byte) error {
	// Tweak keyshare
	keyshare, err := leaf.QuerySigningKeyshare().First(ctx)
	if err != nil || keyshare == nil {
		return fmt.Errorf("unable to load keyshare for leaf %s: %v", req.LeafId, err)
	}
	keyshare, err = keyshare.TweakKeyShare(
		ctx,
		req.SecretShareTweak.SecretShare,
		req.SecretShareTweak.Proofs[0],
		req.PubkeySharesTweak,
	)
	if err != nil || keyshare == nil {
		return fmt.Errorf("unable to tweak keyshare %s for leaf %s: %v", keyshare.ID.String(), req.LeafId, err)
	}

	// Update leaf
	signingPubkey, err := common.SubtractPublicKeys(leaf.VerifyingPubkey, keyshare.PublicKey)
	if err != nil {
		return fmt.Errorf("unable to calculate new signing pubkey for leaf %s: %v", req.LeafId, err)
	}
	leafMutator := leaf.
		Update().
		SetOwnerSigningPubkey(signingPubkey)
	if updatedRefundTx != nil {
		leafMutator.SetRawRefundTx(updatedRefundTx)
	}
	leaf, err = leafMutator.Save(ctx)
	if err != nil || leaf == nil {
		return fmt.Errorf("unable to update leaf %s: %v", req.LeafId, err)
	}
	return nil
}

// QueryPendingTransfers queries the pending transfers to claim.
func (h *TransferHandler) QueryPendingTransfers(ctx context.Context, req *pb.QueryPendingTransfersRequest) (*pb.QueryPendingTransfersResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.ReceiverIdentityPublicKey); err != nil {
		return nil, err
	}

	transferPredicate := []predicate.Transfer{
		enttransfer.ReceiverIdentityPubkeyEQ(req.ReceiverIdentityPublicKey),
		enttransfer.StatusEQ(schema.TransferStatusSenderKeyTweaked),
		enttransfer.Or(
			enttransfer.ExpiryTimeGT(time.Now()),
			enttransfer.ExpiryTimeEQ(time.Unix(0, 0)),
		),
	}
	if req.TransferIds != nil {
		transferUUIDs := make([]uuid.UUID, len(req.TransferIds))
		for _, transferID := range req.TransferIds {
			transferUUID, err := uuid.Parse(transferID)
			if err != nil {
				return nil, fmt.Errorf("unable to parse transfer id as a uuid %s: %v", transferID, err)
			}
			transferUUIDs = append(transferUUIDs, transferUUID)
		}
		transferPredicate = append([]predicate.Transfer{enttransfer.IDIn(transferUUIDs...)}, transferPredicate...)
	}

	db := ent.GetDbFromContext(ctx)
	transfers, err := db.Transfer.Query().Where(enttransfer.And(transferPredicate...)).All(ctx)
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

const CoopExitConfirmationThreshold = 6

func checkCoopExitTxBroadcasted(ctx context.Context, db *ent.Tx, transferID uuid.UUID, networks []common.Network) error {
	coopExit, err := db.CooperativeExit.Query().Where(
		cooperativeexit.HasTransferWith(enttransfer.ID(transferID)),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to find coop exit for transfer %s: %v", transferID.String(), err)
	}
	schemaNetworks := []schema.Network{}
	for _, network := range networks {
		schemaNetworks = append(schemaNetworks, common.SchemaNetwork(network))
	}
	blockHeight, err := db.BlockHeight.Query().Where(
		blockheight.NetworkIn(schemaNetworks...),
	).Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to find block height: %v", err)
	}
	if coopExit.ConfirmationHeight == 0 {
		return fmt.Errorf("coop exit tx hasn't been broadcasted")
	}
	if coopExit.ConfirmationHeight+CoopExitConfirmationThreshold-1 > blockHeight.Height {
		return fmt.Errorf("coop exit tx doesn't have enough confirmations: confirmation height: %d current block height: %d", coopExit.ConfirmationHeight, blockHeight.Height)
	}
	return nil
}

// ClaimTransferTweakKeys starts claiming a pending transfer by tweaking keys of leaves.
func (h *TransferHandler) ClaimTransferTweakKeys(ctx context.Context, req *pb.ClaimTransferTweakKeysRequest) error {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.OwnerIdentityPublicKey); err != nil {
		return err
	}

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
	if !bytes.Equal(transfer.ReceiverIdentityPubkey, req.OwnerIdentityPublicKey) || transfer.Status != schema.TransferStatusSenderKeyTweaked || (transfer.ExpiryTime.Unix() != 0 && transfer.ExpiryTime.Before(time.Now())) {
		return fmt.Errorf("transfer cannot be claimed %s", req.TransferId)
	}

	if err := checkCoopExitTxBroadcasted(ctx, db, transferID, h.config.SupportedNetworks); err != nil {
		return fmt.Errorf("failed to unlock transfer %s: %v", req.TransferId, err)
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
	for _, leafTweak := range req.LeavesToReceive {
		leaf, exists := (*leaves)[leafTweak.LeafId]
		if !exists {
			return fmt.Errorf("unexpected leaf id %s", leafTweak.LeafId)
		}
		err = h.claimLeafTweakKey(ctx, leaf, leafTweak, req.OwnerIdentityPublicKey)
		if err != nil {
			return fmt.Errorf("unable to tweak key for leaf %s: %v", leafTweak.LeafId, err)
		}
	}

	// Update transfer status
	_, err = transfer.Update().SetStatus(schema.TransferStatusReceiverKeyTweaked).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status %s: %v", transfer.ID.String(), err)
	}

	return nil
}

func (h *TransferHandler) claimLeafTweakKey(ctx context.Context, leaf *ent.TreeNode, req *pb.ClaimLeafKeyTweak, ownerIdentityPubkey []byte) error {
	err := secretsharing.ValidateShare(
		&secretsharing.VerifiableSecretShare{
			SecretShare: secretsharing.SecretShare{
				FieldModulus: secp256k1.S256().N,
				Threshold:    int(h.config.Threshold),
				Index:        big.NewInt(int64(h.config.Index + 1)),
				Share:        new(big.Int).SetBytes(req.SecretShareTweak.SecretShare),
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
		req.SecretShareTweak.SecretShare,
		req.SecretShareTweak.Proofs[0],
		req.PubkeySharesTweak,
	)
	if err != nil {
		return fmt.Errorf("unable to tweak keyshare %s for leaf %s: %v", keyshare.ID.String(), leaf.ID.String(), err)
	}

	signingPubkey, err := common.SubtractPublicKeys(leaf.VerifyingPubkey, keyshare.PublicKey)
	if err != nil {
		return fmt.Errorf("unable to calculate new signing pubkey for leaf %s: %v", req.LeafId, err)
	}
	_, err = leaf.
		Update().
		SetOwnerIdentityPubkey(ownerIdentityPubkey).
		SetOwnerSigningPubkey(signingPubkey).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update leaf %s: %v", req.LeafId, err)
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
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.OwnerIdentityPublicKey); err != nil {
		return nil, err
	}

	transferID, err := uuid.Parse(req.TransferId)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %v", req.TransferId, err)
	}
	db := ent.GetDbFromContext(ctx)
	transfer, err := db.Transfer.Get(ctx, transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to find pending transfer %s: %v", req.TransferId, err)
	}
	if !bytes.Equal(transfer.ReceiverIdentityPubkey, req.OwnerIdentityPublicKey) || transfer.Status != schema.TransferStatusReceiverKeyTweaked {
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
	signingResultProtos := []*pb.LeafRefundTxSigningResult{}
	for _, signingResult := range signingResults {
		leafID := jobToLeafMap[signingResult.JobID]
		leaf := (*leaves)[leafID.String()]
		signingResultProto, err := signingResult.MarshalProto()
		if err != nil {
			return nil, err
		}
		signingResultProtos = append(signingResultProtos, &pb.LeafRefundTxSigningResult{
			LeafId:                leafID.String(),
			RefundTxSigningResult: signingResultProto,
			VerifyingKey:          leaf.VerifyingPubkey,
		})
	}

	// Update transfer status
	_, err = transfer.Update().SetStatus(schema.TransferStatusReceiverRefundSigned).Save(ctx)
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
	if len(leafTx.TxOut) <= 0 {
		return nil, fmt.Errorf("vout out of bounds")
	}
	refundSigningJob, _, err := helper.NewSigningJob(keyshare, job, leafTx.TxOut[0], nil)
	if err != nil {
		return nil, fmt.Errorf("unable to create signing job for leaf %s: %v", leaf.ID.String(), err)
	}
	return refundSigningJob, nil
}
