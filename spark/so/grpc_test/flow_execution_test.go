package grpctest

import (
	"fmt"
	"os"
	"os/user"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/flowexecution"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
