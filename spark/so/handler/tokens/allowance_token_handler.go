package tokens

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenallowance"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type AllowanceTokenHandler struct {
	config *so.Config
	// gossip is injected rather than constructed here: so/handler imports this package for the
	// consensus flow handlers, so importing it back would be an import cycle.
	gossip consensus.GossipSender
}

func NewAllowanceTokenHandler(config *so.Config, gossip consensus.GossipSender) *AllowanceTokenHandler {
	return &AllowanceTokenHandler{
		config: config,
		gossip: gossip,
	}
}

// allowancesEnabled reports whether the token allowance lifecycle RPCs are turned on for this SO.
func allowancesEnabled(ctx context.Context) bool {
	knobService := knobs.GetKnobsService(ctx)
	return knobService != nil && knobService.GetValue(knobs.KnobTokenAllowancesEnabled, 0) != 0
}

// CreateTokenAllowance installs an owner-signed spending allowance through the
// consensus engine so every operator validates and writes the grant atomically.
func (h *AllowanceTokenHandler) CreateTokenAllowance(ctx context.Context, req *tokenpb.CreateTokenAllowanceRequest) (*tokenpb.CreateTokenAllowanceResponse, error) {
	if !allowancesEnabled(ctx) {
		return nil, errors.UnimplementedMethodDisabled(fmt.Errorf("token allowances are not enabled"))
	}

	payload := req.GetAllowancePayload()
	allowanceID, err := uuid.FromBytes(payload.GetAllowanceId())
	if err != nil {
		return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("invalid allowance_id: %w", err))
	}
	ownerPublicKey, err := keys.ParsePublicKey(payload.GetOwnerPublicKey())
	if err != nil {
		return nil, errors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse owner public key: %w", err))
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, ownerPublicKey); err != nil {
		return nil, err
	}
	if err := authz.EnforceWalletNotKillSwitched(ctx, ownerPublicKey); err != nil {
		return nil, err
	}

	// Freshness is enforced only at this public edge; the internal replication path accepts the
	// original timestamp indefinitely so peers that were down can still recover the grant.
	if err := ValidateTimestampMillis(payload.GetOwnerProvidedTimestamp()); err != nil {
		return nil, err
	}
	if err := validateAllowanceCreatePreflight(ctx, h.config, ownerPublicKey, payload, req.GetOwnerSignature()); err != nil {
		return nil, err
	}

	engine, err := consensus.GetEngine(ctx)
	if err != nil {
		return nil, err
	}
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	flow := newCreateTokenAllowanceCoordinatorFlow(h.config, req, ownerPublicKey)
	if _, err := engine.Execute(
		ctx,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CREATE_TOKEN_ALLOWANCE,
		&selection,
		flow,
	); err != nil {
		return nil, errors.WrapErrorWithMessage(err, "consensus token allowance creation failed")
	}

	// Execute commits and clears the request transaction, so this query starts a fresh
	// transaction and observes the durable row, including state from an idempotent replay.
	row, err := ent.GetAllowanceByAllowanceID(ctx, allowanceID)
	if err != nil {
		logging.GetLoggerFromContext(ctx).With(zap.Error(err)).Sugar().Warnf(
			"token allowance %s committed but response read-back failed; returning success without allowance details", allowanceID)
		return &tokenpb.CreateTokenAllowanceResponse{}, nil
	}
	allowance, err := allowanceRowToInfo(row)
	if err != nil {
		// Consensus is already durable, so response enrichment cannot turn the
		// create into an apparent failure. Omitting details is safer than
		// fabricating current metering state for an idempotent replay.
		logging.GetLoggerFromContext(ctx).With(zap.Error(err)).Sugar().Warnf(
			"token allowance %s committed but response conversion failed; returning success without allowance details", allowanceID)
		return &tokenpb.CreateTokenAllowanceResponse{}, nil
	}
	return &tokenpb.CreateTokenAllowanceResponse{Allowance: allowance}, nil
}

// RevokeTokenAllowance tombstones an existing allowance so no further delegated spends succeed.
//
// Unlike create and query, revoke is deliberately NOT gated on the allowances-enable knob, so an
// owner can still revoke a grant they made while the feature was on. The wallet kill switch does
// gate it, like every other state-mutating handler.
func (h *AllowanceTokenHandler) RevokeTokenAllowance(ctx context.Context, req *tokenpb.RevokeTokenAllowanceRequest) (*tokenpb.RevokeTokenAllowanceResponse, error) {
	revokePayload := req.GetRevokeAllowancePayload()
	ownerPublicKey, err := keys.ParsePublicKey(revokePayload.GetOwnerPublicKey())
	if err != nil {
		return nil, errors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse owner public key: %w", err))
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, ownerPublicKey); err != nil {
		return nil, err
	}
	if err := authz.EnforceWalletNotKillSwitched(ctx, ownerPublicKey); err != nil {
		return nil, err
	}

	// Freshness is enforced only at this public edge; see CreateTokenAllowance for rationale.
	if err := ValidateTimestampMillis(revokePayload.GetOwnerProvidedTimestamp()); err != nil {
		return nil, err
	}

	if err := ValidateAndApplyRevokeAllowance(ctx, h.config, revokePayload, req.GetOwnerSignature()); err != nil {
		return nil, err
	}

	// Propagate the revocation to every other operator over durable gossip: the
	// Gossip row is created in the SAME transaction as the tombstone and
	// committed atomically, then best-effort sent. The send_gossip retry task
	// redelivers to any operator that has not acked until the per-participant
	// receipts bitmap is full, so a partial fan-out self-heals (fail-closed)
	// without owner action - replacing the previous best-effort fan-out and its
	// reconcile cron. Each operator re-verifies the owner signature carried in
	// the message, so durability confers no trust.
	excludeSelf := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	participants, err := excludeSelf.OperatorIdentifierList(h.config)
	if err != nil {
		return nil, errors.InternalUnhandledError(fmt.Errorf("failed to list operators for allowance revoke gossip: %w", err))
	}

	var gossipRow *ent.Gossip
	if len(participants) > 0 {
		gossipRow, err = h.gossip.CreateCommitAndSendGossipMessage(
			ctx, revokeAllowanceGossipMessage(revokePayload, req.GetOwnerSignature()), participants,
		)
		if err != nil {
			return nil, err
		}
	} else if err := ent.DbCommit(ctx); err != nil {
		// Single-operator federation: nothing to gossip, just commit the tombstone.
		return nil, errors.InternalDatabaseWriteError(fmt.Errorf("failed to commit token allowance revocation: %w", err))
	}

	return &tokenpb.RevokeTokenAllowanceResponse{
		AllowanceProgress: buildAllowanceRevokeProgress(h.config, participants, gossipRow),
	}, nil
}

// allowanceQueryPageSize is the default and maximum page size for
// QueryTokenAllowances. Requests asking for more are clamped, so a single call
// can never marshal an unbounded number of allowances.
const allowanceQueryPageSize = 100

// QueryTokenAllowances returns allowances matching the supplied filters. It is read-only and
// privacy-scoped: when authorization is enforced the session identity must equal the owner filter
// or the spender filter, so a caller can only enumerate allowances it is a party to. Results are
// paginated (limit/offset, stable row-id order); the response offset is -1 when there are no
// further results.
func (h *AllowanceTokenHandler) QueryTokenAllowances(ctx context.Context, req *tokenpb.QueryTokenAllowancesRequest) (*tokenpb.QueryTokenAllowancesResponse, error) {
	if !allowancesEnabled(ctx) {
		return nil, errors.UnimplementedMethodDisabled(fmt.Errorf("token allowances are not enabled"))
	}
	if req.GetLimit() < 0 {
		return nil, errors.InvalidArgumentOutOfRange(fmt.Errorf("limit must be non-negative"))
	}
	if req.GetOffset() < 0 {
		return nil, errors.InvalidArgumentOutOfRange(fmt.Errorf("offset must be non-negative"))
	}
	limit := allowanceQueryPageSize
	if req.GetLimit() > 0 && req.GetLimit() <= allowanceQueryPageSize {
		limit = int(req.GetLimit())
	}
	offset := int(req.GetOffset())

	var ownerFilter, spenderFilter *keys.Public
	if len(req.GetOwnerPublicKey()) > 0 {
		ownerKey, err := keys.ParsePublicKey(req.GetOwnerPublicKey())
		if err != nil {
			return nil, errors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse owner public key filter: %w", err))
		}
		ownerFilter = &ownerKey
	}
	if len(req.GetSpenderPublicKey()) > 0 {
		spenderKey, err := keys.ParsePublicKey(req.GetSpenderPublicKey())
		if err != nil {
			return nil, errors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse spender public key filter: %w", err))
		}
		spenderFilter = &spenderKey
	}

	if err := h.authorizeAllowanceQuery(ctx, ownerFilter, spenderFilter); err != nil {
		return nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	query := db.TokenAllowance.Query().Where(tokenallowance.FlowExecutionIDIsNil())
	if ownerFilter != nil {
		query = query.Where(tokenallowance.OwnerPublicKey(*ownerFilter))
	}
	if spenderFilter != nil {
		query = query.Where(tokenallowance.SpenderPublicKey(*spenderFilter))
	}
	if len(req.GetTokenIdentifier()) > 0 {
		query = query.Where(tokenallowance.TokenIdentifier(req.GetTokenIdentifier()))
	}
	if !req.GetIncludeInactive() {
		query = query.Where(tokenallowance.StatusEQ(schematype.TokenAllowanceStatusActive))
	}

	// Fetch one extra row beyond the page so a full final page (total an exact
	// multiple of the page size) is distinguishable from a page with more rows:
	// the next offset is returned only when that extra row actually exists.
	rows, err := query.
		Order(ent.Asc(tokenallowance.FieldID)).
		Offset(offset).
		Limit(limit + 1).
		All(ctx)
	if err != nil {
		return nil, errors.InternalDatabaseReadError(fmt.Errorf("failed to query token allowances: %w", err))
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	allowances := make([]*tokenpb.TokenAllowanceInfo, 0, len(rows))
	for _, row := range rows {
		info, err := allowanceRowToInfo(row)
		if err != nil {
			return nil, err
		}
		allowances = append(allowances, info)
	}

	nextOffset := int64(-1)
	if hasMore {
		nextOffset = req.GetOffset() + int64(limit)
	}
	return &tokenpb.QueryTokenAllowancesResponse{Allowances: allowances, Offset: nextOffset}, nil
}

// authorizeAllowanceQuery enforces that the caller is a party (owner or spender) to the allowances
// it is asking for. Mirrors the codebase convention of gating identity checks on IsAuthzEnforced.
func (h *AllowanceTokenHandler) authorizeAllowanceQuery(ctx context.Context, ownerFilter, spenderFilter *keys.Public) error {
	if !h.config.IsAuthzEnforced() {
		return nil
	}
	session, err := authn.GetSessionFromContext(ctx)
	if err != nil {
		return status.Error(codes.Unauthenticated, "no valid session found")
	}
	sessionKey := session.IdentityPublicKey()
	if ownerFilter != nil && sessionKey.Equals(*ownerFilter) {
		return nil
	}
	if spenderFilter != nil && sessionKey.Equals(*spenderFilter) {
		return nil
	}
	return errors.PermissionDeniedNoReadAccess(fmt.Errorf("session identity must match the owner or spender filter to query allowances"))
}

// allowanceRowToInfo reconstructs the owner-signed payload plus live metering state from a stored row.
func allowanceRowToInfo(row *ent.TokenAllowance) (*tokenpb.TokenAllowanceInfo, error) {
	protoNetwork, err := row.Network.ToProtoNetwork()
	if err != nil {
		return nil, errors.InternalUnhandledError(fmt.Errorf("failed to convert allowance network: %w", err))
	}

	payload := &tokenpb.TokenAllowancePayload{
		Version:                 uint32(row.Version),
		AllowanceId:             append([]byte(nil), row.AllowanceID[:]...),
		OwnerPublicKey:          row.OwnerPublicKey.Serialize(),
		SpenderPublicKey:        row.SpenderPublicKey.Serialize(),
		TokenIdentifier:         row.TokenIdentifier,
		PerTransactionCap:       row.PerTransactionCap,
		TotalLimit:              row.TotalLimit,
		PerTransactionUnlimited: row.PerTransactionUnlimited,
		TotalUnlimited:          row.TotalUnlimited,
		RecipientAllowlist:      row.RecipientAllowlist,
		ExpiryTime:              timestamppb.New(row.ExpiryTime),
		Network:                 protoNetwork,
		OwnerProvidedTimestamp:  row.OwnerProvidedTimestamp,
	}

	info := &tokenpb.TokenAllowanceInfo{
		AllowancePayload: payload,
		SpentAmount:      row.SpentAmount,
		Status:           allowanceStatusToProto(row.Status),
		OwnerSignature:   row.OwnerSignature,
	}
	// Serve the revoke proof for tombstoned rows so clients can verify the
	// owner authorized the revocation, mirroring the create proof above.
	if row.Status == schematype.TokenAllowanceStatusRevoked {
		info.RevokeSignature = row.RevokeSignature
		info.OwnerProvidedRevokeTimestamp = row.OwnerProvidedRevokeTimestamp
		info.RevokeVersion = uint32(row.RevokeVersion)
	}
	return info, nil
}

func allowanceStatusToProto(status schematype.TokenAllowanceStatus) tokenpb.TokenAllowanceStatus {
	switch status {
	case schematype.TokenAllowanceStatusActive:
		return tokenpb.TokenAllowanceStatus_TOKEN_ALLOWANCE_STATUS_ACTIVE
	case schematype.TokenAllowanceStatusRevoked:
		return tokenpb.TokenAllowanceStatus_TOKEN_ALLOWANCE_STATUS_REVOKED
	case schematype.TokenAllowanceStatusExhausted:
		return tokenpb.TokenAllowanceStatus_TOKEN_ALLOWANCE_STATUS_EXHAUSTED
	case schematype.TokenAllowanceStatusExpired:
		return tokenpb.TokenAllowanceStatus_TOKEN_ALLOWANCE_STATUS_EXPIRED
	default:
		return tokenpb.TokenAllowanceStatus_TOKEN_ALLOWANCE_STATUS_UNSPECIFIED
	}
}
