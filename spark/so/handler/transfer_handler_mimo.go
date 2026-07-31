package handler

import (
	"context"
	"fmt"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/uuids"
	"go.uber.org/zap"

	"github.com/google/uuid"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferreceiver "github.com/lightsparkdev/spark/so/ent/transferreceiver"
	enttransfersender "github.com/lightsparkdev/spark/so/ent/transfersender"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/mimo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// convertV2ToV3SendTransferRequest maps a v2 StartTransferRequest onto the v3
// StartTransferV3Request shape consumed by the consensus engine. A v2 request
// targets a single receiver for the whole transfer, so every leaf in the
// transfer package is mapped to that one receiver. The caller must have
// verified req.TransferPackage is non-nil. The spark_invoice (which v3 cannot
// represent) is carried separately by the caller, not on the returned request.
func convertV2ToV3SendTransferRequest(req *pb.StartTransferRequest) *pb.StartTransferV3Request {
	pkg := req.GetTransferPackage()
	receiver := req.GetReceiverIdentityPublicKey()
	receiverMap := make(map[string][]byte)
	for _, jobs := range [][]*pb.UserSignedTxSigningJob{
		pkg.GetLeavesToSend(),
		pkg.GetDirectLeavesToSend(),
		pkg.GetDirectFromCpfpLeavesToSend(),
	} {
		for _, job := range jobs {
			receiverMap[job.GetLeafId()] = receiver
		}
	}

	return &pb.StartTransferV3Request{
		TransferId: req.GetTransferId(),
		ExpiryTime: req.GetExpiryTime(),
		SenderPackages: []*pb.SenderTransferPackage{
			{
				OwnerIdentityPublicKey:     req.GetOwnerIdentityPublicKey(),
				TransferPackage:            pkg,
				ReceiverIdentityPublicKeys: receiverMap,
			},
		},
	}
}

// A signature with no manifest covers nothing at all, so it is refused rather than ignored — a
// sender that sent one believes something was bound. Presence of the manifest itself is not
// required here: start_transfer_v3 is the generic path, and each fee flow requires the manifest on
// its own endpoint.
func rejectStrayManifestSignature(ctx context.Context, endpoint manifestBindEndpoint, req *pb.StartTransferV3Request) error {
	if req.GetTransferManifest() != nil {
		return nil
	}
	for _, senderPkg := range req.GetSenderPackages() {
		if len(senderPkg.GetManifestHashSignature()) > 0 {
			recordManifestRefusal(ctx, endpoint, manifestRefusalStraySignature)
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("manifest_hash_signature set without a transfer_manifest"))
		}
	}
	return nil
}

// startTransferV3Consensus runs the v3 send-transfer flow through the 2PC
// consensus engine. It is the only path for v3 send-transfers, reached from
// StartTransferV3 and from StartTransferV2 for TransferPackage-carrying
// requests.
//
// The stale-flow sweep does not need to undo coordinator domain writes: the
// engine records the COMMITTED decision atomically with the coordinator's
// SENDER_KEY_TWEAKED write (single request-tx DbCommit), so a crash before
// that commit leaves the transfer untweaked with an IN_FLIGHT row (sweep →
// ROLLED_BACK, consistent) and a crash after it leaves a COMMITTED row the
// reconciler drives forward (SP-3195).
//
// This is intentionally a thin entry point: only the cheap structural checks,
// session-identity auth, and the manifest refusal run on the coordinator before
// the engine fans out. The expensive package validation
// (ValidateTransferPackage / createTransferV3 / transfer-size limit) lives
// inside Prepare so it runs concurrently across all SOs rather than serially
// on the coordinator first.
//
// sparkInvoice is carried separately from req because the public
// StartTransferV3Request has no invoice field; it is non-empty only when a
// StartTransferV2 request routed through the engine paid an invoice.
func (h *TransferHandler) startTransferV3Consensus(
	ctx context.Context,
	req *pb.StartTransferV3Request,
	sparkInvoice string,
) (*pb.StartTransferResponse, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.startTransferV3Consensus")
	defer span.End()

	// Entry-gate precedence is pinned by tests: envelope checks, then session
	// auth, then the manifest refusal, and only then the full parse.
	_, senderIDPK, err := parseSendTransferEnvelope(req)
	if err != nil {
		return nil, err
	}

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, senderIDPK); err != nil {
		return nil, err
	}
	if err := authz.EnforceWalletNotKillSwitched(ctx, senderIDPK); err != nil {
		return nil, err
	}

	if err := rejectStrayManifestSignature(ctx, manifestEndpointStartTransferV3, req); err != nil {
		return nil, err
	}

	// No PendingSendTransfer guard on the consensus path — the engine's
	// FlowExecution row plus createTransferV3's unique constraint on
	// Transfer.id already provide mutual exclusivity and recovery. Two
	// concurrent calls with the same transferID both reach Prepare; the
	// second's createTransferV3 fails on the duplicate row, the engine
	// rolls back, and the participant reconciler cleans up. Other consensus
	// flows (renew_leaf) follow the same pattern.

	parsed, err := parseSendTransferRequest(req)
	if err != nil {
		return nil, err
	}

	flow, err := buildSendTransferCoordinatorFlow(ctx, h.config, req, parsed, sparkInvoice, NewSendTransferFlowHandler(h.config))
	if err != nil {
		return nil, err
	}
	engine, err := consensus.GetEngine(ctx)
	if err != nil {
		return nil, err
	}
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	if _, err := engine.Execute(ctx,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
		&selection,
		flow,
	); err != nil {
		// Two of engine.Execute's failure modes need distinguishing:
		//   (a) commit-decision preempted by the self-sweep (ErrCoordinatorRowPreempted):
		//       the request tx was rolled back, so the transfer is NOT committed and
		//       both coordinator and participants converge on rolled-back. Safe to fail.
		//   (b) commit-gossip dispatch failure (after the atomic DbCommit): the transfer
		//       IS committed and gossip/the reconciler drive participants forward from
		//       the persisted FlowExecution row.
		// In both cases returning an error to the client is correct (the transfer is
		// idempotent via Transfer.id); the wrapped inner error carries the distinction
		// for the on-call signal.
		return nil, fmt.Errorf("consensus send transfer failed: %w", err)
	}

	// flow.response is set inside BuildCommitPayload. engine.Execute returns
	// nil only after BuildCommitPayload + DbCommit both succeed, so a nil
	// response here means the contract was violated (e.g., a future engine
	// refactor moves response construction off the synchronous path). Surface
	// it loudly instead of returning nil to the client.
	if flow.response == nil {
		return nil, fmt.Errorf("internal: consensus send transfer for %s succeeded but produced no response", req.GetTransferId())
	}
	return flow.response, nil
}

// finishQueryFromIDs wraps the post-SQL-scan tail shared by every MIMO query
// handler: empty-result short-circuit, load + marshal via
// loadAndMarshalTransfersByIDs, metrics recording, and nextOffset computation.
// limit/offset are the post-normalization values; nextOffset advances by SQL ID
// count (not ORM count) so concurrent deletes don't reshape pagination.
func finishQueryFromIDs(
	ctx context.Context,
	db *ent.Client,
	transferIDs []uuid.UUID,
	walletPubkey keys.Public,
	order pb.Order,
	limit, offset int,
	metrics *transferQueryRecorder,
) (*pb.QueryTransfersResponse, error) {
	if len(transferIDs) == 0 {
		metrics.record(ctx, 0, nil)
		return &pb.QueryTransfersResponse{Offset: -1}, nil
	}
	transferProtos, err := loadAndMarshalTransfersByIDs(ctx, db, transferIDs, walletPubkey, order)
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
	transfers, err := withTransferQueryEdges(
		db.Transfer.Query().Where(enttransfer.IDIn(ids...)),
	).Order(orderFn, idOrderFn).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load transfers: %w", err)
	}
	protos := make([]*pb.Transfer, 0, len(transfers))
	for _, t := range transfers {
		p, err := marshalTransferForWallet(ctx, t, &walletPubkey)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal transfer %s: %w", t.ID, err)
		}
		protos = append(protos, p)
	}
	return protos, nil
}

// validateBaseTransferFilter checks the four cross-handler input invariants
// shared by every QueryAllTransfers entry point.
func validateBaseTransferFilter(filter *pb.TransferFilter) error {
	if filter.GetLimit() < 0 {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("limit must be non-negative"))
	}
	if filter.GetOffset() < 0 {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("offset must be non-negative"))
	}
	if filter.GetCreatedAfter() != nil && filter.GetCreatedBefore() != nil {
		return status.Error(codes.InvalidArgument, "cannot specify both created_after and created_before filters")
	}
	if filter.GetNetwork() == pb.Network_UNSPECIFIED {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("filter.Network must be specified"))
	}
	return nil
}

// validateWalletReadAccess gates !isSSP MIMO query handlers on HasReadAccessToWallet.
// Denial returns the empty short-circuit response, matching legacy queryTransfers
// (empty page rather than PermissionDenied — avoids leaking wallet existence via
// gRPC code).
func (h *TransferHandler) validateWalletReadAccess(
	ctx context.Context,
	walletPubkey keys.Public,
	isSSP bool,
	metrics *transferQueryRecorder,
) (*pb.QueryTransfersResponse, error) {
	if isSSP {
		return nil, nil
	}
	hasReadAccess, err := NewWalletSettingHandler(h.config).HasReadAccessToWallet(ctx, walletPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to check read access for wallet %s: %w", walletPubkey, err)
	}
	if !hasReadAccess {
		metrics.record(ctx, 0, nil)
		return &pb.QueryTransfersResponse{Offset: -1}, nil
	}
	return nil, nil
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
// to queryOutgoingInFlight when the filter shape allows it: sender-only
// participant + non-empty status filter that's a subset of
// OutgoingInFlightSenderStatuses (the partial index's WHERE clause).
// sender_or_receiver and receiver-only participants fall through to the
// remaining routes (ultimately queryByParticipantFallback); mixed/wider
// status sets fall through too.
//
// Known caller shapes: the SDK's queryPrimarySwapTransfers and
// queryPendingOutgoingTransfers, and getOwnedBalance's sender path.
func shouldRouteToOutgoingInFlight(filter *pb.TransferFilter) bool {
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
func (h *TransferHandler) queryOutgoingInFlight(ctx context.Context, filter *pb.TransferFilter, isSSP bool) (resp *pb.QueryTransfersResponse, err error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.queryOutgoingInFlight")
	defer span.End()

	start := time.Now()
	defer func() {
		resultCount := 0
		if resp != nil {
			resultCount = len(resp.GetTransfers())
		}
		logQueryTransfersInvocation(ctx, "query_outgoing_in_flight", filter, time.Since(start),
			zap.Bool("is_ssp", isSSP),
			zap.Int("result_count", resultCount),
			zap.Error(err),
		)
	}()

	if err := validateBaseTransferFilter(filter); err != nil {
		return nil, err
	}

	walletPubkey, err := keys.ParsePublicKey(filter.GetSenderIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid sender identity public key: %w", err)
	}

	statuses := make([]st.TransferStatus, len(filter.GetStatuses()))
	for i, s := range filter.GetStatuses() {
		statuses[i], err = ent.TransferStatusSchema(s)
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer status: %w", err))
		}
	}

	metrics := newTransferQueryRecorder(transferQueryAttrs{
		QueryPath:       "query_outgoing_in_flight",
		FilterType:      "sender",
		HasStatusFilter: true,
		HasTypeFilter:   len(filter.GetTypes()) > 0,
		HasTransferIDs:  len(filter.GetTransferIds()) > 0,
	})

	if resp, err := h.validateWalletReadAccess(ctx, walletPubkey, isSSP, metrics); resp != nil || err != nil {
		return resp, err
	}

	limit, offset := normalizeTransferPagination(filter.GetLimit(), filter.GetOffset())

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
		Order:             filter.GetOrder(),
		Limit:             limit,
		Offset:            offset,
	}

	query, sqlArgs, err := mimo.BuildOutgoingInFlightQuery(args)
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get db from context: %w", err))
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

	return finishQueryFromIDs(ctx, db, transferIDs, walletPubkey, filter.GetOrder(), limit, offset, metrics)
}

// shouldRouteToByTypes reports whether the request can dispatch to
// queryByTypes. The shape predicate is intentionally narrow: type filter set,
// no status filter, no transfer-id filter. Under those
// conditions both arms collapse to an index-only walk on
// (identity_pubkey, transfer_type, create_time, transfer_id), and the
// sender_or_receiver path is a straight UNION-DISTINCT with no
// status-collapsing translation logic.
func shouldRouteToByTypes(filter *pb.TransferFilter) bool {
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
// / idx_transfersender_pubkey_type_time directly; the sender_or_receiver path
// UNIONs the two arms and dedups for self-transfers.
//
// Routing in QueryAllTransfers guarantees:
//   - filter.Participant identifies one of sender / receiver / sender_or_receiver
//   - len(filter.Types) > 0
//   - filter.Statuses, filter.TransferIds are empty
func (h *TransferHandler) queryByTypes(ctx context.Context, filter *pb.TransferFilter, isSSP bool) (resp *pb.QueryTransfersResponse, err error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.queryByTypes")
	defer span.End()

	start := time.Now()
	defer func() {
		resultCount := 0
		if resp != nil {
			resultCount = len(resp.GetTransfers())
		}
		logQueryTransfersInvocation(ctx, "query_by_types", filter, time.Since(start),
			zap.Bool("is_ssp", isSSP),
			zap.Int("result_count", resultCount),
			zap.Error(err),
		)
	}()

	if err := validateBaseTransferFilter(filter); err != nil {
		return nil, err
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
		FilterType:     filterType,
		HasTypeFilter:  true,
		HasTransferIDs: len(filter.GetTransferIds()) > 0,
	})

	if resp, err := h.validateWalletReadAccess(ctx, walletPubkey, isSSP, metrics); resp != nil || err != nil {
		return resp, err
	}

	baseArgs.WalletPubkey = walletPubkey
	query, sqlArgs, err := build(baseArgs)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to build query: %w", err))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get db from context: %w", err))
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

	return finishQueryFromIDs(ctx, db, transferIDs, walletPubkey, filter.GetOrder(), limit, offset, metrics)
}

// shouldRouteToReceiverByTypeStatus reports whether the request can dispatch
// to queryReceiverByTypeStatus. The shape requires receiver participant,
// non-empty types AND statuses, no transfer-id filter, and every requested
// status must translate to a receiver-axis equivalent. sender_or_receiver and
// sender participants fall through to the remaining routes (ultimately
// queryByParticipantFallback).
func shouldRouteToReceiverByTypeStatus(filter *pb.TransferFilter) bool {
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
	// translation entry; falls through to the participant fallback rather than
	// running an incomplete-coverage query.
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
// idx_transferreceiver_claim_pending_pubkey_time for post-tweak-active rows,
// idx_transferreceiver_initiated_pubkey_type_time for INITIATED rows, and
// idx_transferreceiver_pubkey_type_time for terminal rows.
//
// Routing in QueryAllTransfers guarantees:
//   - filter.Participant is ReceiverIdentityPublicKey
//   - len(filter.Types) > 0 and len(filter.Statuses) > 0
//   - filter.TransferIds is empty
//   - every requested status is receiver-axis translatable
//
// The call surface is exposed publicly, but the dominant caller is the SSP's
// gen_all_inbound_transfers.
func (h *TransferHandler) queryReceiverByTypeStatus(ctx context.Context, filter *pb.TransferFilter, isSSP bool) (resp *pb.QueryTransfersResponse, err error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.queryReceiverByTypeStatus")
	defer span.End()

	start := time.Now()
	defer func() {
		resultCount := 0
		if resp != nil {
			resultCount = len(resp.GetTransfers())
		}
		logQueryTransfersInvocation(ctx, "query_receiver_by_type_status", filter, time.Since(start),
			zap.Bool("is_ssp", isSSP),
			zap.Int("result_count", resultCount),
			zap.Error(err),
		)
	}()

	if err := validateBaseTransferFilter(filter); err != nil {
		return nil, err
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
		FilterType:      filterType,
		HasTypeFilter:   true,
		HasStatusFilter: true,
		HasTransferIDs:  false,
	})

	if resp, err := h.validateWalletReadAccess(ctx, walletPubkey, isSSP, metrics); resp != nil || err != nil {
		return resp, err
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
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get db from context: %w", err))
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

	return finishQueryFromIDs(ctx, db, transferIDs, walletPubkey, filter.GetOrder(), limit, offset, metrics)
}

// shouldRouteToCounterSwap reports whether the request can dispatch to
// queryCounterSwap. The shape requires sender-or-receiver participant,
// non-empty types where every type is in {COUNTER_SWAP, COUNTER_SWAP_V3},
// non-empty statuses where every status is receiver-axis translatable,
// and no transfer-id filter. Narrow type scoping matches the SDK's
// queryCounterSwapTransfers caller — broadening to arbitrary types lands
// sender-or-receiver traffic on a path whose per-arm perf hasn't been
// validated for that shape.
func shouldRouteToCounterSwap(filter *pb.TransferFilter) bool {
	if len(filter.GetTransferIds()) != 0 {
		return false
	}
	if _, ok := filter.GetParticipant().(*pb.TransferFilter_SenderOrReceiverIdentityPublicKey); !ok {
		return false
	}
	if len(filter.GetTypes()) == 0 || len(filter.GetStatuses()) == 0 {
		return false
	}
	for _, t := range filter.GetTypes() {
		schemaType, err := st.TransferTypeFromProto(t.String())
		if err != nil {
			return false
		}
		if schemaType != st.TransferTypeCounterSwap && schemaType != st.TransferTypeCounterSwapV3 {
			return false
		}
	}
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

// queryCounterSwap handles QueryAllTransfers sender-or-receiver requests
// carrying a counter-swap type filter and a status filter — the SDK's
// queryCounterSwapTransfers caller. The SQL builder emits an asymmetric
// cross-arm UNION DISTINCT (column-based sender arm + edge-based receiver
// arm); see mimo.BuildCounterSwapQuery for the full design rationale —
// per-status sender decomposition, per-type × per-bucket receiver
// structure, collapsing-narrowing optimization, and the MIMO v0
// single-sender assumption.
//
// Routing in QueryAllTransfers guarantees:
//   - filter.Participant is SenderOrReceiverIdentityPublicKey
//   - len(filter.Types) > 0 and every type is in {COUNTER_SWAP, COUNTER_SWAP_V3}
//   - len(filter.Statuses) > 0 and every status is receiver-axis translatable
//   - filter.TransferIds is empty
func (h *TransferHandler) queryCounterSwap(ctx context.Context, filter *pb.TransferFilter, isSSP bool) (resp *pb.QueryTransfersResponse, err error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.queryCounterSwap")
	defer span.End()

	start := time.Now()
	defer func() {
		resultCount := 0
		if resp != nil {
			resultCount = len(resp.GetTransfers())
		}
		logQueryTransfersInvocation(ctx, "query_counter_swap", filter, time.Since(start),
			zap.Bool("is_ssp", isSSP),
			zap.Int("result_count", resultCount),
			zap.Error(err),
		)
	}()

	if err := validateBaseTransferFilter(filter); err != nil {
		return nil, err
	}

	limit, offset := normalizeTransferPagination(filter.GetLimit(), filter.GetOffset())

	statuses := make([]st.TransferStatus, len(filter.GetStatuses()))
	for i, s := range filter.GetStatuses() {
		schemaStatus, schemaErr := ent.TransferStatusSchema(s)
		if schemaErr != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer status: %w", schemaErr))
		}
		statuses[i] = schemaStatus
	}

	walletPubkey, _, filterType, err := extractParticipant(filter)
	if err != nil {
		return nil, err
	}

	metrics := newTransferQueryRecorder(transferQueryAttrs{
		QueryPath:       "query_counter_swap",
		FilterType:      filterType,
		HasTypeFilter:   true,
		HasStatusFilter: true,
		HasTransferIDs:  false,
	})

	if resp, err := h.validateWalletReadAccess(ctx, walletPubkey, isSSP, metrics); resp != nil || err != nil {
		return resp, err
	}

	args := mimo.CounterSwapArgs{
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

	query, sqlArgs, err := mimo.BuildCounterSwapQuery(args)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to build query: %w", err))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get db from context: %w", err))
	}

	//nolint:forbidigo // raw SQL drives partial + composite indexes directly.
	rows, err := db.QueryContext(ctx, query, sqlArgs...)
	if err != nil {
		metrics.record(ctx, 0, err)
		return nil, fmt.Errorf("failed to execute counter-swap query: %w", err)
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

	return finishQueryFromIDs(ctx, db, transferIDs, walletPubkey, filter.GetOrder(), limit, offset, metrics)
}

// shouldRouteToByParticipantFallback reports whether the request should dispatch to
// queryByParticipantFallback. Fallback only claims participant-bearing shapes —
// nil-participant traffic stays on legacy queryTransfers, which has the
// per-transfer access-check pass that this handler doesn't replicate.
func shouldRouteToByParticipantFallback(filter *pb.TransferFilter) bool {
	return filter.GetParticipant() != nil
}

// queryByParticipantFallback handles QueryAllTransfers requests via Ent-based edge
// predicates — the correctness floor for any participant-bearing shape that
// fell through the specialized handlers. The load-bearing invariant: status
// filtering on the receiver arm applies to transfer_receivers.status, not
// transfers.status. For multi-receiver MIMO transfers the parent can lag
// behind an individual receiver — legacy's parent-axis filter silently drops
// the row when a sibling hasn't settled.
//
// Routing in QueryAllTransfers guarantees:
//   - filter.GetParticipant() != nil
//   - No specialized handler claimed this shape upstream
//
// Not a hot-path handler — the correctness floor. Multi-second outliers
// already exist on legacy queryTransfers, so a slow call here isn't
// necessarily new; lift recurring hot shapes into specialized handlers.
func (h *TransferHandler) queryByParticipantFallback(ctx context.Context, filter *pb.TransferFilter, isSSP bool) (resp *pb.QueryTransfersResponse, err error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.queryByParticipantFallback")
	defer span.End()

	start := time.Now()
	defer func() {
		resultCount := 0
		if resp != nil {
			resultCount = len(resp.GetTransfers())
		}
		logQueryTransfersInvocation(ctx, "query_by_participant_fallback", filter, time.Since(start),
			zap.Bool("is_ssp", isSSP),
			zap.Int("result_count", resultCount),
			zap.Error(err),
		)
	}()

	if err := validateBaseTransferFilter(filter); err != nil {
		return nil, err
	}

	network, err := btcnetwork.FromProtoNetwork(filter.GetNetwork())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to convert proto network to schema network: %w", err))
	}

	if filter.GetParticipant() == nil {
		return nil, status.Error(codes.InvalidArgument, "queryByParticipantFallback requires a participant")
	}

	statuses := make([]st.TransferStatus, len(filter.GetStatuses()))
	for i, s := range filter.GetStatuses() {
		schemaStatus, statusErr := ent.TransferStatusSchema(s)
		if statusErr != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer status: %w", statusErr))
		}
		statuses[i] = schemaStatus
	}

	walletPubkey, role, filterType, err := extractParticipant(filter)
	if err != nil {
		return nil, err
	}

	metrics := newTransferQueryRecorder(transferQueryAttrs{
		QueryPath:       "query_by_participant_fallback",
		FilterType:      filterType,
		HasStatusFilter: len(filter.GetStatuses()) > 0,
		HasTypeFilter:   len(filter.GetTypes()) > 0,
		HasTransferIDs:  len(filter.GetTransferIds()) > 0,
	})

	if resp, err := h.validateWalletReadAccess(ctx, walletPubkey, isSSP, metrics); resp != nil || err != nil {
		return resp, err
	}

	limit, offset := normalizeTransferPagination(filter.GetLimit(), filter.GetOffset())

	participantPred, err := buildByParticipantFallbackParticipantPredicate(role, walletPubkey, statuses)
	if err != nil {
		return nil, err
	}
	preds := []predicate.Transfer{enttransfer.NetworkEQ(network), participantPred}

	if len(filter.GetTransferIds()) > 0 {
		if len(filter.GetTransferIds()) > maxTransferIDFilterValues {
			return nil, sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("there were %d transfer ids provided, but the max is %d", len(filter.GetTransferIds()), maxTransferIDFilterValues))
		}
		transferUUIDs, parseErr := uuids.ParseSlice(filter.GetTransferIds())
		if parseErr != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse transfer IDs as UUIDs: %w", parseErr))
		}
		preds = append(preds, enttransfer.IDIn(transferUUIDs...))
	}

	if len(filter.GetTypes()) > 0 {
		transferTypes := make([]st.TransferType, len(filter.GetTypes()))
		for i, protoType := range filter.GetTypes() {
			schemaType, typeErr := st.TransferTypeFromProto(protoType.String())
			if typeErr != nil {
				return nil, status.Errorf(codes.InvalidArgument, "invalid transfer type: %s", protoType.String())
			}
			transferTypes[i] = schemaType
		}
		preds = append(preds, enttransfer.TypeIn(transferTypes...))
	}

	if filter.GetCreatedAfter() != nil {
		preds = append(preds, enttransfer.CreateTimeGT(filter.GetCreatedAfter().AsTime().UTC()))
	} else if filter.GetCreatedBefore() != nil {
		preds = append(preds, enttransfer.CreateTimeLT(filter.GetCreatedBefore().AsTime().UTC()))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get db from context: %w", err))
	}

	orderFn := ent.Desc(enttransfer.FieldCreateTime)
	idOrderFn := ent.Desc(enttransfer.FieldID)
	if filter.GetOrder() == pb.Order_ASCENDING {
		orderFn = ent.Asc(enttransfer.FieldCreateTime)
		idOrderFn = ent.Asc(enttransfer.FieldID)
	}

	transferIDs, err := db.Transfer.Query().
		Where(enttransfer.And(preds...)).
		Order(orderFn, idOrderFn).
		Limit(limit).
		Offset(offset).
		IDs(ctx)
	if err != nil {
		metrics.record(ctx, 0, err)
		return nil, fmt.Errorf("failed to query transfer IDs: %w", err)
	}

	return finishQueryFromIDs(ctx, db, transferIDs, walletPubkey, filter.GetOrder(), limit, offset, metrics)
}

// buildByParticipantFallbackParticipantPredicate builds the participant-axis
// predicate for queryByParticipantFallback. Sender-axis status filters apply to
// transfers.status (the parent axis is unambiguous for sender); receiver-axis
// status filters apply to transfer_receivers.status via the receiver-axis
// translation + narrowing — preserving MIMO correctness for multi-receiver
// transfers.
func buildByParticipantFallbackParticipantPredicate(role participantRole, walletPubkey keys.Public, statuses []st.TransferStatus) (predicate.Transfer, error) {
	switch role {
	case participantRoleSender:
		senderArm := enttransfer.HasTransferSendersWith(enttransfersender.IdentityPubkeyEQ(walletPubkey))
		if len(statuses) > 0 {
			return enttransfer.And(senderArm, enttransfer.StatusIn(statuses...)), nil
		}
		return senderArm, nil
	case participantRoleReceiver:
		return buildReceiverArmPredicate(walletPubkey, statuses), nil
	case participantRoleSenderOrReceiver:
		senderArm := enttransfer.HasTransferSendersWith(enttransfersender.IdentityPubkeyEQ(walletPubkey))
		if len(statuses) > 0 {
			senderArm = enttransfer.And(senderArm, enttransfer.StatusIn(statuses...))
		}
		return enttransfer.Or(senderArm, buildReceiverArmPredicate(walletPubkey, statuses)), nil
	default:
		return nil, fmt.Errorf("unsupported participant role: %d", role)
	}
}

// buildReceiverArmPredicate composes the receiver-axis predicate for a given
// wallet pubkey and (optional) status filter. With no statuses, just the edge
// existence. With statuses, mirror ReceiverArmFilters' SQL form in Ent:
//
//	r.status IN $indexSet AND (r.status IN $exactMatch OR t.status IN $narrowing)
//
// Untranslatable statuses are silently dropped by ReceiverArmFilters. If
// every input status is untranslatable (indexSet empty) the receiver arm
// contributes nothing — IDEQ(uuid.Nil) is a never-matches predicate that
// composes cleanly under Or in the sender_or_receiver case.
func buildReceiverArmPredicate(walletPubkey keys.Public, statuses []st.TransferStatus) predicate.Transfer {
	if len(statuses) == 0 {
		return enttransfer.HasTransferReceiversWith(enttransferreceiver.IdentityPubkeyEQ(walletPubkey))
	}
	indexSet, exactMatch, narrowingTransfer := mimo.ReceiverArmFilters(statuses)
	if len(indexSet) == 0 {
		return enttransfer.IDEQ(uuid.Nil)
	}

	inIndexSet := enttransfer.HasTransferReceiversWith(
		enttransferreceiver.IdentityPubkeyEQ(walletPubkey),
		enttransferreceiver.StatusIn(indexSet...),
	)

	var orArms []predicate.Transfer
	if len(exactMatch) > 0 {
		orArms = append(orArms, enttransfer.HasTransferReceiversWith(
			enttransferreceiver.IdentityPubkeyEQ(walletPubkey),
			enttransferreceiver.StatusIn(exactMatch...),
		))
	}
	if len(narrowingTransfer) > 0 {
		orArms = append(orArms, enttransfer.StatusIn(narrowingTransfer...))
	}
	switch len(orArms) {
	case 0:
		return inIndexSet
	case 1:
		return enttransfer.And(inIndexSet, orArms[0])
	default:
		return enttransfer.And(inIndexSet, enttransfer.Or(orArms...))
	}
}
