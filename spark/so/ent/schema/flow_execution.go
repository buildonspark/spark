package schema

import (
	"context"
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	gen "github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/hook"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

// FlowExecution captures one row per role (coordinator or participant) per
// two-phase-commit execution driven by the consensus engine. The engine writes
// these rows so unfinished flows can be detected (observability) and
// auto-reconciled by a background task (recovery).
//
// The row's id (provided by BaseMixin) is the execution identifier. Because
// each Signing Operator maintains an independent database, the coordinator
// generates the id for its row and then sends that id in the ConsensusPrepare
// RPC; every participant creates its own row with the same id on its own DB.
// Cross-role lookups within a single SO's DB are not meaningful — each DB
// holds exactly one row per execution (either the COORDINATOR row or the
// PARTICIPANT row, never both).
type FlowExecution struct {
	ent.Schema
}

func (FlowExecution) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (FlowExecution) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("role").GoType(st.FlowExecutionRole("")).
			Immutable().
			Comment("COORDINATOR or PARTICIPANT. Determines the semantics of the decision_payload column.").
			Annotations(entexample.Default(st.FlowExecutionRoleCoordinator)),
		field.Int32("op_type").
			Immutable().
			Comment("Mirrors gossip.ConsensusOperationType; identifies the flow this execution belongs to.").
			Annotations(entexample.Default(int32(1))),
		field.Enum("status").GoType(st.FlowExecutionStatus("")).
			Default(string(st.FlowExecutionStatusInFlight)).
			Comment("IN_FLIGHT until the role reaches a terminal decision (COMMITTED or ROLLED_BACK)."),
		field.Uint("coordinator_index").
			Immutable().
			Comment("Index of the coordinator operator (matches SigningOperator.ID). Required; on coordinator rows this equals the coordinator's own self-index.").
			Annotations(entexample.Default(0)),
		field.Bytes("decision_payload").
			Optional().
			Nillable().
			Comment("Marshalled google.protobuf.Any carrying the commit or rollback payload. Populated on coordinator rows once a decision is reached so ConsensusQueryOutcome can serve the payload."),
	}
}

func (FlowExecution) Edges() []ent.Edge {
	return []ent.Edge{}
}

func (FlowExecution) Indexes() []ent.Index {
	return []ent.Index{
		// Drives the participant reconciliation sweep and the coordinator self-sweep.
		index.Fields("role", "status", "update_time"),
	}
}

// Hooks enforces role-conditional invariants on decision_payload at row
// creation so the reconciliation task can rely on the shape of each row:
//
//	PARTICIPANT rows must not set decision_payload.
//
// coordinator_index is a required field handled by Ent itself (no default);
// absent SetCoordinatorIndex at Create the save fails. decision_payload is
// legitimately set by an Update (on the coordinator's terminal transition);
// engine code is the only caller that sets it, and ensures it runs only on
// COORDINATOR rows.
func (FlowExecution) Hooks() []ent.Hook {
	return []ent.Hook{
		func(next ent.Mutator) ent.Mutator {
			return hook.FlowExecutionFunc(func(ctx context.Context, m *gen.FlowExecutionMutation) (ent.Value, error) {
				if !m.Op().Is(ent.OpCreate) {
					return next.Mutate(ctx, m)
				}
				role, ok := m.Role()
				if !ok {
					return nil, fmt.Errorf("flow_execution: role is required")
				}
				_, hasDecision := m.DecisionPayload()
				if role == st.FlowExecutionRoleParticipant && hasDecision {
					return nil, fmt.Errorf("flow_execution: PARTICIPANT row must not set decision_payload")
				}
				return next.Mutate(ctx, m)
			})
		},
	}
}
