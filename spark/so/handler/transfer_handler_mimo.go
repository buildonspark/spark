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

// loadAndMarshalTransfersByIDs loads transfers by ID with the standard preload
// graph and marshals each one with the wallet-receiver projection when the
// wallet is on the receiver side, MarshalProto otherwise. Shared across the
// MIMO-style two-phase query handlers (SQL → IDs → ORM IDIn → marshal).
func loadAndMarshalTransfersByIDs(ctx context.Context, db *ent.Client, ids []uuid.UUID, walletPubkey keys.Public, order pb.Order) ([]*pb.Transfer, error) {
	orderFn := ent.Desc(enttransfer.FieldCreateTime)
	idOrderFn := ent.Desc(enttransfer.FieldID)
	if order == pb.Order_ASCENDING {
		orderFn = ent.Asc(enttransfer.FieldCreateTime)
		idOrderFn = ent.Asc(enttransfer.FieldID)
	}
	transfers, err := db.Transfer.Query().
		Where(enttransfer.IDIn(ids...)).
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
	if err != nil {
		return nil, fmt.Errorf("failed to load transfers: %w", err)
	}
	protos := make([]*pb.Transfer, 0, len(transfers))
	for _, t := range transfers {
		var p *pb.Transfer
		if t.HasReceiver(walletPubkey) {
			p, err = t.MarshalProtoForReceiver(ctx, walletPubkey)
		} else {
			p, err = t.MarshalProto(ctx)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to marshal transfer %s: %w", t.ID, err)
		}
		protos = append(protos, p)
	}
	return protos, nil
}

// extractParticipant resolves the TransferFilter participant oneof into the
// wallet pubkey and matching participantRole + display label. Shared between
// the two-phase MIMO query handlers so they dispatch on the same enum rather
// than re-doing the oneof type-switch per call site.
func extractParticipant(filter *pb.TransferFilter) (keys.Public, participantRole, string, error) {
	switch p := filter.GetParticipant().(type) {
	case *pb.TransferFilter_ReceiverIdentityPublicKey:
		pubkey, err := keys.ParsePublicKey(p.ReceiverIdentityPublicKey)
		if err != nil {
			return keys.Public{}, 0, "", sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid receiver identity public key: %w", err))
		}
		return pubkey, participantRoleReceiver, "receiver", nil
	case *pb.TransferFilter_SenderIdentityPublicKey:
		pubkey, err := keys.ParsePublicKey(p.SenderIdentityPublicKey)
		if err != nil {
			return keys.Public{}, 0, "", sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid sender identity public key: %w", err))
		}
		return pubkey, participantRoleSender, "sender", nil
	case *pb.TransferFilter_SenderOrReceiverIdentityPublicKey:
		pubkey, err := keys.ParsePublicKey(p.SenderOrReceiverIdentityPublicKey)
		if err != nil {
			return keys.Public{}, 0, "", sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid sender or receiver identity public key: %w", err))
		}
		return pubkey, participantRoleSenderOrReceiver, "sender_or_receiver", nil
	default:
		return keys.Public{}, 0, "", status.Errorf(codes.InvalidArgument, "unsupported participant variant: %T", p)
	}
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
	if len(filter.GetStatuses()) == 0 {
		return false
	}
	for _, protoStatus := range filter.GetStatuses() {
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
		HasTransferIDs:  len(filter.TransferIds) > 0,
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

	limit, offset := normalizeTransferPagination(filter.Limit, filter.Offset)

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
			metrics.record(ctx, 0, err)
			return nil, fmt.Errorf("failed to scan transfer ID: %w", err)
		}
		transferIDs = append(transferIDs, id)
	}
	if err := rows.Err(); err != nil {
		metrics.record(ctx, 0, err)
		return nil, fmt.Errorf("rows error: %w", err)
	}

	if len(transferIDs) == 0 {
		metrics.record(ctx, 0, nil)
		return &pb.QueryTransfersResponse{Offset: -1}, nil
	}

	transferProtos, err := loadAndMarshalTransfersByIDs(ctx, db, transferIDs, walletPubkey, filter.Order)
	metrics.record(ctx, len(transferProtos), err)
	if err != nil {
		return nil, err
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

// shouldRouteToByTypes reports whether the request can dispatch to
// queryByTypes. The shape predicate is intentionally narrow: type filter set,
// no status filter, no transfer-id filter. Under those
// conditions both arms collapse to an index-only walk on
// (identity_pubkey, transfer_type, create_time, transfer_id), and SR1 is a
// straight UNION-DISTINCT with no status-collapsing translation logic.
func shouldRouteToByTypes(ctx context.Context, filter *pb.TransferFilter) bool {
	if !knobs.GetKnobsService(ctx).RolloutRandom(knobs.KnobReadMIMODataModelQueryByTypes, 0) {
		return false
	}
	if len(filter.GetTypes()) == 0 {
		return false
	}
	if len(filter.GetStatuses()) != 0 {
		return false
	}
	if len(filter.GetTransferIds()) != 0 {
		return false
	}
	switch filter.GetParticipant().(type) {
	case *pb.TransferFilter_SenderIdentityPublicKey,
		*pb.TransferFilter_ReceiverIdentityPublicKey,
		*pb.TransferFilter_SenderOrReceiverIdentityPublicKey:
		return true
	default:
		return false
	}
}

// queryByTypes handles QueryAllTransfers requests with a type filter and no
// status filter. The per-arm SQL drives idx_transferreceiver_pubkey_type_time
// / idx_transfersender_pubkey_type_time directly; SR1 UNIONs the two arms and
// dedups for self-transfers.
//
// Routing in QueryAllTransfers guarantees:
//   - filter.Participant identifies one of sender / receiver / SR1
//   - len(filter.Types) > 0
//   - filter.Statuses, filter.TransferIds are empty
//   - KnobReadMIMODataModelQueryByTypes is on
func (h *TransferHandler) queryByTypes(ctx context.Context, filter *pb.TransferFilter, isSSP bool) (resp *pb.QueryTransfersResponse, err error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.queryByTypes")
	defer span.End()

	start := time.Now()
	defer func() {
		resultCount := 0
		if resp != nil {
			resultCount = len(resp.Transfers)
		}
		logQueryTransfersInvocation(ctx, "query_by_types", filter,
			zap.Bool("is_ssp", isSSP),
			zap.Bool("use_mimo", true),
			zap.Duration("elapsed", time.Since(start)),
			zap.Int("result_count", resultCount),
			zap.Error(err),
		)
	}()

	if filter.GetLimit() < 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("limit must be non-negative"))
	}
	if filter.GetOffset() < 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("offset must be non-negative"))
	}
	if filter.GetCreatedAfter() != nil && filter.GetCreatedBefore() != nil {
		return nil, status.Error(codes.InvalidArgument, "cannot specify both created_after and created_before filters")
	}
	if filter.GetNetwork() == pb.Network_UNSPECIFIED {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("filter.Network must be specified"))
	}

	limit, offset := normalizeTransferPagination(filter.GetLimit(), filter.GetOffset())

	baseArgs := mimo.ByTypesArgs{
		Network:          filter.GetNetwork(),
		Types:            filter.GetTypes(),
		HasCreatedAfter:  filter.GetCreatedAfter() != nil,
		CreatedAfter:     timeOrZero(filter.GetCreatedAfter()),
		HasCreatedBefore: filter.GetCreatedBefore() != nil,
		CreatedBefore:    timeOrZero(filter.GetCreatedBefore()),
		Order:            filter.GetOrder(),
		Limit:            limit,
		Offset:           offset,
	}

	walletPubkey, role, filterType, err := extractParticipant(filter)
	if err != nil {
		return nil, err
	}

	var build func(mimo.ByTypesArgs) (string, []any, error)
	switch role {
	case participantRoleSender:
		build = mimo.BuildByTypesQuerySender
	case participantRoleReceiver:
		build = mimo.BuildByTypesQueryReceiver
	case participantRoleSenderOrReceiver:
		build = mimo.BuildByTypesQuerySenderOrReceiver
	}

	metrics := newTransferQueryRecorder(transferQueryAttrs{
		QueryPath:      "query_by_types",
		MIMOEnabled:    true,
		FilterType:     filterType,
		HasTypeFilter:  true,
		HasTransferIDs: len(filter.GetTransferIds()) > 0,
	})

	if !isSSP {
		hasReadAccess, accessErr := NewWalletSettingHandler(h.config).HasReadAccessToWallet(ctx, walletPubkey)
		if accessErr != nil {
			return nil, fmt.Errorf("failed to check read access for wallet %s: %w", walletPubkey, accessErr)
		}
		if !hasReadAccess {
			metrics.record(ctx, 0, nil)
			return &pb.QueryTransfersResponse{Offset: -1}, nil
		}
	}

	baseArgs.WalletPubkey = walletPubkey
	query, sqlArgs, err := build(baseArgs)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to build query: %w", err))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db from context: %w", err)
	}

	//nolint:forbidigo // raw SQL drives the type composite directly.
	rows, err := db.QueryContext(ctx, query, sqlArgs...)
	if err != nil {
		metrics.record(ctx, 0, err)
		return nil, fmt.Errorf("failed to execute by-types query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	transferIDs := make([]uuid.UUID, 0, limit)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			metrics.record(ctx, 0, err)
			return nil, fmt.Errorf("failed to scan transfer ID: %w", err)
		}
		transferIDs = append(transferIDs, id)
	}
	if err := rows.Err(); err != nil {
		metrics.record(ctx, 0, err)
		return nil, fmt.Errorf("rows error: %w", err)
	}

	if len(transferIDs) == 0 {
		metrics.record(ctx, 0, nil)
		return &pb.QueryTransfersResponse{Offset: -1}, nil
	}

	transferProtos, err := loadAndMarshalTransfersByIDs(ctx, db, transferIDs, walletPubkey, filter.GetOrder())
	metrics.record(ctx, len(transferProtos), err)
	if err != nil {
		return nil, err
	}

	nextOffset := int64(-1)
	if len(transferIDs) == limit {
		nextOffset = int64(offset + len(transferIDs))
	}
	return &pb.QueryTransfersResponse{
		Transfers: transferProtos,
		Offset:    nextOffset,
	}, nil
}

// shouldRouteToReceiverByTypeStatus reports whether the request can dispatch
// to queryReceiverByTypeStatus. The shape requires receiver participant,
// non-empty types AND statuses, no transfer-id filter, and every requested
// status must translate to a receiver-axis equivalent. SR1 and sender
// participants stay on legacy until per-caller handlers exist.
func shouldRouteToReceiverByTypeStatus(ctx context.Context, filter *pb.TransferFilter) bool {
	if !knobs.GetKnobsService(ctx).RolloutRandom(knobs.KnobReadMIMODataModelReceiverByTypeStatus, 0) {
		return false
	}
	if len(filter.GetTransferIds()) != 0 {
		return false
	}
	if _, ok := filter.GetParticipant().(*pb.TransferFilter_ReceiverIdentityPublicKey); !ok {
		return false
	}
	if len(filter.GetTypes()) == 0 || len(filter.GetStatuses()) == 0 {
		return false
	}
	// Future-proofs against a new TransferStatus enum value landing without a
	// translation entry; falls through to legacy rather than running an
	// incomplete-coverage query.
	for _, s := range filter.GetStatuses() {
		schemaStatus, err := ent.TransferStatusSchema(s)
		if err != nil {
			return false
		}
		if !mimo.IsReceiverAxisTranslatable(schemaStatus) {
			return false
		}
	}
	return true
}

// queryReceiverByTypeStatus handles QueryAllTransfers receiver-arm requests
// carrying both a type filter and a status filter. The SQL builder translates
// each requested t.status to its r.status equivalent, splits the translated
// set across the receiver claim-pending partial index's WHERE coverage, and
// emits per-type × per-bucket UNION ALL — driving
// idx_transferreceiver_claim_pending_pubkey_time for post-tweak-active rows
// and idx_transferreceiver_pubkey_type_time for INITIATED / terminal rows.
//
// Routing in QueryAllTransfers guarantees:
//   - filter.Participant is ReceiverIdentityPublicKey
//   - len(filter.Types) > 0 and len(filter.Statuses) > 0
//   - filter.TransferIds is empty
//   - every requested status is receiver-axis translatable
//   - KnobReadMIMODataModelReceiverByTypeStatus is on
func (h *TransferHandler) queryReceiverByTypeStatus(ctx context.Context, filter *pb.TransferFilter, isSSP bool) (resp *pb.QueryTransfersResponse, err error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.queryReceiverByTypeStatus")
	defer span.End()

	start := time.Now()
	defer func() {
		resultCount := 0
		if resp != nil {
			resultCount = len(resp.Transfers)
		}
		logQueryTransfersInvocation(ctx, "query_receiver_by_type_status", filter,
			zap.Bool("is_ssp", isSSP),
			zap.Bool("use_mimo", true),
			zap.Duration("elapsed", time.Since(start)),
			zap.Int("result_count", resultCount),
			zap.Error(err),
		)
	}()

	if filter.GetLimit() < 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("limit must be non-negative"))
	}
	if filter.GetOffset() < 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("offset must be non-negative"))
	}
	if filter.GetCreatedAfter() != nil && filter.GetCreatedBefore() != nil {
		return nil, status.Error(codes.InvalidArgument, "cannot specify both created_after and created_before filters")
	}
	if filter.GetNetwork() == pb.Network_UNSPECIFIED {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("filter.Network must be specified"))
	}

	limit, offset := normalizeTransferPagination(filter.GetLimit(), filter.GetOffset())

	statuses := make([]st.TransferStatus, len(filter.GetStatuses()))
	for i, s := range filter.GetStatuses() {
		schemaStatus, err := ent.TransferStatusSchema(s)
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer status: %w", err))
		}
		statuses[i] = schemaStatus
	}

	walletPubkey, _, filterType, err := extractParticipant(filter)
	if err != nil {
		return nil, err
	}

	metrics := newTransferQueryRecorder(transferQueryAttrs{
		QueryPath:       "query_receiver_by_type_status",
		MIMOEnabled:     true,
		FilterType:      filterType,
		HasTypeFilter:   true,
		HasStatusFilter: true,
		HasTransferIDs:  false,
	})

	if !isSSP {
		hasReadAccess, accessErr := NewWalletSettingHandler(h.config).HasReadAccessToWallet(ctx, walletPubkey)
		if accessErr != nil {
			return nil, fmt.Errorf("failed to check read access for wallet %s: %w", walletPubkey, accessErr)
		}
		if !hasReadAccess {
			metrics.record(ctx, 0, nil)
			return &pb.QueryTransfersResponse{Offset: -1}, nil
		}
	}

	args := mimo.ReceiverByTypeStatusArgs{
		WalletPubkey:     walletPubkey,
		Network:          filter.GetNetwork(),
		Types:            filter.GetTypes(),
		Statuses:         statuses,
		HasCreatedAfter:  filter.GetCreatedAfter() != nil,
		CreatedAfter:     timeOrZero(filter.GetCreatedAfter()),
		HasCreatedBefore: filter.GetCreatedBefore() != nil,
		CreatedBefore:    timeOrZero(filter.GetCreatedBefore()),
		Order:            filter.GetOrder(),
		Limit:            limit,
		Offset:           offset,
	}

	query, sqlArgs, err := mimo.BuildReceiverByTypeStatusQuery(args)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to build query: %w", err))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db from context: %w", err)
	}

	//nolint:forbidigo // raw SQL drives partial + composite indexes directly.
	rows, err := db.QueryContext(ctx, query, sqlArgs...)
	if err != nil {
		metrics.record(ctx, 0, err)
		return nil, fmt.Errorf("failed to execute receiver-by-type-status query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	transferIDs := make([]uuid.UUID, 0, limit)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			metrics.record(ctx, 0, err)
			return nil, fmt.Errorf("failed to scan transfer ID: %w", err)
		}
		transferIDs = append(transferIDs, id)
	}
	if err := rows.Err(); err != nil {
		metrics.record(ctx, 0, err)
		return nil, fmt.Errorf("rows error: %w", err)
	}

	if len(transferIDs) == 0 {
		metrics.record(ctx, 0, nil)
		return &pb.QueryTransfersResponse{Offset: -1}, nil
	}

	transferProtos, err := loadAndMarshalTransfersByIDs(ctx, db, transferIDs, walletPubkey, filter.GetOrder())
	metrics.record(ctx, len(transferProtos), err)
	if err != nil {
		return nil, err
	}

	nextOffset := int64(-1)
	if len(transferIDs) == limit {
		nextOffset = int64(offset + len(transferIDs))
	}
	return &pb.QueryTransfersResponse{
		Transfers: transferProtos,
		Offset:    nextOffset,
	}, nil
}
