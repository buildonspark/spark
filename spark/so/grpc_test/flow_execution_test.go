package grpctest

import (
	"context"
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
	"github.com/lightsparkdev/spark/so/ent/treenode"
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
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}

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
	for _, i := range operatorIndices {
		newRowsByOperator[i] = newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i])
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

	// Enable active recovery for the duration of the test. The production
	// default is monitor-only (knob = 0), so without this the reconcile
	// task would just log the stuck row and return without transitioning
	// it, and the polling assertion below would time out.
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot enable reconcile recovery: %v", err)
	}
	require.NoError(t, kc.SetKnob(t, knobs.KnobFlowExecutionReconcileEnabled, 1))
	t.Cleanup(func() {
		if err := kc.SetKnob(t, knobs.KnobFlowExecutionReconcileEnabled, 0); err != nil {
			t.Logf("best-effort KnobFlowExecutionReconcileEnabled reset failed: %v", err)
		}
	})

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
	// The task surfaces a stuck-rows alert as a task-level error (so the
	// scheduler's LogMiddleware logs at ERROR and Slack fans the
	// notification out); manual triggers see the same error propagated
	// as codes.Internal carrying the alert message. Recovery has
	// already committed by the time the alert fires, which the polling
	// loop below verifies.
	triggerErr := triggerTask(t, config.SigningOperators[operatorIdentifier(participantIdx)], "reconcile_stuck_flow_executions")
	require.Error(t, triggerErr, "reconcile must surface stuck rows as a task-level error so Slack alerts fire")
	assert.Contains(t, triggerErr.Error(), "stuck PARTICIPANT flow_execution rows",
		"alert error must carry the role-qualified message used by the Slack notification")

	// Open one client for the polling loop rather than creating a new
	// connection per tick.
	participantClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, participantIdx))
	defer participantClient.Close()

	// Recovery committed before the alert error returned (DbCommit ran
	// inside reconcile() before stuckFlowExecutionError), so the row
	// should already be COMMITTED on disk. Poll briefly in case any
	// internal step is async.
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

// triggerTask calls the mock TriggerTask RPC on the given operator and
// returns whatever the RPC returns. Callers decide whether the error is
// expected — the flow_execution sweep tasks deliberately surface
// stuck-rows alerts as task-level errors (which propagate through
// MockServer.TriggerTask as codes.Internal) so that the same error
// triggers Slack notifications via the scheduler's LogMiddleware. RPC-
// transport problems (bad task name, connection failure) are reported
// via require.NoError on the connection setup so callers don't have to
// disambiguate them from alert errors.
func triggerTask(t *testing.T, op *so.SigningOperator, taskName string) error {
	t.Helper()
	conn, err := op.NewOperatorGRPCConnection()
	require.NoError(t, err)
	defer conn.Close()
	_, err = pbmock.NewMockServiceClient(conn).TriggerTask(t.Context(), &pbmock.TriggerTaskRequest{TaskName: taskName})
	return err
}

// operatorIdentifier derives the 32-byte-hex operator identifier from the
// operator's ID (matching testing/wallet/testing.go's `fmt.Sprintf("%064d", id+1)`).
func operatorIdentifier(id int) string {
	return fmt.Sprintf("%064d", id+1)
}

// TestFlowExecution_RenewLeafConsensus_RequestCancellation_RowsTerminal exercises
// the layer-1 engine fix: when a coordinator's gRPC client cancels mid-flow,
// the engine's bookkeeping (coordinator FlowExecution row + commit/rollback
// gossip dispatch) must complete on a detached cleanup context. Pre-fix, the
// coordinator's row was tied to the request transaction and the cleanup paths
// ran on the cancelled ctx, so participants that had already prepared were
// stranded IN_FLIGHT with locked resources.
//
// The test fires a real renew with a tight client-side timeout designed to
// land inside Execute, then asserts that every operator's FlowExecution row
// for that execution reaches a terminal state (COMMITTED or ROLLED_BACK)
// within a few seconds — not minutes via the reconciler. Either terminal
// outcome is acceptable: a fast renew that races past the cancel ends in
// COMMITTED; a cancel that lands mid-fan-out ends in ROLLED_BACK. The
// failure mode this test guards against is "stuck IN_FLIGHT", which is the
// pre-fix behavior.
func TestFlowExecution_RenewLeafConsensus_RequestCancellation_RowsTerminal(t *testing.T) {
	// The multi-operator DB workload of this test is hostile to
	// bare-local Postgres' default max_connections; same minikube gate
	// as the happy-path consensus test.
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}

	config := wallet.NewTestWalletConfig(t)
	operatorIndices := operatorIndicesFromConfig(config)

	// One long-lived ent client per operator. The test runs ~50 DB
	// queries (snapshot + assertion + Eventually polling) and re-opening a
	// client every time saturates Postgres' connection pool when the 5
	// running SOs already hold a large slice of max_connections.
	clients := make(map[int]*ent.Client, len(operatorIndices))
	for _, i := range operatorIndices {
		clients[i] = db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		t.Cleanup(func() { _ = clients[i].Close() })
	}

	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		ids, err := clients[i].FlowExecution.Query().IDs(t.Context())
		require.NoError(t, err)
		set := make(map[uuid.UUID]struct{}, len(ids))
		for _, id := range ids {
			set[id] = struct{}{}
		}
		preExistingIDs[i] = set
	}

	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(config, faucet, leafPrivKey, 100000)
	require.NoError(t, err)
	require.Equal(t, "AVAILABLE", rootNode.Status)

	modifyNodeTimelockAllOperators(t, config, rootNode.Id, 0, timelockBelowRenewThreshold)

	authToken, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	parentCtx := wallet.ContextWithToken(t.Context(), authToken)
	leaf := queryLeafByID(t, config, authToken, rootNode.Id)

	// 75ms is shorter than the typical end-to-end renew latency (p50
	// ~110ms in production) but long enough that createCoordinatorRow
	// has fired. Whether cancel lands during prepare fan-out, between
	// Prepare and BuildCommitPayload, or after commit gossip starts
	// dispatching, the post-fix engine drives the row to a terminal
	// state on cleanupCtx.
	ctx, cancel := context.WithTimeout(parentCtx, 75*time.Millisecond)
	defer cancel()

	// Don't fail the test on the renew error — the cancellation may or
	// may not propagate to the client depending on which phase it landed
	// in. What matters is the post-state of the FlowExecution rows.
	_, _ = wallet.RenewNodeZeroTimelock(ctx, config, leaf, leafPrivKey)

	// newRows fetches all FlowExecution rows on operator i that didn't
	// exist at test start. Closure over the per-operator client so the
	// pool stays bounded.
	//
	// Uses assert.NoError + return nil rather than require.NoError so a
	// DB error inside require.Eventually's goroutine doesn't get
	// swallowed by t.Fatal → runtime.Goexit (which would surface only
	// as "condition never satisfied in time" without the actual cause).
	// assert.NoError marks the test failed via t.Errorf; the empty
	// return lets the caller continue and Eventually's wall-clock
	// timeout will end the loop normally.
	newRows := func(i int) []*ent.FlowExecution {
		rows, err := clients[i].FlowExecution.Query().
			Order(ent.Asc(flowexecution.FieldCreateTime)).
			All(t.Context())
		if !assert.NoError(t, err) {
			return nil
		}
		var newOnes []*ent.FlowExecution
		for _, r := range rows {
			if _, seen := preExistingIDs[i][r.ID]; !seen {
				newOnes = append(newOnes, r)
			}
		}
		return newOnes
	}

	// If the 75ms timeout fires before the coordinator even reaches
	// createCoordinatorRow, no row was ever created — nothing to assert
	// on. Rare under minikube but possible under load.
	totalNew := 0
	for _, i := range operatorIndices {
		totalNew += len(newRows(i))
	}
	if totalNew == 0 {
		t.Skip("no FlowExecution rows written — cancel landed before any row was created")
	}

	// All NEW rows must reach a terminal state within a few seconds —
	// well under any reconciler threshold, so this assertion specifically
	// tests that the engine's cleanupCtx drove the gossip dispatch
	// directly, rather than relying on the participant reconcile sweep.
	// (If the cleanupCtx wiring is broken, participants stay IN_FLIGHT
	// and only the reconciler picks them up — which we explicitly *don't*
	// enable here.)
	require.Eventually(t, func() bool {
		for _, i := range operatorIndices {
			// If a participant's own request ctx was cancelled before
			// it finished writing its row, no row will ever appear on
			// that operator — that's correct (nothing to undo). We
			// only assert on rows that DID land.
			for _, r := range newRows(i) {
				if r.Status == st.FlowExecutionStatusInFlight {
					return false
				}
			}
		}
		return true
	}, 10*time.Second, 200*time.Millisecond,
		"every FlowExecution row left behind by the cancelled renew must reach a terminal state via the engine's cleanup ctx, not via the reconciler")

	// All present rows must agree on outcome — coordinator and any
	// participants that wrote rows must record the same terminal state,
	// or gossip dispatch was incomplete and we're in a divergence.
	observed := make(map[st.FlowExecutionStatus]int)
	for _, i := range operatorIndices {
		for _, r := range newRows(i) {
			observed[r.Status]++
		}
	}
	assert.Len(t, observed, 1,
		"all FlowExecution rows from a single execution must record the same terminal status; got %v", observed)
}

// TestFlowExecution_RenewLeafConsensus_RequestCancellation_LeafStateConsistent
// is the domain-level companion to the row-state cancellation test above:
// it cancels mid-renew and asserts the renew flow ended *correctly* from
// the application's point of view — not just that engine bookkeeping
// settled.
//
// "Correctly" means: every operator must agree on what happened to the
// leaf, and the leaf must not be left in the transient RenewLocked
// status. Two valid outcomes:
//
//   - All operators COMMITTED: leaf is back to AVAILABLE, leaf's parent
//     points at a freshly-created split node (renew applied cleanly,
//     the cancel raced past commit gossip dispatch).
//   - All operators ROLLED_BACK: leaf is back to AVAILABLE with its
//     original parent unchanged (rollback released the lock and undid
//     the prepare-time mutation).
//
// The pre-fix bug would manifest as: some operators ROLLED_BACK while
// others were stuck IN_FLIGHT (leaf in RenewLocked, requiring manual
// recovery), or as divergence — coordinator COMMITTED but participants
// ROLLED_BACK because the engine's commit gossip never dispatched.
func TestFlowExecution_RenewLeafConsensus_RequestCancellation_LeafStateConsistent(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}

	config := wallet.NewTestWalletConfig(t)
	operatorIndices := operatorIndicesFromConfig(config)

	// Single ent client per operator, reused across all DB inspections —
	// re-opening per query saturates Postgres' default connection limit
	// when 5 SOs are already each holding a pool slice.
	clients := make(map[int]*ent.Client, len(operatorIndices))
	for _, i := range operatorIndices {
		clients[i] = db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		t.Cleanup(func() { _ = clients[i].Close() })
	}

	preExistingFlowIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		ids, err := clients[i].FlowExecution.Query().IDs(t.Context())
		require.NoError(t, err)
		set := make(map[uuid.UUID]struct{}, len(ids))
		for _, id := range ids {
			set[id] = struct{}{}
		}
		preExistingFlowIDs[i] = set
	}

	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(config, faucet, leafPrivKey, 100000)
	require.NoError(t, err)
	require.Equal(t, "AVAILABLE", rootNode.Status)
	leafUUID, err := uuid.Parse(rootNode.Id)
	require.NoError(t, err)

	modifyNodeTimelockAllOperators(t, config, rootNode.Id, 0, timelockBelowRenewThreshold)

	// Snapshot the leaf's parent id on every operator BEFORE the renew —
	// this is the signal we use to distinguish committed-renew (parent
	// changed to the new split node) from rolled-back-renew (parent
	// unchanged). uuid.Nil = "no parent" (root node), which is a valid
	// pre-state for a freshly-created single-node tree.
	leafParentBefore := make(map[int]uuid.UUID, len(operatorIndices))
	for _, i := range operatorIndices {
		row, err := clients[i].TreeNode.Query().
			Where(treenode.ID(leafUUID)).
			WithParent().
			Only(t.Context())
		require.NoError(t, err, "operator %d must hold the leaf at test start", i)
		if row.Edges.Parent != nil {
			leafParentBefore[i] = row.Edges.Parent.ID
		}
	}

	authToken, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	parentCtx := wallet.ContextWithToken(t.Context(), authToken)
	leaf := queryLeafByID(t, config, authToken, rootNode.Id)

	// Tight timeout designed to land cancellation inside Execute. Either
	// outcome (commit-races-past-cancel or cancel-lands-mid-flight) is a
	// valid path through the engine; we just want both to converge to a
	// consistent application-level state.
	ctx, cancel := context.WithTimeout(parentCtx, 75*time.Millisecond)
	defer cancel()
	_, _ = wallet.RenewNodeZeroTimelock(ctx, config, leaf, leafPrivKey)

	// Helper to read the new FlowExecution rows (post-renew) on a
	// per-operator basis. Closure over the long-lived clients so the
	// pool stays bounded.
	//
	// Uses assert.NoError + return nil so a DB error inside
	// require.Eventually's goroutine surfaces as a real failure rather
	// than getting swallowed by t.Fatal → runtime.Goexit (which would
	// otherwise read as "condition never satisfied in time").
	newFlowRows := func(i int) []*ent.FlowExecution {
		rows, err := clients[i].FlowExecution.Query().
			Order(ent.Asc(flowexecution.FieldCreateTime)).
			All(t.Context())
		if !assert.NoError(t, err) {
			return nil
		}
		var out []*ent.FlowExecution
		for _, r := range rows {
			if _, seen := preExistingFlowIDs[i][r.ID]; !seen {
				out = append(out, r)
			}
		}
		return out
	}

	// If the 75ms timeout fires before the coordinator even reaches
	// createCoordinatorRow, no row was ever created — nothing to assert
	// on. Same convention as the row-state companion test.
	totalNew := 0
	for _, i := range operatorIndices {
		totalNew += len(newFlowRows(i))
	}
	if totalNew == 0 {
		t.Skip("no FlowExecution rows written — cancel landed before any row was created")
	}

	// Wait for the engine's cleanup ctx to drive every persisted row to
	// a terminal state. Without layer 1 this would never converge.
	require.Eventually(t, func() bool {
		for _, i := range operatorIndices {
			for _, r := range newFlowRows(i) {
				if r.Status == st.FlowExecutionStatusInFlight {
					return false
				}
			}
		}
		return true
	}, 10*time.Second, 200*time.Millisecond,
		"every FlowExecution row must reach a terminal state on the engine cleanup ctx")

	// Domain-level invariants on the leaf itself:
	//
	// 1. No operator may have the leaf stuck in RenewLocked. RenewLocked
	//    is the prepare-time transient status; if any operator is still
	//    in it, prepare's lock was never released — the user-facing bug.
	// 2. Every operator must agree on the leaf's terminal status (no
	//    divergence between coordinator and participants).
	// 3. Every operator must agree on whether the leaf's parent changed
	//    (committed) or stayed the same (rolled back) — divergence here
	//    would mean some operators committed and others rolled back,
	//    which is the worst kind of split-brain.
	leafStatusByOp := make(map[int]st.TreeNodeStatus, len(operatorIndices))
	leafParentChangedByOp := make(map[int]bool, len(operatorIndices))
	for _, i := range operatorIndices {
		row, err := clients[i].TreeNode.Query().
			Where(treenode.ID(leafUUID)).
			WithParent().
			Only(t.Context())
		require.NoError(t, err, "operator %d must still hold the leaf after the renew settles", i)

		assert.NotEqual(t, st.TreeNodeStatusRenewLocked, row.Status,
			"operator %d still has the leaf in RenewLocked — prepare's lock was never released, the bug layer 1 fixes", i)
		leafStatusByOp[i] = row.Status

		// uuid.Nil → no parent, matching the snapshot convention above.
		var parentAfter uuid.UUID
		if row.Edges.Parent != nil {
			parentAfter = row.Edges.Parent.ID
		}
		leafParentChangedByOp[i] = parentAfter != leafParentBefore[i]
	}

	// Status agreement.
	statusSet := make(map[st.TreeNodeStatus]struct{})
	for _, s := range leafStatusByOp {
		statusSet[s] = struct{}{}
	}
	assert.Len(t, statusSet, 1,
		"every operator must agree on the leaf's terminal status; got per-operator: %v", leafStatusByOp)

	// Parent-changed agreement.
	parentChangedSet := make(map[bool]struct{})
	for _, c := range leafParentChangedByOp {
		parentChangedSet[c] = struct{}{}
	}
	require.Len(t, parentChangedSet, 1,
		"every operator must agree on whether the renew committed (parent changed) or rolled back (parent unchanged); got per-operator: %v", leafParentChangedByOp)

	// Cross-check: parent-changed flag must match the FlowExecution
	// terminal status on every operator that DID write a row. If parent
	// changed → row should be COMMITTED; if parent unchanged → row
	// should be ROLLED_BACK.
	//
	// An operator with zero new rows is a valid outcome of the cancel
	// race (its participant gRPC ctx was cancelled before the row
	// persisted) — same outcome the row-terminal companion test
	// permits. We skip those operators here rather than assert exactly
	// one row, otherwise the test flakes on the same race.
	for _, i := range operatorIndices {
		rows := newFlowRows(i)
		if len(rows) == 0 {
			continue
		}
		require.Len(t, rows, 1, "operator %d should have at most one new FlowExecution row", i)
		row := rows[0]
		if leafParentChangedByOp[i] {
			assert.Equal(t, st.FlowExecutionStatusCommitted, row.Status,
				"operator %d: leaf parent changed (renew applied) but FlowExecution row is %s", i, row.Status)
		} else {
			assert.Equal(t, st.FlowExecutionStatusRolledBack, row.Status,
				"operator %d: leaf parent unchanged (renew not applied) but FlowExecution row is %s", i, row.Status)
		}
	}
}
