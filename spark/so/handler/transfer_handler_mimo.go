package handler

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/lightsparkdev/spark/common/keys"
	"go.uber.org/zap"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/pendingsendtransfer"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (h *TransferHandler) startTransferV3Internal(
	ctx context.Context,
	req *pb.StartTransferV3Request,
) (*pb.StartTransferResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "TransferHandler.startTransferV3Internal")
	defer span.End()

	// MVP: single sender only.
	if len(req.SenderPackages) != 1 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("expected exactly 1 sender package, got %d", len(req.SenderPackages)))
	}
	senderPkg := req.SenderPackages[0]

	if senderPkg.TransferPackage == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_package is required"))
	}

	// Auth
	senderIDPK, err := keys.ParsePublicKey(senderPkg.OwnerIdentityPublicKey)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse owner identity public key: %w", err))
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, senderIDPK); err != nil {
		return nil, err
	}

	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer id: %w", err))
	}

	// Parse receivers from the leaf→receiver map.
	leafReceiverMap := make(map[string]keys.Public)
	receiverSet := make(map[string]keys.Public)
	for leafID, receiverBytes := range senderPkg.ReceiverIdentityPublicKeys {
		recvPK, err := keys.ParsePublicKey(receiverBytes)
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse receiver public key for leaf %s: %w", leafID, err))
		}
		leafReceiverMap[leafID] = recvPK
		receiverSet[string(recvPK.Serialize())] = recvPK
	}
	if len(receiverSet) == 0 {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("at least one receiver required"))
	}
	receivers := make([]keys.Public, 0, len(receiverSet))
	for _, pk := range receiverSet {
		receivers = append(receivers, pk)
	}
	slices.SortFunc(receivers, func(a, b keys.Public) int {
		return bytes.Compare(a.Serialize(), b.Serialize())
	})

	// Validate transfer package.
	leafTweakMap, err := h.ValidateTransferPackage(ctx, transferID, senderPkg.TransferPackage, senderIDPK, true /* requireDirectFromCpfpLeaves */)
	if err != nil {
		return nil, fmt.Errorf("failed to validate transfer package for transfer %s: %w", transferID, err)
	}
	if len(leafTweakMap) == 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("transfer package contains no key tweaks"))
	}

	// Verify the transfer size doesn't exceed the transfer limit.
	knobService := knobs.GetKnobsService(ctx)
	if knobService != nil {
		transferLimit := knobService.GetValue(knobs.KnobSoTransferLimit, 0)
		if transferLimit > 0 && len(leafTweakMap) > int(transferLimit) {
			return nil, status.Errorf(codes.InvalidArgument, "transfer limit reached, please send %d leaves at a time", int(transferLimit))
		}
	}

	leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap := loadLeafRefundMapsFromTransferPackage(senderPkg.TransferPackage)

	// Mutual exclusivity
	if err := createPendingSendTransferAndCommit(ctx, transferID); err != nil {
		return nil, err
	}

	// Create transfer with multiple receivers.
	transfer, leafMap, err := h.createTransferV3(
		ctx,
		transferID,
		senderPkg.TransferPackage,
		req.ExpiryTime.AsTime(),
		senderIDPK,
		receivers,
		leafReceiverMap,
		leafCpfpRefundMap,
		leafDirectRefundMap,
		leafDirectFromCpfpRefundMap,
		leafTweakMap,
		TransferRoleCoordinator,
		true, /* requireDirectTx */
	)
	if err != nil {
		originalErr := err
		if rbErr := h.rollbackTransferInit(ctx, transferID, false /* cancelGossip */); rbErr != nil {
			return nil, fmt.Errorf("rollback failed: %w while creating transfer: %w", rbErr, originalErr)
		}
		return nil, fmt.Errorf("failed to create transfer for transfer %s: %w", transferID, originalErr)
	}

	refundSignatures, err := h.signAggregateAndUpdateRefunds(
		ctx, transfer, transferID.String(), senderPkg.TransferPackage, leafMap,
		keys.Public{}, keys.Public{}, keys.Public{}, nil,
	)
	if err != nil {
		return nil, err
	}

	// Build signing result protos for the response.
	signingResultProtos, err := buildSigningResultProtos(leafMap, refundSignatures.cpfpSigningResultMap, refundSignatures.directSigningResultMap, refundSignatures.directFromCpfpSigningResultMap)
	if err != nil {
		return nil, err
	}

	// Gossip sync: notify other SOs using InitiateTransferV2.
	senderKeyTweakProofs := make(map[string]*pb.SecretProof)
	for _, leaf := range leafTweakMap {
		senderKeyTweakProofs[leaf.LeafId] = &pb.SecretProof{
			Proofs: leaf.SecretShareTweak.Proofs,
		}
	}

	err = h.syncTransferV3Init(
		ctx,
		req,
		senderPkg,
		senderKeyTweakProofs,
		refundSignatures.finalCpfpSignatureMap,
		refundSignatures.finalDirectSignatureMap,
		refundSignatures.finalDfcSignatureMap,
	)
	if err != nil {
		syncErr := err
		logger.With(zap.Error(syncErr)).Sugar().Errorf("Failed to sync transfer V3 init for transfer %s", transferID)
		if rbErr := h.rollbackTransferInit(ctx, transferID, true /* cancelGossip */); rbErr != nil {
			return nil, fmt.Errorf("rollback failed: %w while syncing transfer V3 %s: %w", rbErr, transferID, syncErr)
		}
		return nil, fmt.Errorf("failed to sync transfer V3 init for transfer %s: %w", transferID, syncErr)
	}

	// After this point, the transfer send is considered successful.

	// Commit and settle key tweaks.
	entTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get db before key tweak settlement: %w", err)
	}
	if err := entTx.Commit(); err != nil {
		return nil, fmt.Errorf("unable to commit db before key tweak settlement: %w", err)
	}

	// Settle sender key tweaks via gossip.
	if err := h.syncSettleSenderKeyTweaks(ctx, transfer.ID.String(), senderKeyTweakProofs); err != nil {
		return nil, err
	}

	transfer, err = h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get database transaction: %w", err)
	}
	_, err = db.PendingSendTransfer.Update().Where(pendingsendtransfer.TransferID(transfer.ID)).SetStatus(st.PendingSendTransferStatusFinished).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update pending send transfer: %w", err)
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Unable to marshal transfer %s", transfer.ID)
	}

	return &pb.StartTransferResponse{Transfer: transferProto, SigningResults: signingResultProtos}, nil
}

func (h *TransferHandler) syncTransferV3Init(
	ctx context.Context,
	req *pb.StartTransferV3Request,
	senderPkg *pb.SenderTransferPackage,
	senderKeyTweakProofs map[string]*pb.SecretProof,
	cpfpRefundSignatures map[string][]byte,
	directRefundSignatures map[string][]byte,
	directFromCpfpRefundSignatures map[string][]byte,
) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.syncTransferV3Init")
	defer span.End()

	initReq := &pbinternal.InitiateTransferV2Request{
		TransferId: req.TransferId,
		SenderPackages: []*pbinternal.InitiateTransferSenderPackage{{
			SenderIdentityPublicKey:        senderPkg.OwnerIdentityPublicKey,
			TransferPackage:                senderPkg.TransferPackage,
			ReceiverIdentityPublicKeys:     senderPkg.ReceiverIdentityPublicKeys,
			RefundSignatures:               cpfpRefundSignatures,
			DirectRefundSignatures:         directRefundSignatures,
			DirectFromCpfpRefundSignatures: directFromCpfpRefundSignatures,
		}},
		SenderKeyTweakProofs: senderKeyTweakProofs,
		ExpiryTime:           req.ExpiryTime,
	}

	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.InitiateTransferV2(ctx, initReq)
	})
	return err
}
