package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ConsensusQueryHandler serves ConsensusQueryOutcome RPCs. The flow-execution
// reconciliation task calls this to ask the coordinator for a stuck
// FlowExecution's final outcome so it can apply the correct commit or
// rollback on the participant's side.
type ConsensusQueryHandler struct {
	config *so.Config
}

// NewConsensusQueryHandler creates a new ConsensusQueryHandler.
func NewConsensusQueryHandler(config *so.Config) *ConsensusQueryHandler {
	return &ConsensusQueryHandler{config: config}
}

// QueryOutcome looks up the coordinator's FlowExecution row by id and
// returns its outcome. A missing row or a row that isn't COORDINATOR is
// reported as OUTCOME_UNSPECIFIED — callers treat that as "no record,"
// which (under normal operation) only happens if coordinator data was lost.
func (h *ConsensusQueryHandler) QueryOutcome(ctx context.Context, req *pbinternal.ConsensusQueryOutcomeRequest) (*pbinternal.ConsensusQueryOutcomeResponse, error) {
	id, err := uuid.Parse(req.GetFlowExecutionId())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid flow_execution_id: %w", err))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	row, err := db.FlowExecution.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return &pbinternal.ConsensusQueryOutcomeResponse{
				Outcome: pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_UNSPECIFIED,
			}, nil
		}
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to query flow_execution %s: %w", id, err))
	}

	// Participants keep their own row under the same id in their own DB; a
	// caller that somehow reaches this handler for a PARTICIPANT row is
	// querying the wrong operator. Don't leak participant state — report
	// UNSPECIFIED so the caller treats it as "no authoritative record."
	if row.Role != st.FlowExecutionRoleCoordinator {
		return &pbinternal.ConsensusQueryOutcomeResponse{
			Outcome: pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_UNSPECIFIED,
		}, nil
	}

	resp := &pbinternal.ConsensusQueryOutcomeResponse{
		OpType:  row.OpType,
		Outcome: outcomeFromStatus(row.Status),
	}

	if row.DecisionPayload != nil && len(*row.DecisionPayload) > 0 {
		anyMsg := &anypb.Any{}
		if err := proto.Unmarshal(*row.DecisionPayload, anyMsg); err != nil {
			return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to unmarshal decision_payload for %s: %w", id, err))
		}
		resp.DecisionPayload = anyMsg
	}
	return resp, nil
}

// outcomeFromStatus maps a FlowExecution row status to the proto outcome enum.
// An unrecognized status falls through to OUTCOME_UNSPECIFIED.
func outcomeFromStatus(status st.FlowExecutionStatus) pbinternal.ConsensusQueryOutcomeResponse_Outcome {
	switch status {
	case st.FlowExecutionStatusInFlight:
		return pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_IN_FLIGHT
	case st.FlowExecutionStatusCommitted:
		return pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_COMMITTED
	case st.FlowExecutionStatusRolledBack:
		return pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_ROLLED_BACK
	}
	return pbinternal.ConsensusQueryOutcomeResponse_OUTCOME_UNSPECIFIED
}
