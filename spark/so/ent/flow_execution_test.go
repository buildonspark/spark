package ent_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/require"
)

func TestFlowExecutionHook_CoordinatorIndexIsRequired(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	client := dbCtx.Client

	// Omitting SetCoordinatorIndex on a required field: Ent rejects the
	// Create outright, so we never need a hook for this invariant.
	_, err := client.FlowExecution.Create().
		SetRole(st.FlowExecutionRoleCoordinator).
		SetOpType(1).
		Save(ctx)
	require.ErrorContains(t, err, "coordinator_index")
}

func TestFlowExecutionHook_ParticipantMustNotSetDecisionPayload(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	client := dbCtx.Client

	_, err := client.FlowExecution.Create().
		SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(1).
		SetCoordinatorIndex(2).
		SetDecisionPayload([]byte{0x01}).
		Save(ctx)
	require.ErrorContains(t, err, "PARTICIPANT row must not set decision_payload")
}

// Models the production flow: coordinator generates a row id (recording its
// own index as coordinator_index), and then each participant — on its own
// DB — creates a row with that same id and the coordinator's index. These
// two Creates land in different databases in production; this test exercises
// them against two test DBs to confirm the id can be reused across roles.
func TestFlowExecutionHook_CoordinatorIDCanBeReusedAsParticipantID(t *testing.T) {
	ctx, coordDBCtx := db.ConnectToTestPostgres(t)
	coordClient := coordDBCtx.Client

	const coordinatorSelfIndex = 0

	coord, err := coordClient.FlowExecution.Create().
		SetRole(st.FlowExecutionRoleCoordinator).
		SetOpType(1).
		SetCoordinatorIndex(coordinatorSelfIndex).
		Save(ctx)
	require.NoError(t, err)
	require.Equal(t, st.FlowExecutionStatusInFlight, coord.Status)
	require.Equal(t, uint(coordinatorSelfIndex), coord.CoordinatorIndex)

	// Second operator's DB.
	_, participantDBCtx := db.ConnectToTestPostgres(t)
	participantClient := participantDBCtx.Client

	participant, err := participantClient.FlowExecution.Create().
		SetID(coord.ID).
		SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(1).
		SetCoordinatorIndex(coordinatorSelfIndex).
		Save(ctx)
	require.NoError(t, err)
	require.Equal(t, coord.ID, participant.ID)
	require.Equal(t, uint(coordinatorSelfIndex), participant.CoordinatorIndex)

	require.NotEqual(t, uuid.Nil, participant.ID)
}
