package tokens

import (
	"context"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

type retryBroadcastTestSetup struct {
	config   *so.Config
	ctx      context.Context
	client   *ent.Client
	fixtures *entfixtures.Fixtures
}

func setUpRetryBroadcastTest(t *testing.T) *retryBroadcastTestSetup {
	t.Helper()

	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	return &retryBroadcastTestSetup{
		config:   config,
		ctx:      ctx,
		client:   dbClient,
		fixtures: entfixtures.New(t, ctx, dbClient),
	}
}

func retryEnabledKnobs() knobs.Knobs {
	return knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenTransactionV3Phase2RetryEnabled: 100,
	})
}

func TestRetryIncompleteSignatureBroadcasts_NoTransactionsToRetry(t *testing.T) {
	setup := setUpRetryBroadcastTest(t)
	ctx := knobs.InjectKnobsService(setup.ctx, retryEnabledKnobs())

	// No transactions in DB - should return nil without error
	err := RetryIncompleteSignatureBroadcasts(ctx, setup.config)
	require.NoError(t, err)
}

func TestRetryIncompleteSignatureBroadcasts_FiltersCorrectly(t *testing.T) {
	setup := setUpRetryBroadcastTest(t)
	ctx := knobs.InjectKnobsService(setup.ctx, retryEnabledKnobs())

	// Helper to create operator signature
	createSig := func(hash []byte) []byte {
		return ecdsa.Sign(setup.config.IdentityPrivateKey.ToBTCEC(), hash).Serialize()
	}

	// Transaction 1: Should be included (SIGNED, coordinator matches, has sig, not expired, V3, no peer sigs)
	hash1 := []byte("test-hash-should-retry-12345678")
	setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(hash1).
		SetFinalizedTokenTransactionHash(hash1).
		SetStatus(st.TokenTransactionStatusSigned).
		SetOperatorSignature(createSig(hash1)).
		SetCoordinatorPublicKey(setup.config.IdentityPublicKey()).
		SetVersion(st.TokenTransactionVersionV3).
		SetExpiryTime(time.Now().Add(1 * time.Hour)).
		SaveX(ctx)

	// Transaction 2: Should be excluded (wrong status - FINALIZED)
	hash2 := []byte("test-hash-finalized-123456789")
	setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(hash2).
		SetFinalizedTokenTransactionHash(hash2).
		SetStatus(st.TokenTransactionStatusFinalized).
		SetOperatorSignature(createSig(hash2)).
		SetCoordinatorPublicKey(setup.config.IdentityPublicKey()).
		SetVersion(st.TokenTransactionVersionV3).
		SetExpiryTime(time.Now().Add(1 * time.Hour)).
		SaveX(ctx)

	// Transaction 3: Should be excluded (no operator signature)
	hash3 := []byte("test-hash-no-op-sig-123456789")
	setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(hash3).
		SetFinalizedTokenTransactionHash(hash3).
		SetStatus(st.TokenTransactionStatusSigned).
		SetCoordinatorPublicKey(setup.config.IdentityPublicKey()).
		SetVersion(st.TokenTransactionVersionV3).
		SetExpiryTime(time.Now().Add(1 * time.Hour)).
		SaveX(ctx)

	// Transaction 4: Should be excluded (expired)
	hash4 := []byte("test-hash-expired-1234567890")
	setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(hash4).
		SetFinalizedTokenTransactionHash(hash4).
		SetStatus(st.TokenTransactionStatusSigned).
		SetOperatorSignature(createSig(hash4)).
		SetCoordinatorPublicKey(setup.config.IdentityPublicKey()).
		SetVersion(st.TokenTransactionVersionV3).
		SetExpiryTime(time.Now().Add(-1 * time.Hour)). // Expired
		SaveX(ctx)

	// Transaction 5: Should be excluded (V2, not V3+)
	hash5 := []byte("test-hash-v2-version-12345678")
	setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(hash5).
		SetFinalizedTokenTransactionHash(hash5).
		SetStatus(st.TokenTransactionStatusSigned).
		SetOperatorSignature(createSig(hash5)).
		SetCoordinatorPublicKey(setup.config.IdentityPublicKey()).
		SetVersion(st.TokenTransactionVersionV2).
		SetExpiryTime(time.Now().Add(1 * time.Hour)).
		SaveX(ctx)

	// Transaction 6: Should be excluded (different coordinator)
	hash6 := []byte("test-hash-diff-coord-12345678")
	otherCoordinator := setup.fixtures.GeneratePrivateKey()
	setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(hash6).
		SetFinalizedTokenTransactionHash(hash6).
		SetStatus(st.TokenTransactionStatusSigned).
		SetOperatorSignature(createSig(hash6)).
		SetCoordinatorPublicKey(otherCoordinator.Public()).
		SetVersion(st.TokenTransactionVersionV3).
		SetExpiryTime(time.Now().Add(1 * time.Hour)).
		SaveX(ctx)

	ids, err := findTransactionIDsNeedingRetry(ctx, setup.config)
	require.NoError(t, err)
	require.Len(t, ids, 1, "should only find 1 transaction matching all criteria")

	// Verify it's the right transaction
	foundTx, err := setup.client.TokenTransaction.Query().
		Where(tokentransaction.IDEQ(ids[0])).
		Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, hash1, foundTx.FinalizedTokenTransactionHash)
}

func TestRetryIncompleteSignatureBroadcasts_PeerSignatureCountLogic(t *testing.T) {
	setup := setUpRetryBroadcastTest(t)
	ctx := knobs.InjectKnobsService(setup.ctx, retryEnabledKnobs())

	hash := []byte("test-hash-with-peer-sigs-12345")
	operatorSig := ecdsa.Sign(setup.config.IdentityPrivateKey.ToBTCEC(), hash).Serialize()

	// Create transaction
	tx := setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(hash).
		SetFinalizedTokenTransactionHash(hash).
		SetStatus(st.TokenTransactionStatusSigned).
		SetOperatorSignature(operatorSig).
		SetCoordinatorPublicKey(setup.config.IdentityPublicKey()).
		SetVersion(st.TokenTransactionVersionV3).
		SetExpiryTime(time.Now().Add(1 * time.Hour)).
		SaveX(ctx)

	// Add one peer signature
	for _, op := range setup.config.SigningOperatorMap {
		if op.Identifier == setup.config.Identifier {
			continue // Skip self
		}
		peerSig := ecdsa.Sign(setup.config.IdentityPrivateKey.ToBTCEC(), hash).Serialize()
		setup.client.TokenTransactionPeerSignature.Create().
			SetTokenTransaction(tx).
			SetOperatorIdentityPublicKey(op.IdentityPublicKey).
			SetSignature(peerSig).
			SaveX(ctx)
		break // Only add 1 peer signature
	}

	// Query and verify peer signature was added
	txWithSigs, err := setup.client.TokenTransaction.Query().
		Where(tokentransaction.ID(tx.ID)).
		WithPeerSignatures().
		Only(ctx)
	require.NoError(t, err)

	assert.Len(t, txWithSigs.Edges.PeerSignatures, 1, "should have 1 peer signature")

	// With 1 local + 1 peer = 2 total signatures, and test config requires 5 operators,
	// this transaction should need retry
	ids, err := findTransactionIDsNeedingRetry(ctx, setup.config)
	require.NoError(t, err)
	require.Len(t, ids, 1, "transaction with insufficient signatures should need retry")
	assert.Equal(t, tx.ID, ids[0])
}
