package tokens

import (
	"bytes"
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenallowance"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// CreateTokenAllowanceFlowHandler writes an allowance during participant
// Prepare so every SO validates the owner-signed statement before the
// coordinator can commit.
// Revocation intentionally remains on durable gossip: creation may safely fail
// closed when an SO is unavailable, while revocation must not be blocked by an
// unavailable SO and must converge after it recovers.
type CreateTokenAllowanceFlowHandler struct {
	config *so.Config
}

func NewCreateTokenAllowanceFlowHandler(config *so.Config) *CreateTokenAllowanceFlowHandler {
	return &CreateTokenAllowanceFlowHandler{config: config}
}

var (
	_ consensus.FlowHandler             = (*CreateTokenAllowanceFlowHandler)(nil)
	_ consensus.PrepareBoundFlowHandler = (*CreateTokenAllowanceFlowHandler)(nil)
)

func (h *CreateTokenAllowanceFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	if !allowancesEnabled(ctx) {
		return nil, sparkerrors.UnimplementedMethodDisabled(fmt.Errorf("token allowances are not enabled"))
	}
	req, ok := op.(*pbinternal.CreateTokenAllowancePrepareRequest)
	if !ok {
		return nil, fmt.Errorf("expected CreateTokenAllowancePrepareRequest, got %T", op)
	}
	if req.GetOriginalRequest() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("original request is required"))
	}
	if _, ok := consensus.FlowExecutionIDFromContext(ctx); !ok {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("token allowance creation requires a consensus flow execution id"))
	}
	ownerPublicKey, err := keys.ParsePublicKey(req.GetOriginalRequest().GetAllowancePayload().GetOwnerPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse owner public key: %w", err))
	}
	if err := authz.EnforceWalletNotKillSwitched(ctx, ownerPublicKey); err != nil {
		return nil, err
	}

	if err := ValidateAndApplyCreateAllowance(
		ctx,
		h.config,
		req.GetOriginalRequest().GetAllowancePayload(),
		req.GetOriginalRequest().GetOwnerSignature(),
	); err != nil {
		return nil, err
	}
	return nil, nil
}

func (h *CreateTokenAllowanceFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	allowanceIDBytes, err := createTokenAllowanceIDFromOp(op)
	if err != nil {
		return err
	}
	allowanceID, err := uuid.FromBytes(allowanceIDBytes)
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid allowance_id in commit: %w", err))
	}
	flowExecutionID, ok := consensus.FlowExecutionIDFromContext(ctx)
	if !ok {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("token allowance commit requires a consensus flow execution id"))
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	// Revocation or exhaustion may win the race with the decision. Those terminal
	// rows are durable tombstones, so commit must publish them along with active grants.
	if _, err := db.TokenAllowance.Update().
		Where(
			tokenallowance.AllowanceIDEQ(allowanceID),
			tokenallowance.FlowExecutionIDEQ(flowExecutionID),
		).
		ClearFlowExecutionID().
		Save(ctx); err != nil {
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to commit token allowance %s: %w", allowanceID, err))
	}
	return nil
}

func (h *CreateTokenAllowanceFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	allowanceIDBytes, err := createTokenAllowanceIDFromOp(op)
	if err != nil {
		return err
	}
	allowanceID, err := uuid.FromBytes(allowanceIDBytes)
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid allowance_id in rollback: %w", err))
	}
	flowExecutionID, ok := consensus.FlowExecutionIDFromContext(ctx)
	if !ok {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("token allowance rollback requires a consensus flow execution id"))
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	if _, err := db.TokenAllowance.Delete().
		Where(
			tokenallowance.AllowanceIDEQ(allowanceID),
			tokenallowance.FlowExecutionIDEQ(flowExecutionID),
			tokenallowance.StatusEQ(st.TokenAllowanceStatusActive),
		).
		Exec(ctx); err != nil {
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to roll back token allowance %s: %w", allowanceID, err))
	}
	// A terminal row prevents the signed grant from being replayed, so rollback
	// preserves it but must release the flow stamp that hides it from queries.
	if _, err := db.TokenAllowance.Update().
		Where(
			tokenallowance.AllowanceIDEQ(allowanceID),
			tokenallowance.FlowExecutionIDEQ(flowExecutionID),
			tokenallowance.StatusNEQ(st.TokenAllowanceStatusActive),
		).
		ClearFlowExecutionID().
		Save(ctx); err != nil {
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to publish rolled-back token allowance tombstone %s: %w", allowanceID, err))
	}
	return nil
}

func (h *CreateTokenAllowanceFlowHandler) ValidateDecisionAgainstPrepare(prepareOp, decisionOp proto.Message) error {
	prepareID, err := createTokenAllowanceIDFromOp(prepareOp)
	if err != nil {
		return fmt.Errorf("invalid create-token-allowance prepare payload: %w", err)
	}
	decisionID, err := createTokenAllowanceIDFromOp(decisionOp)
	if err != nil {
		return fmt.Errorf("invalid create-token-allowance decision payload: %w", err)
	}
	if !bytes.Equal(prepareID, decisionID) {
		return fmt.Errorf("decision allowance_id %x does not match prepared allowance_id %x", decisionID, prepareID)
	}
	return nil
}

func createTokenAllowanceIDFromOp(op proto.Message) ([]byte, error) {
	var allowanceID []byte
	switch typed := op.(type) {
	case *pbinternal.CreateTokenAllowancePrepareRequest:
		allowanceID = typed.GetOriginalRequest().GetAllowancePayload().GetAllowanceId()
	case *pbinternal.CreateTokenAllowanceCommitRequest:
		allowanceID = typed.GetAllowanceId()
	case *pbinternal.CreateTokenAllowanceRollbackRequest:
		allowanceID = typed.GetAllowanceId()
	default:
		return nil, fmt.Errorf("unexpected payload type %T", op)
	}
	if len(allowanceID) != 16 {
		return nil, fmt.Errorf("allowance_id must be 16 bytes, got %d", len(allowanceID))
	}
	return allowanceID, nil
}

var _ consensus.CoordinatorFlow = (*createTokenAllowanceCoordinatorFlow)(nil)

type createTokenAllowanceCoordinatorFlow struct {
	*CreateTokenAllowanceFlowHandler
	req             *tokenpb.CreateTokenAllowanceRequest
	ownerPublicKey  keys.Public
	flowExecutionID uuid.UUID
}

func newCreateTokenAllowanceCoordinatorFlow(config *so.Config, req *tokenpb.CreateTokenAllowanceRequest, ownerPublicKey keys.Public) *createTokenAllowanceCoordinatorFlow {
	return &createTokenAllowanceCoordinatorFlow{
		CreateTokenAllowanceFlowHandler: NewCreateTokenAllowanceFlowHandler(config),
		req:                             req,
		ownerPublicKey:                  ownerPublicKey,
	}
}

func (f *createTokenAllowanceCoordinatorFlow) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	if _, ok := op.(*pbinternal.CreateTokenAllowancePrepareRequest); !ok {
		return nil, fmt.Errorf("expected CreateTokenAllowancePrepareRequest, got %T", op)
	}
	flowExecutionID, ok := consensus.FlowExecutionIDFromContext(ctx)
	if !ok {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("token allowance creation requires a consensus flow execution id"))
	}
	// Local validation and insertion run after remote Prepare completes so their
	// quota and token-row locks never span the network fan-out.
	f.flowExecutionID = flowExecutionID
	return nil, nil
}

func (f *createTokenAllowanceCoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.CreateTokenAllowancePrepareRequest{OriginalRequest: f.req}
}

func (f *createTokenAllowanceCoordinatorFlow) BuildCommitPayload(ctx context.Context, _ map[string]*anypb.Any) (proto.Message, error) {
	ctx = consensus.ContextWithFlowExecutionID(ctx, f.flowExecutionID)
	if err := EnforceAllowanceCreateQuota(ctx, f.ownerPublicKey, f.req.GetAllowancePayload()); err != nil {
		return nil, err
	}
	if _, err := f.CreateTokenAllowanceFlowHandler.Prepare(ctx, f.PrepareOp()); err != nil {
		return nil, err
	}
	commit := &pbinternal.CreateTokenAllowanceCommitRequest{AllowanceId: f.req.GetAllowancePayload().GetAllowanceId()}
	if err := f.CreateTokenAllowanceFlowHandler.Commit(ctx, commit); err != nil {
		return nil, err
	}
	return commit, nil
}

func (f *createTokenAllowanceCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.CreateTokenAllowanceRollbackRequest{AllowanceId: f.req.GetAllowancePayload().GetAllowanceId()}
}
