package grpctest

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// opTypeTokenTransaction is the int32 value of
// CONSENSUS_OPERATION_TYPE_TOKEN_TRANSACTION, derived from the proto enum so
// renumbering it surfaces a compile error rather than vacuously passing.
const opTypeTokenTransaction = int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_TOKEN_TRANSACTION)

// buildConsensusTestTokenCreateTx builds a legacy-shape token create
// transaction for the V3 broadcast helper. V3 metadata validation requires
// the operator identity keys in strictly ascending byte order.
func buildConsensusTestTokenCreateTx(config *wallet.TestWalletConfig, issuerKey keys.Private, name, ticker string) *tokenpb.TokenTransaction {
	operatorKeys := make([][]byte, 0, len(config.SigningOperators))
	for _, operator := range config.SigningOperators {
		operatorKeys = append(operatorKeys, operator.IdentityPublicKey.Serialize())
	}
	slices.SortFunc(operatorKeys, bytes.Compare)

	maxSupply := make([]byte, 16)
	maxSupply[15] = 100

	return &tokenpb.TokenTransaction{
		Version: 3,
		TokenInputs: &tokenpb.TokenTransaction_CreateInput{
			CreateInput: &tokenpb.TokenCreateInput{
				IssuerPublicKey: issuerKey.Public().Serialize(),
				TokenName:       name,
				TokenTicker:     ticker,
				Decimals:        8,
				MaxSupply:       maxSupply,
				IsFreezable:     false,
			},
		},
		TokenOutputs:                    []*tokenpb.TokenOutput{},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: operatorKeys,
		// V3 validation rejects sub-microsecond precision (Linux clocks return
		// full nanoseconds; macOS's 1µs resolution masks the difference).
		ClientCreatedTimestamp: timestamppb.New(utils.ToMicrosecondPrecision(time.Now().UTC())),
	}
}

// TestTokenCreate_Consensus_HappyPath drives a token create through the 2PC
// engine with KnobUseConsensusTokenCreate set, and verifies:
//   - BroadcastTransaction returns COMMIT_FINALIZED with a token identifier
//   - every operator holds a FINALIZED create transaction for that identifier
//     (Prepare ran everywhere; commit gossip finalized the participants)
//   - the coordinator identity recorded on every SO's row is the real
//     coordinator's key, derived from the engine's authenticated
//     coordinator_index rather than a self-declared payload field
func TestTokenCreate_Consensus_HappyPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot route through consensus engine: %v", err)
	}
	require.NoError(t, kc.SetKnob(t, knobs.KnobUseConsensusTokenCreate, 100))

	issuerKey := keys.GeneratePrivateKey()
	config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerKey)
	createTx := buildConsensusTestTokenCreateTx(config, issuerKey, "Consensus Token", "CTK")

	resp, err := wallet.BroadcastTokenTransactionV3WithResponse(t.Context(), config, createTx, []keys.Private{issuerKey}, 0)
	require.NoError(t, err, "consensus token create should succeed")
	require.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.GetCommitStatus())
	require.NotEmpty(t, resp.GetTokenIdentifier())

	coordinatorPubKey := config.SigningOperators[config.CoordinatorIdentifier].IdentityPublicKey
	for _, i := range operatorIndicesFromConfig(config) {
		entClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		t.Cleanup(func() { _ = entClient.Close() })
		tokenCreateEnt, err := entClient.TokenCreate.Query().
			Where(tokencreate.TokenIdentifierEQ(resp.GetTokenIdentifier())).
			WithTokenTransaction().
			Only(t.Context())
		require.NoError(t, err, "operator %d missing token create row", i)
		require.Len(t, tokenCreateEnt.Edges.TokenTransaction, 1, "operator %d token create should have exactly one transaction", i)
		tx := tokenCreateEnt.Edges.TokenTransaction[0]
		assert.Equal(t, st.TokenTransactionStatusFinalized, tx.Status, "operator %d create transaction status mismatch", i)
		assert.Equal(t, coordinatorPubKey, tx.CoordinatorPublicKey, "operator %d coordinator identity mismatch", i)
	}
}

// TestTokenCreate_Consensus_WritesFlowExecutionRows asserts every operator
// writes a TOKEN_TRANSACTION FlowExecution row in COMMITTED state sharing the
// coordinator's execution id, with role aligned to coordinator/participant.
func TestTokenCreate_Consensus_WritesFlowExecutionRows(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot route through consensus engine: %v", err)
	}
	require.NoError(t, kc.SetKnob(t, knobs.KnobUseConsensusTokenCreate, 100))

	issuerKey := keys.GeneratePrivateKey()
	config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerKey)
	coordinatorIdx := int(config.SigningOperators[config.CoordinatorIdentifier].ID)
	operatorIndices := operatorIndicesFromConfig(config)

	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		preExistingIDs[i] = snapshotFlowExecutionIDs(t, operatorDatabasePath(t, i))
	}

	createTx := buildConsensusTestTokenCreateTx(config, issuerKey, "Consensus Flow Token", "CFT")
	resp, err := wallet.BroadcastTokenTransactionV3WithResponse(t.Context(), config, createTx, []keys.Private{issuerKey}, 0)
	require.NoError(t, err)
	require.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.GetCommitStatus())

	newRowsByOperator := make(map[int]*ent.FlowExecution, len(operatorIndices))
	for _, i := range operatorIndices {
		var rows []*ent.FlowExecution
		for _, r := range newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i]) {
			if r.OpType == opTypeTokenTransaction {
				rows = append(rows, r)
			}
		}
		require.Len(t, rows, 1, "operator %d should have exactly one new TOKEN_TRANSACTION flow execution row", i)
		newRowsByOperator[i] = rows[0]
	}

	coordinatorRow := newRowsByOperator[coordinatorIdx]
	require.NotNil(t, coordinatorRow)
	assert.Equal(t, st.FlowExecutionRoleCoordinator, coordinatorRow.Role)
	assert.Equal(t, st.FlowExecutionStatusCommitted, coordinatorRow.Status)

	for _, i := range operatorIndices {
		row := newRowsByOperator[i]
		assert.Equal(t, coordinatorRow.ID, row.ID, "operator %d flow execution id mismatch", i)
		assert.Equal(t, st.FlowExecutionStatusCommitted, row.Status, "operator %d flow execution not committed", i)
		if i == coordinatorIdx {
			continue
		}
		assert.Equal(t, st.FlowExecutionRoleParticipant, row.Role, "operator %d should hold a participant row", i)
		assert.Equal(t, uint(coordinatorIdx), row.CoordinatorIndex, "operator %d coordinator index mismatch", i)
	}
}

// TestTokenCreate_Consensus_DuplicateTokenIdentifierFails verifies the
// duplicate-identifier check holds on the consensus path: a second create
// with identical metadata (same deterministic token identifier) is rejected.
func TestTokenCreate_Consensus_DuplicateTokenIdentifierFails(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot route through consensus engine: %v", err)
	}
	require.NoError(t, kc.SetKnob(t, knobs.KnobUseConsensusTokenCreate, 100))

	issuerKey := keys.GeneratePrivateKey()
	config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerKey)

	first := buildConsensusTestTokenCreateTx(config, issuerKey, "Duplicate Token", "DUP")
	resp, err := wallet.BroadcastTokenTransactionV3WithResponse(t.Context(), config, first, []keys.Private{issuerKey}, 0)
	require.NoError(t, err)
	require.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.GetCommitStatus())

	// Fresh client timestamp -> new partial hash, same token identifier.
	second := buildConsensusTestTokenCreateTx(config, issuerKey, "Duplicate Token", "DUP")
	second.ClientCreatedTimestamp = timestamppb.New(utils.ToMicrosecondPrecision(time.Now().UTC().Add(time.Second)))
	_, err = wallet.BroadcastTokenTransactionV3WithResponse(t.Context(), config, second, []keys.Private{issuerKey}, 0)
	require.ErrorContains(t, err, "already created", "duplicate token identifier must be rejected on the consensus path")
}

// TestTokenCreate_Consensus_KnobOffUsesLegacyPath pins the knob-off behavior:
// creates finalize via the legacy pipeline and write no TOKEN_TRANSACTION
// FlowExecution rows.
func TestTokenCreate_Consensus_KnobOffUsesLegacyPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable: %v", err)
	}
	require.NoError(t, kc.SetKnob(t, knobs.KnobUseConsensusTokenCreate, 0))

	issuerKey := keys.GeneratePrivateKey()
	config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerKey)
	operatorIndices := operatorIndicesFromConfig(config)

	preExistingIDs := make(map[int]map[uuid.UUID]struct{}, len(operatorIndices))
	for _, i := range operatorIndices {
		preExistingIDs[i] = snapshotFlowExecutionIDs(t, operatorDatabasePath(t, i))
	}

	createTx := buildConsensusTestTokenCreateTx(config, issuerKey, "Legacy Token", "LGC")
	resp, err := wallet.BroadcastTokenTransactionV3WithResponse(t.Context(), config, createTx, []keys.Private{issuerKey}, 0)
	require.NoError(t, err, "legacy token create should succeed with the knob off")
	require.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.GetCommitStatus())

	for _, i := range operatorIndices {
		for _, r := range newFlowExecutionsSince(t, operatorDatabasePath(t, i), preExistingIDs[i]) {
			assert.NotEqual(t, opTypeTokenTransaction, r.OpType, "operator %d wrote a TOKEN_TRANSACTION flow execution row on the legacy path", i)
		}
	}
}
