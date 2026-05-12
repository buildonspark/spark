package handler

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"time"

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
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/mimo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (h *TransferHandler) startTransferV3Internal(
	ctx context.Context,
	req *pb.StartTransferV3Request,
) (resp *pb.StartTransferResponse, retErr error) {
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

	// Multi-receiver transfers require the MIMO knob to be enabled.
	if len(receivers) > 1 {
		if knobs.GetKnobsService(ctx).GetValue(knobs.KnobMimoTransferMultiReceiverEnabled, 0) == 0 {
			return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("multi-receiver transfers are not enabled"))
		}
	}

	// Validate transfer package.
	leafTweakMap, err := h.ValidateTransferPackage(ctx, transferID, senderPkg.TransferPackage, senderIDPK, true /* requireDirectFromCpfpLeaves */)
	if err != nil {
		return nil, fmt.Errorf("failed to validate transfer package for transfer %s: %w", transferID, err)
	}
	if len(leafTweakMap) == 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("transfer package contains no key tweaks"))
	}

	// Verify the transfer size doesn't exceed the transfer limit.
	transferLimit := knobs.GetKnobsService(ctx).GetValue(knobs.KnobSoTransferLimit, 0)
	if transferLimit > 0 && len(leafTweakMap) > int(transferLimit) {
		return nil, status.Errorf(codes.InvalidArgument, "transfer limit reached, please send %d leaves at a time", int(transferLimit))
	}

	leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap := loadLeafRefundMapsFromTransferPackage(senderPkg.TransferPackage)

	// Mutual exclusivity
	if err := createPendingSendTransferAndCommit(ctx, transferID); err != nil {
		return nil, err
	}

	// Rollback PendingSendTransfer on any failure between here and the success
	// point. cancelGossip is set to true before syncTransferV3Init so that a
	// sync failure also cancels the gossip messages sent to other SOs.
	needsRollback := true
	cancelGossip := false
	defer func() {
		if !needsRollback || retErr == nil {
			return
		}
		if rbErr := h.rollbackTransferInit(ctx, transferID, cancelGossip); rbErr != nil {
			retErr = fmt.Errorf("rollback failed: %w while processing transfer %s: %w", rbErr, transferID, retErr)
		}
	}()

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
		return nil, fmt.Errorf("failed to create transfer for transfer %s: %w", transferID, err)
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

	cancelGossip = true
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
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to sync transfer V3 init for transfer %s", transferID)
		return nil, fmt.Errorf("failed to sync transfer V3 init for transfer %s: %w", transferID, err)
	}

	// After this point, the transfer send is considered successful.
	needsRollback = false

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

// shouldRouteToOutgoingInFlight reports whether the request should dispatch
// to queryOutgoingInFlight. Both the filter shape AND the knob must allow it:
//
//   - Filter shape: sender-only participant + non-empty status filter that's
//     a subset of OutgoingInFlightSenderStatuses (the partial index's WHERE
//     clause). SR1 (sender_or_receiver) and receiver-only participants fall
//     through to legacy; mixed/wider status sets fall through too.
//   - Knob: KnobReadMIMODataModelOutgoingInFlight is a per-call RolloutRandom
//     probability (0–100). Bare value is the broad rollout percentage; no
//     per-pubkey overrides — keep the dispatcher uniform and the routing
//     decision simple.
//
// Caller shapes this routes (per the cross-axis audit):
//   - queryPrimarySwapTransfers (TS1)
//   - queryPendingOutgoingTransfers (TS3)
//   - getOwnedBalance sender path (GOB1)
func shouldRouteToOutgoingInFlight(ctx context.Context, filter *pb.TransferFilter) bool {
	if !knobs.GetKnobsService(ctx).RolloutRandom(knobs.KnobReadMIMODataModelOutgoingInFlight, 0) {
		return false
	}
	if filter.GetSenderIdentityPublicKey() == nil {
		return false
	}
	if len(filter.Statuses) == 0 {
		return false
	}
	for _, protoStatus := range filter.Statuses {
		schemaStatus, err := ent.TransferStatusSchema(protoStatus)
		if err != nil {
			return false
		}
		if !mimo.IsOutgoingInFlightStatus(schemaStatus) {
			return false
		}
	}
	return true
}

// queryOutgoingInFlight handles QueryAllTransfers requests whose filter shape
// matches sender + status-subset-of-the-4-state-outgoing-in-flight set. The
// SQL drives idx_transfers_outgoing_in_flight_sender_pubkey_time directly:
// column-based leading equality on sender_identity_pubkey, status filter
// implied by the partial's WHERE, top-N pushdown via the matching ORDER BY.
//
// Routing in QueryAllTransfers guarantees:
//   - filter.GetSenderIdentityPublicKey() != nil
//   - filter.Statuses is a non-empty subset of OutgoingInFlightSenderStatuses
//   - KnobReadMIMODataModelOutgoingInFlight is on
func (h *TransferHandler) queryOutgoingInFlight(ctx context.Context, filter *pb.TransferFilter, isSSP bool) (resp *pb.QueryTransfersResponse, err error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.queryOutgoingInFlight")
	defer span.End()

	start := time.Now()
	defer func() {
		resultCount := 0
		if resp != nil {
			resultCount = len(resp.Transfers)
		}
		logQueryTransfersInvocation(ctx, "query_outgoing_in_flight", filter,
			zap.Bool("is_ssp", isSSP),
			zap.Bool("use_mimo", true),
			zap.Duration("elapsed", time.Since(start)),
			zap.Int("result_count", resultCount),
			zap.Error(err),
		)
	}()

	if filter.GetCreatedAfter() != nil && filter.GetCreatedBefore() != nil {
		return nil, status.Error(codes.InvalidArgument, "cannot specify both created_after and created_before filters")
	}
	if filter.GetNetwork() == pb.Network_UNSPECIFIED {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("filter.Network must be specified"))
	}

	walletPubkey, err := keys.ParsePublicKey(filter.GetSenderIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid sender identity public key: %w", err)
	}

	statuses := make([]st.TransferStatus, len(filter.Statuses))
	for i, s := range filter.Statuses {
		statuses[i], err = ent.TransferStatusSchema(s)
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer status: %w", err))
		}
	}

	metrics := newTransferQueryRecorder(transferQueryAttrs{
		QueryPath:       "query_outgoing_in_flight",
		MIMOEnabled:     true,
		FilterType:      "sender",
		HasStatusFilter: true,
		HasTypeFilter:   len(filter.Types) > 0,
	})

	if !isSSP {
		hasReadAccess, err := NewWalletSettingHandler(h.config).HasReadAccessToWallet(ctx, walletPubkey)
		if err != nil {
			return nil, fmt.Errorf("failed to check read access for wallet %s: %w", walletPubkey, err)
		}
		if !hasReadAccess {
			metrics.record(ctx, 0, nil)
			return &pb.QueryTransfersResponse{Offset: -1}, nil
		}
	}

	limit, offset := normalizePendingPagination(filter.Limit, filter.Offset)

	args := mimo.OutgoingInFlightArgs{
		WalletPubkey:      walletPubkey,
		Statuses:          statuses,
		Network:           filter.GetNetwork(),
		Types:             filter.GetTypes(),
		TransferIDsFilter: filter.GetTransferIds(),
		HasCreatedAfter:   filter.GetCreatedAfter() != nil,
		CreatedAfter:      timeOrZero(filter.GetCreatedAfter()),
		HasCreatedBefore:  filter.GetCreatedBefore() != nil,
		CreatedBefore:     timeOrZero(filter.GetCreatedBefore()),
		Order:             filter.Order,
		Limit:             limit,
		Offset:            offset,
	}

	query, sqlArgs, err := mimo.BuildOutgoingInFlightQuery(args)
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db from context: %w", err)
	}

	//nolint:forbidigo // raw SQL needed for partial-index-driven query.
	rows, err := db.QueryContext(ctx, query, sqlArgs...)
	if err != nil {
		metrics.record(ctx, 0, err)
		return nil, fmt.Errorf("failed to execute outgoing-in-flight query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	transferIDs := make([]uuid.UUID, 0, limit)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan transfer ID: %w", err)
		}
		transferIDs = append(transferIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	if len(transferIDs) == 0 {
		metrics.record(ctx, 0, nil)
		return &pb.QueryTransfersResponse{Offset: -1}, nil
	}

	orderFn := ent.Desc(enttransfer.FieldCreateTime)
	idOrderFn := ent.Desc(enttransfer.FieldID)
	if filter.Order == pb.Order_ASCENDING {
		orderFn = ent.Asc(enttransfer.FieldCreateTime)
		idOrderFn = ent.Asc(enttransfer.FieldID)
	}
	transfers, err := db.Transfer.Query().
		Where(enttransfer.IDIn(transferIDs...)).
		WithSparkInvoice().
		WithTransferSenders().
		WithTransferReceivers().
		WithTransferLeaves(func(q *ent.TransferLeafQuery) {
			q.WithLeaf(func(q *ent.TreeNodeQuery) {
				q.WithTree().WithSigningKeyshare().WithParent()
			})
		}).
		Order(orderFn, idOrderFn).
		All(ctx)
	metrics.record(ctx, len(transfers), err)
	if err != nil {
		return nil, fmt.Errorf("failed to load transfers: %w", err)
	}

	transferProtos := make([]*pb.Transfer, 0, len(transfers))
	for _, t := range transfers {
		var transferProto *pb.Transfer
		if t.HasReceiver(walletPubkey) {
			transferProto, err = t.MarshalProtoForReceiver(ctx, walletPubkey)
		} else {
			transferProto, err = t.MarshalProto(ctx)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to marshal transfer %s: %w", t.ID, err)
		}
		transferProtos = append(transferProtos, transferProto)
	}

	// Gate and advance by SQL ID count, not ORM count — concurrent deletes shouldn't reshape pagination.
	nextOffset := int64(-1)
	if len(transferIDs) == limit {
		nextOffset = int64(offset + len(transferIDs))
	}
	return &pb.QueryTransfersResponse{
		Transfers: transferProtos,
		Offset:    nextOffset,
	}, nil
}
