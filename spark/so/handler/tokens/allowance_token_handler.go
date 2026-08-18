package tokens

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/authz"
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
}

func NewAllowanceTokenHandler(config *so.Config) *AllowanceTokenHandler {
	return &AllowanceTokenHandler{
		config: config,
	}
}

// allowancesEnabled reports whether the token allowance lifecycle RPCs are turned on for this SO.
func allowancesEnabled(ctx context.Context) bool {
	knobService := knobs.GetKnobsService(ctx)
	return knobService != nil && knobService.RolloutRandom(knobs.KnobTokenAllowancesEnabled, 0)
}

// CreateTokenAllowance installs an owner-signed spending allowance. It acts as coordinator:
// validating and applying the grant locally, committing before any network call, then fanning the
// grant out to every other operator. Each operator re-validates independently.
func (h *AllowanceTokenHandler) CreateTokenAllowance(ctx context.Context, req *tokenpb.CreateTokenAllowanceRequest) (*tokenpb.CreateTokenAllowanceResponse, error) {
	if !allowancesEnabled(ctx) {
		return nil, errors.UnimplementedMethodDisabled(fmt.Errorf("token allowances are not enabled"))
	}

	payload := req.GetAllowancePayload()
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

	// Like freshness, the per-owner quota is a public-edge admission control:
	// replication must converge operators on an admitted grant, never re-vote it.
	if err := EnforceAllowanceCreateQuota(ctx, ownerPublicKey, payload); err != nil {
		return nil, err
	}

	if err := ValidateAndApplyCreateAllowance(ctx, h.config, payload, req.GetOwnerSignature()); err != nil {
		return nil, err
	}

	// Commit locally before fanning out so we don't hold row locks during network calls
	// (durable-before-fanout, matching coordinated freeze).
	if err := ent.DbCommit(ctx); err != nil {
		return nil, errors.InternalDatabaseWriteError(fmt.Errorf("failed to commit token allowance transaction: %w", err))
	}

	progress := h.fanOutCreateAllowanceToOtherOperators(ctx, req)
	progress.AppliedOperatorPublicKeys = append(
		progress.AppliedOperatorPublicKeys,
		h.config.IdentityPublicKey().Serialize(),
	)

	return &tokenpb.CreateTokenAllowanceResponse{AllowanceProgress: progress}, nil
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

	query := db.TokenAllowance.Query()
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
		Version:                uint32(row.Version),
		AllowanceId:            append([]byte(nil), row.AllowanceID[:]...),
		OwnerPublicKey:         row.OwnerPublicKey.Serialize(),
		SpenderPublicKey:       row.SpenderPublicKey.Serialize(),
		TokenIdentifier:        row.TokenIdentifier,
		PerTransactionCap:      row.PerTransactionCap,
		TotalLimit:             row.TotalLimit,
		RecipientAllowlist:     row.RecipientAllowlist,
		ExpiryTime:             timestamppb.New(row.ExpiryTime),
		Network:                protoNetwork,
		OwnerProvidedTimestamp: row.OwnerProvidedTimestamp,
	}

	return &tokenpb.TokenAllowanceInfo{
		AllowancePayload: payload,
		SpentAmount:      row.SpentAmount,
		Status:           allowanceStatusToProto(row.Status),
		OwnerSignature:   row.OwnerSignature,
	}, nil
}

func allowanceStatusToProto(status schematype.TokenAllowanceStatus) tokenpb.TokenAllowanceStatus {
	switch status {
	case schematype.TokenAllowanceStatusActive:
		return tokenpb.TokenAllowanceStatus_TOKEN_ALLOWANCE_STATUS_ACTIVE
	case schematype.TokenAllowanceStatusRevoked:
		return tokenpb.TokenAllowanceStatus_TOKEN_ALLOWANCE_STATUS_REVOKED
	case schematype.TokenAllowanceStatusExhausted:
		return tokenpb.TokenAllowanceStatus_TOKEN_ALLOWANCE_STATUS_EXHAUSTED
	default:
		return tokenpb.TokenAllowanceStatus_TOKEN_ALLOWANCE_STATUS_UNSPECIFIED
	}
}

// fanOutCreateAllowanceToOtherOperators replicates the grant to every other operator and reports
// which ones applied it. Fan-out failures are logged, not fatal: progress reflects partial success.
func (h *AllowanceTokenHandler) fanOutCreateAllowanceToOtherOperators(ctx context.Context, req *tokenpb.CreateTokenAllowanceRequest) *tokenpb.AllowanceProgress {
	logger := logging.GetLoggerFromContext(ctx)

	excludeSelf := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	results, err := helper.ExecuteTaskWithAllOperatorsWithAllResponses(ctx, h.config, &excludeSelf,
		func(ctx context.Context, operator *so.SigningOperator) (*tokeninternalpb.InternalCreateTokenAllowanceResponse, error) {
			conn, err := operator.NewOperatorInternalGRPCConnection(ctx)
			if err != nil {
				return nil, err
			}
			defer conn.Close()

			client := tokeninternalpb.NewSparkTokenInternalServiceClient(conn)
			return client.InternalCreateTokenAllowance(ctx, &tokeninternalpb.InternalCreateTokenAllowanceRequest{
				AllowancePayload: req.GetAllowancePayload(),
				OwnerSignature:   req.GetOwnerSignature(),
			})
		},
	)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Warnf("token allowance create fan-out setup failed for allowance %x", req.GetAllowancePayload().GetAllowanceId())
		results = emptyCreateResults()
	} else if results.HasErrors() {
		for opID, opErr := range results.Errors {
			logger.With(zap.Error(opErr)).Sugar().Warnf("token allowance create failed for operator %s (allowance %x)", opID, req.GetAllowancePayload().GetAllowanceId())
		}
	}

	return buildAllowanceProgress(h.config, results)
}

func emptyCreateResults() *helper.PartialResults[*tokeninternalpb.InternalCreateTokenAllowanceResponse] {
	return &helper.PartialResults[*tokeninternalpb.InternalCreateTokenAllowanceResponse]{
		Successes: make(map[string]*tokeninternalpb.InternalCreateTokenAllowanceResponse),
		Errors:    make(map[string]error),
	}
}

// buildAllowanceProgress maps the set of operators that applied the operation to their identity
// public keys. Self is appended by the caller.
func buildAllowanceProgress[V any](config *so.Config, results *helper.PartialResults[V]) *tokenpb.AllowanceProgress {
	var applied [][]byte
	for identifier, operator := range config.SigningOperatorMap {
		if identifier == config.Identifier {
			continue
		}
		if _, ok := results.Successes[identifier]; ok {
			applied = append(applied, operator.IdentityPublicKey.Serialize())
		}
	}
	return &tokenpb.AllowanceProgress{AppliedOperatorPublicKeys: applied}
}
