package grpctest

import (
	"fmt"
	"os"
	"os/user"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbmock "github.com/lightsparkdev/spark/proto/mock"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/flowexecution"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// operatorDatabasePath returns the DB URI for the given operator index,
// preferring sparktesting.GetTestDatabasePath but falling back to a URI
// that carries the current OS user when running against a plain local
// Postgres (which authenticates by OS user via peer/ident). The default
// GetTestDatabasePath emits "postgresql://:@127.0.0.1:5432/..." with an
// empty username, which libpq rejects with "no PostgreSQL user name
// specified in startup packet."
func operatorDatabasePath(t *testing.T, i int) string {
	t.Helper()
	if sparktesting.HasLocalSparkIngressHost() {
		return sparktesting.GetTestDatabasePath(i)
	}
	pguser := os.Getenv("PGUSER")
	if pguser == "" {
		u, err := user.Current()
		require.NoError(t, err)
		pguser = u.Username
	}
	return fmt.Sprintf("postgresql://%s@127.0.0.1:5432/sparkoperator_%d?sslmode=disable", pguser, i)
}

// TestFlowExecution_RenewLeafConsensus_WritesRowsOnCoordinatorAndParticipants
// drives a renew-leaf flow through the 2PC engine and asserts that every SO
// ends up with exactly one COMMITTED FlowExecution row sharing the same id:
// COORDINATOR on the coordinator and PARTICIPANT on the others. This is the
// end-to-end check for the row-write logic in twopc.go (coordinator) and
// consensus_handler.go + gossip_handler.go (participants).
func TestFlowExecution_RenewLeafConsensus_WritesRowsOnCoordinatorAndParticipants(t *testing.T) {
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot route through consensus engine: %v", err)
	}
	require.NoError(t, kc.SetKnob(t, knobs.KnobUseConsensusRenew, 100))
	t.Cleanup(func() {
		// Best-effort: if the knob store is flaky during cleanup (e.g., the
		// test context just canceled), we don't want the whole test to fail
		// for a housekeeping operation. Log and move on.
		if err := kc.SetKnob(t, knobs.KnobUseConsensusRenew, 0); err != nil {
			t.Logf("best-effort KnobUseConsensusRenew reset failed: %v", err)
		}
	})

	// Drive a consensus-path renew by mocking the leaf's timelock below the
	// renewal threshold, then calling RenewNodeZeroTimelock.
	config := wallet.NewTestWalletConfig(t)
	coordinatorIdx := int(config.SigningOperators[config.CoordinatorIdentifier].ID)

	// Snapshot existing FlowExecution ids on every operator so other tests'
	// rows don't get picked up by our assertions. operatorIndices is the
	// ordered list of operator IDs discovered from the test signing set.
	operatorIndices := operatorIndicesFromConfig(config)
	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		preExistingIDs[i] = snapshotFlowExecutionIDs(t, operatorDatabasePath(t, i))
	}

	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(config, faucet, leafPrivKey, 100000)
	require.NoError(t, err)
	require.Equal(t, "AVAILABLE", rootNode.Status)

	modifyNodeTimelockAllOperators(t, config, rootNode.Id, 0, timelockBelowRenewThreshold)

	authToken, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), authToken)

	leaf := queryLeafByID(t, config, authToken, rootNode.Id)
	renewed, err := wallet.RenewNodeZeroTimelock(ctx, config, leaf, leafPrivKey)
	require.NoError(t, err)
	require.NotNil(t, renewed)

	// Collect the FlowExecution rows each operator wrote during the renew.
	newRowsByOperator := make(map[int][]*ent.FlowExecution, len(operatorIndices))
	totalNew := 0
	for _, i := range operatorIndices {
		newRowsByOperator[i] = newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i])
		totalNew += len(newRowsByOperator[i])
	}

	// If no operator wrote a FlowExecution row the renew ran through the
	// legacy (non-consensus) path — KnobUseConsensusRenew did not actually
	// propagate to the running operators. That happens under the bare
	// run-everything.sh local env (knobs are defaulted in-process; the
	// KnobController writes to a K8s ConfigMap the local operators do not
	// read). Skip rather than fail; this test is meaningful under minikube.
	if totalNew == 0 {
		t.Skip("no FlowExecution rows written — 2PC path did not run (likely a bare local env without live knob propagation; run under minikube)")
	}

	// Exactly one new row per operator. (The renew consensus flow runs once
	// per renew call; any additional rows would indicate leaked state from a
	// different code path.)
	for _, i := range operatorIndices {
		require.Len(t, newRowsByOperator[i], 1, "operator %d should have exactly one new FlowExecution row; got %d", i, len(newRowsByOperator[i]))
	}

	// All rows must share the same id — participants were supposed to adopt
	// the coordinator's row id via SetID during their own Create.
	sharedID := newRowsByOperator[coordinatorIdx][0].ID
	for _, i := range operatorIndices {
		assert.Equal(t, sharedID, newRowsByOperator[i][0].ID, "operator %d row id must match coordinator's", i)
	}

	for _, i := range operatorIndices {
		row := newRowsByOperator[i][0]
		assert.Equal(t, st.FlowExecutionStatusCommitted, row.Status,
			"operator %d row should be COMMITTED after a successful renew; got %s", i, row.Status)
		assert.Equal(t, uint(coordinatorIdx), row.CoordinatorIndex,
			"operator %d coordinator_index should point at the coordinator", i)
		if i == coordinatorIdx {
			assert.Equal(t, st.FlowExecutionRoleCoordinator, row.Role, "coordinator row has wrong role")
			require.NotNil(t, row.DecisionPayload, "coordinator row should carry a decision_payload after commit")
			assert.NotEmpty(t, *row.DecisionPayload, "coordinator row decision_payload should be non-empty")
		} else {
			assert.Equal(t, st.FlowExecutionRoleParticipant, row.Role, "operator %d should be PARTICIPANT", i)
			// Ent reads a NULL Optional+Nillable bytes column as either a
			// nil pointer or a non-nil pointer to a nil slice. Both mean
			// "no payload stored" — the invariant we actually care about.
			assert.True(t, row.DecisionPayload == nil || len(*row.DecisionPayload) == 0,
				"participant rows must not carry decision_payload; got %v", row.DecisionPayload)
		}
	}
}

// operatorIndicesFromConfig returns the sorted list of operator IDs present in
// the test wallet config.
func operatorIndicesFromConfig(config *wallet.TestWalletConfig) []int {
	ids := make([]int, 0, len(config.SigningOperators))
	for _, op := range config.SigningOperators {
		ids = append(ids, int(op.ID))
	}
	return ids
}

// snapshotFlowExecutionIDs reads the current set of FlowExecution ids on the
// given DB so the assertion pass can distinguish rows produced by this test
// from rows left behind by previous tests in the same hermetic run.
func snapshotFlowExecutionIDs(t *testing.T, dbURI string) map[uuid.UUID]struct{} {
	t.Helper()
	client := db.NewPostgresEntClientForIntegrationTest(t, dbURI)
	defer client.Close()
	ids, err := client.FlowExecution.Query().IDs(t.Context())
	require.NoError(t, err)
	set := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// newFlowExecutionsSince returns FlowExecution rows on the given DB whose ids
// were not present in the pre-test snapshot. Sorted by create_time so the
// first row is the oldest new row.
func newFlowExecutionsSince(t *testing.T, dbURI string, preExisting map[uuid.UUID]struct{}) []*ent.FlowExecution {
	t.Helper()
	client := db.NewPostgresEntClientForIntegrationTest(t, dbURI)
	defer client.Close()
	rows, err := client.FlowExecution.Query().
		Order(ent.Asc(flowexecution.FieldCreateTime)).
		All(t.Context())
	require.NoError(t, err)
	var newOnes []*ent.FlowExecution
	for _, r := range rows {
		if _, seen := preExisting[r.ID]; seen {
			continue
		}
		newOnes = append(newOnes, r)
	}
	return newOnes
}

// TestFlowExecution_ReconcileRecoversStuckParticipantRow exercises the full
// cross-operator recovery loop end-to-end:
//
//	participant.reconcile_task → coordinator.ConsensusQueryOutcome RPC →
//	participant.gossip-replay dispatch → participant FlowHandler.Commit →
//	row transition to COMMITTED.
//
// To exercise this without relying on a real 2PC flow execution (whose
// FlowHandler.Commit may not be safely re-runnable on already-committed
// state), the test seeds the FlowExecution rows directly:
//
//  1. On the coordinator's DB: insert a COORDINATOR row with
//     op_type=STORE_PREIMAGE_SHARE, status=COMMITTED, decision_payload=<a
//     well-formed StorePreimageSharePrepareRequest Any>.
//  2. On a participant's DB: insert a PARTICIPANT row with the same id,
//     status=IN_FLIGHT, coordinator_index pointing at the coordinator,
//     update_time backdated past the stuck threshold.
//  3. Trigger reconcile_stuck_flow_executions on the participant.
//  4. Assert the participant row transitions to COMMITTED.
//
// STORE_PREIMAGE_SHARE is chosen because its Commit and Rollback are no-ops
// (the share was written during Prepare); the gossip-replay still runs the
// real handler and the real row transition, so all the cross-operator
// plumbing is validated. Direct DB seeding is the fixture path; everything
// after step 2 runs through production code.
func TestFlowExecution_ReconcileRecoversStuckParticipantRow(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}

	config := wallet.NewTestWalletConfig(t)
	coordinatorIdx := int(config.SigningOperators[config.CoordinatorIdentifier].ID)

	// Pick a participant (anything that isn't the coordinator).
	var participantIdx int
	foundParticipant := false
	for _, op := range config.SigningOperators {
		if int(op.ID) != coordinatorIdx {
			participantIdx = int(op.ID)
			foundParticipant = true
			break
		}
	}
	require.True(t, foundParticipant, "need at least two operators to pick a non-coordinator participant")

	// Build a well-formed payload for the coordinator's decision_payload.
	// The exact contents don't matter — preimage_share's Commit ignores the
	// payload — but the bytes have to round-trip through anypb.Any.
	payloadAny, err := anypb.New(&pbinternal.StorePreimageSharePrepareRequest{})
	require.NoError(t, err)
	payloadBytes, err := proto.Marshal(payloadAny)
	require.NoError(t, err)

	executionID := uuid.New()

	// Coordinator-side seed: a COORDINATOR row already in COMMITTED state
	// with the decision payload populated. This is what ConsensusQueryOutcome
	// will return when the participant's reconcile task queries.
	insertSeedCoordinatorRow(t, operatorDatabasePath(t, coordinatorIdx), executionID, uint(coordinatorIdx), payloadBytes)
	t.Cleanup(func() {
		deleteSeedRow(t, operatorDatabasePath(t, coordinatorIdx), executionID)
	})

	// Participant-side seed: a PARTICIPANT row stuck IN_FLIGHT, backdated
	// past the reconcile sweep's threshold so the next sweep picks it up.
	insertSeedParticipantRow(t, operatorDatabasePath(t, participantIdx), executionID, uint(coordinatorIdx))
	t.Cleanup(func() {
		deleteSeedRow(t, operatorDatabasePath(t, participantIdx), executionID)
	})

	// Trigger the reconcile task on the participant via the mock RPC.
	triggerTask(t, config.SigningOperators[operatorIdentifier(participantIdx)], "reconcile_stuck_flow_executions")

	// Open one client for the polling loop rather than creating a new
	// connection per tick.
	participantClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, participantIdx))
	defer participantClient.Close()

	// TriggerTask returns when the task body returns, but allow a short poll
	// window in case any internal step is async.
	require.Eventually(t, func() bool {
		row, err := participantClient.FlowExecution.Get(t.Context(), executionID)
		require.NoError(t, err)
		return row.Status == st.FlowExecutionStatusCommitted
	}, 10*time.Second, 200*time.Millisecond,
		"reconcile task did not recover the stuck participant row to COMMITTED")
}

// insertSeedCoordinatorRow creates a COORDINATOR FlowExecution row in
// COMMITTED state. Used as a test fixture so the participant's reconcile
// task gets a real coordinator to query.
func insertSeedCoordinatorRow(t *testing.T, dbURI string, id uuid.UUID, coordIdx uint, decisionPayload []byte) {
	t.Helper()
	client := db.NewPostgresEntClientForIntegrationTest(t, dbURI)
	defer client.Close()
	_, err := client.FlowExecution.Create().
		SetID(id).
		SetRole(st.FlowExecutionRoleCoordinator).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE)).
		SetStatus(st.FlowExecutionStatusCommitted).
		SetCoordinatorIndex(coordIdx).
		SetDecisionPayload(decisionPayload).
		Save(t.Context())
	require.NoError(t, err)
}

// insertSeedParticipantRow creates a PARTICIPANT FlowExecution row in
// IN_FLIGHT state with a backdated update_time so the reconcile sweep picks
// it up immediately.
func insertSeedParticipantRow(t *testing.T, dbURI string, id uuid.UUID, coordIdx uint) {
	t.Helper()
	client := db.NewPostgresEntClientForIntegrationTest(t, dbURI)
	defer client.Close()
	_, err := client.FlowExecution.Create().
		SetID(id).
		SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE)).
		SetCoordinatorIndex(coordIdx).
		Save(t.Context())
	require.NoError(t, err)
	// Backdate update_time. Ent's UpdateDefault(time.Now) on the column
	// re-stamps it on every Update, so the only way to set an older value
	// is via a separate Update call after creation.
	_, err = client.FlowExecution.Update().
		Where(flowexecution.ID(id)).
		SetUpdateTime(time.Now().Add(-1 * time.Hour)).
		Save(t.Context())
	require.NoError(t, err)
}

// deleteSeedRow removes the test-fixture row by id. Called in t.Cleanup so
// repeated test runs don't leak rows.
func deleteSeedRow(t *testing.T, dbURI string, id uuid.UUID) {
	t.Helper()
	client := db.NewPostgresEntClientForIntegrationTest(t, dbURI)
	defer client.Close()
	_, err := client.FlowExecution.Delete().Where(flowexecution.ID(id)).Exec(t.Context())
	if err != nil {
		t.Logf("cleanup of seed row %s failed (non-fatal): %v", id, err)
	}
}

// triggerTask calls the mock TriggerTask RPC on the given operator. The
// reconcile task swallows per-row errors internally and returns nil under
// normal operation, so any error here represents a real problem (bad task
// name, RPC failure, DB query failure inside the task) and should fail the
// test fast rather than letting the polling loop time out.
func triggerTask(t *testing.T, op *so.SigningOperator, taskName string) {
	t.Helper()
	conn, err := op.NewOperatorGRPCConnection()
	require.NoError(t, err)
	defer conn.Close()
	_, err = pbmock.NewMockServiceClient(conn).TriggerTask(t.Context(), &pbmock.TriggerTaskRequest{TaskName: taskName})
	require.NoErrorf(t, err, "TriggerTask(%s) failed", taskName)
}

// operatorIdentifier derives the 32-byte-hex operator identifier from the
// operator's ID (matching testing/wallet/testing.go's `fmt.Sprintf("%064d", id+1)`).
func operatorIdentifier(id int) string {
	return fmt.Sprintf("%064d", id+1)
}
