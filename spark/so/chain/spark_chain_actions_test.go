package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/blockheight"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entephemeral"
	ephemeralenttest "github.com/lightsparkdev/spark/so/entephemeral/enttest"
	"github.com/lightsparkdev/spark/so/entephemeral/signingkeysharesecret"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/watchtower"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestProcessSparkChainActions_NoEligibleWork(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	client := tc.Client

	_, err := client.BlockHeight.Create().
		SetHeight(100).
		SetNetwork(btcnetwork.Testnet).
		Save(ctx)
	require.NoError(t, err)

	bitcoinClient := newDeadBitcoinClient(t)

	config := so.Config{SupportedNetworks: []btcnetwork.Network{btcnetwork.Testnet}}
	err = processSparkChainActions(ctx, &config, client, nil, bitcoinClient, btcnetwork.Testnet)
	require.NoError(t, err)

	require.Equal(t, int64(100), requireChainActionHeight(t, ctx, client), "successful run must advance the cursor to the height it ran at")
}

func requireChainActionHeight(t *testing.T, ctx context.Context, client *ent.Client) int64 {
	t.Helper()
	row, err := client.BlockHeight.Query().Where(blockheight.NetworkEQ(btcnetwork.Testnet)).Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, row.ChainActionHeight)
	return *row.ChainActionHeight
}

// newDeadBitcoinClient returns a client with no reachable server, for tests
// whose code path must never issue an RPC call.
func newDeadBitcoinClient(t *testing.T) *rpcclient.Client {
	t.Helper()
	client, err := rpcclient.New(&rpcclient.ConnConfig{DisableTLS: true, HTTPPostMode: true}, nil)
	require.NoError(t, err)
	return client
}

func setTweakFunc(t *testing.T, fn func(context.Context, *so.Config, *ent.CooperativeExit, int64) error) {
	t.Helper()
	original := tweakKeysForCoopExitFunc
	tweakKeysForCoopExitFunc = fn
	t.Cleanup(func() { tweakKeysForCoopExitFunc = original })
}

func TestProcessSparkChainActions_AdvancesCursorFromNilThenSkips(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	ctx, tc := db.NewTestSQLiteContext(t)
	client := tc.Client

	_, err := client.BlockHeight.Create().
		SetHeight(100).
		SetNetwork(btcnetwork.Testnet).
		Save(ctx)
	require.NoError(t, err)

	tweakCalls := 0
	setTweakFunc(t, func(context.Context, *so.Config, *ent.CooperativeExit, int64) error {
		tweakCalls++
		return nil
	})

	createCoopExitForTest(t, ctx, client, rng, 1, 100-int64(knobs.CoopExitConfirmationThreshold)+1)

	bitcoinClient := newDeadBitcoinClient(t)
	config := so.Config{SupportedNetworks: []btcnetwork.Network{btcnetwork.Testnet}}

	// First run: a nil cursor counts as behind — the work runs and the cursor lands at 100.
	err = processSparkChainActions(ctx, &config, client, nil, bitcoinClient, btcnetwork.Testnet)
	require.NoError(t, err)
	require.Equal(t, 1, tweakCalls)
	require.Equal(t, int64(100), requireChainActionHeight(t, ctx, client))

	// Second run at the same height: skipped by the cursor. A newly eligible
	// exit proves the skip — if the gate failed, this run would tweak it.
	skippedExit := createCoopExitForTest(t, ctx, client, rng, 2, 100-int64(knobs.CoopExitConfirmationThreshold)+1)
	err = processSparkChainActions(ctx, &config, client, nil, bitcoinClient, btcnetwork.Testnet)
	require.NoError(t, err)
	require.Equal(t, 1, tweakCalls)
	reloaded, err := client.CooperativeExit.Get(ctx, skippedExit.ID)
	require.NoError(t, err)
	require.Nil(t, reloaded.KeyTweakedHeight)
	require.Equal(t, int64(100), requireChainActionHeight(t, ctx, client))
}

func TestProcessSparkChainActions_SkipsWhenCursorCurrent(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	ctx, tc := db.NewTestSQLiteContext(t)
	client := tc.Client

	_, err := client.BlockHeight.Create().
		SetHeight(100).
		SetChainActionHeight(100).
		SetNetwork(btcnetwork.Testnet).
		Save(ctx)
	require.NoError(t, err)

	tweakCalls := 0
	setTweakFunc(t, func(context.Context, *so.Config, *ent.CooperativeExit, int64) error {
		tweakCalls++
		return nil
	})

	// Eligible at height 100, but the cursor says Spark chain actions already ran there.
	createCoopExitForTest(t, ctx, client, rng, 1, 100-int64(knobs.CoopExitConfirmationThreshold)+1)

	config := so.Config{SupportedNetworks: []btcnetwork.Network{btcnetwork.Testnet}}
	// nil bitcoinClient proves the gate returns before any work runs.
	err = processSparkChainActions(ctx, &config, client, nil, nil, btcnetwork.Testnet)
	require.NoError(t, err)
	require.Equal(t, 0, tweakCalls)

	// Move the cursor behind the tip: the same call now does the work and re-advances.
	_, err = client.BlockHeight.Update().SetChainActionHeight(99).Save(ctx)
	require.NoError(t, err)
	bitcoinClient := newDeadBitcoinClient(t)
	err = processSparkChainActions(ctx, &config, client, nil, bitcoinClient, btcnetwork.Testnet)
	require.NoError(t, err)
	require.Equal(t, 1, tweakCalls)
	require.Equal(t, int64(100), requireChainActionHeight(t, ctx, client))
}

func TestProcessSparkChainActions_AdvancesCursorDespitePerItemFailure(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	ctx, tc := db.NewTestSQLiteContext(t)
	client := tc.Client

	_, err := client.BlockHeight.Create().
		SetHeight(100).
		SetNetwork(btcnetwork.Testnet).
		Save(ctx)
	require.NoError(t, err)

	setTweakFunc(t, func(context.Context, *so.Config, *ent.CooperativeExit, int64) error {
		return assert.AnError
	})

	failingExit := createCoopExitForTest(t, ctx, client, rng, 1, 100-int64(knobs.CoopExitConfirmationThreshold)+1)

	bitcoinClient := newDeadBitcoinClient(t)

	config := so.Config{SupportedNetworks: []btcnetwork.Network{btcnetwork.Testnet}}
	err = processSparkChainActions(ctx, &config, client, nil, bitcoinClient, btcnetwork.Testnet)
	require.NoError(t, err)

	// Per-item failures retry via their eligibility queries on the next block;
	// they must not hold the cursor back and spin the run on every scan tick.
	require.Equal(t, int64(100), requireChainActionHeight(t, ctx, client))
	reloaded, err := client.CooperativeExit.Get(ctx, failingExit.ID)
	require.NoError(t, err)
	require.Nil(t, reloaded.KeyTweakedHeight, "failed tweak stays eligible for the next run")

	// Next block: the eligibility query picks the failed exit up again and the
	// retry completes.
	setTweakFunc(t, func(context.Context, *so.Config, *ent.CooperativeExit, int64) error {
		return nil
	})
	_, err = client.BlockHeight.Update().SetHeight(101).Save(ctx)
	require.NoError(t, err)
	err = processSparkChainActions(ctx, &config, client, nil, bitcoinClient, btcnetwork.Testnet)
	require.NoError(t, err)

	require.Equal(t, int64(101), requireChainActionHeight(t, ctx, client))
	retried, err := client.CooperativeExit.Get(ctx, failingExit.ID)
	require.NoError(t, err)
	require.NotNil(t, retried.KeyTweakedHeight, "failed exit must be retried and tweaked on the next block")
	require.Equal(t, int64(101), *retried.KeyTweakedHeight)
}

func TestProcessSparkChainActions_WatchtowersDisabled(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	client := tc.Client

	_, err := client.BlockHeight.Create().
		SetHeight(100).
		SetNetwork(btcnetwork.Testnet).
		Save(ctx)
	require.NoError(t, err)

	disabled := false
	config := so.Config{
		SupportedNetworks: []btcnetwork.Network{btcnetwork.Testnet},
		BitcoindConfigs: map[string]so.BitcoindConfig{
			"testnet": {ProcessNodesForWatchtowers: &disabled},
		},
	}
	// nil bitcoinClient proves the watchtower path (the only RPC user) is skipped.
	err = processSparkChainActions(ctx, &config, client, nil, nil, btcnetwork.Testnet)
	require.NoError(t, err)
}

func createCoopExitForTest(t *testing.T, ctx context.Context, client *ent.Client, rng *rand.ChaCha8, txidByte byte, confirmationHeight int64) *ent.CooperativeExit {
	t.Helper()
	senderKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverKey := keys.MustGeneratePrivateKeyFromRand(rng)
	xfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Testnet).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetType(schematype.TransferTypeCooperativeExit).
		SetSenderIdentityPubkey(senderKey.Public()).
		SetReceiverIdentityPubkey(receiverKey.Public()).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	exitTxid, err := schematype.NewTxIDFromBytes(bytes.Repeat([]byte{txidByte}, 32))
	require.NoError(t, err)
	coopExit, err := client.CooperativeExit.Create().
		SetTransfer(xfer).
		SetExitTxid(exitTxid).
		SetConfirmationHeight(confirmationHeight).
		Save(ctx)
	require.NoError(t, err)
	return coopExit
}

func TestTweakEligibleCoopExits_TweaksAtThreshold(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	ctx, tc := db.NewTestSQLiteContext(t)
	client := tc.Client

	tweakCalls := 0
	setTweakFunc(t, func(context.Context, *so.Config, *ent.CooperativeExit, int64) error {
		tweakCalls++
		return nil
	})

	coopExit := createCoopExitForTest(t, ctx, client, rng, 1, 100)
	config := so.Config{SupportedNetworks: []btcnetwork.Network{btcnetwork.Testnet}}

	// One confirmation short of the threshold: nothing happens.
	err := tweakEligibleCoopExits(ctx, &config, client, nil, NewTip(100+int64(knobs.CoopExitConfirmationThreshold)-2, chainhash.Hash{}), btcnetwork.Testnet)
	require.NoError(t, err)
	require.Equal(t, 0, tweakCalls)

	// At the threshold: tweaked and marked.
	tweakHeight := 100 + int64(knobs.CoopExitConfirmationThreshold) - 1
	err = tweakEligibleCoopExits(ctx, &config, client, nil, NewTip(tweakHeight, chainhash.Hash{}), btcnetwork.Testnet)
	require.NoError(t, err)
	require.Equal(t, 1, tweakCalls)

	reloaded, err := client.CooperativeExit.Get(ctx, coopExit.ID)
	require.NoError(t, err)
	require.NotNil(t, reloaded.KeyTweakedHeight)
	require.Equal(t, tweakHeight, *reloaded.KeyTweakedHeight)

	// Already tweaked: not picked up again.
	err = tweakEligibleCoopExits(ctx, &config, client, nil, NewTip(tweakHeight+1, chainhash.Hash{}), btcnetwork.Testnet)
	require.NoError(t, err)
	require.Equal(t, 1, tweakCalls)
}

func TestTweakEligibleCoopExits_FailureIsIsolatedAndStaysEligible(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	ctx, tc := db.NewTestSQLiteContext(t)
	client := tc.Client

	failingExit := createCoopExitForTest(t, ctx, client, rng, 1, 100)
	healthyExit := createCoopExitForTest(t, ctx, client, rng, 2, 100)

	setTweakFunc(t, func(_ context.Context, _ *so.Config, ce *ent.CooperativeExit, _ int64) error {
		if ce.ID == failingExit.ID {
			return assert.AnError
		}
		return nil
	})

	config := so.Config{SupportedNetworks: []btcnetwork.Network{btcnetwork.Testnet}}
	tweakHeight := 100 + int64(knobs.CoopExitConfirmationThreshold) - 1
	// Per-item failures are logged, not returned: the next run retries them.
	err := tweakEligibleCoopExits(ctx, &config, client, nil, NewTip(tweakHeight, chainhash.Hash{}), btcnetwork.Testnet)
	require.NoError(t, err)

	reloadedFailing, err := client.CooperativeExit.Get(ctx, failingExit.ID)
	require.NoError(t, err)
	require.Nil(t, reloadedFailing.KeyTweakedHeight, "failed tweak must stay eligible for retry")

	reloadedHealthy, err := client.CooperativeExit.Get(ctx, healthyExit.ID)
	require.NoError(t, err)
	require.NotNil(t, reloadedHealthy.KeyTweakedHeight, "one failing exit must not block the others")
}

func TestTweakCoopExitInOwnTx_CommitsMainAndEphemeralAtomically(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	rng := rand.NewChaCha8([32]byte{10})
	mainClient := tc.Client

	ephemeralClient := ephemeralenttest.Open(t, "sqlite3", "file:coop_exit_own_tx_ephemeral?mode=memory&_fk=1")
	t.Cleanup(func() { _ = ephemeralClient.Close() })

	coopExit, transferID, fixtures := setupCoopExitWithKeyTweaks(t, ctx, mainClient, ephemeralClient, rng, 2)

	err := tweakCoopExitInOwnTx(ctx, &so.Config{}, mainClient, ephemeralClient, coopExit.ID, NewTip(200, chainhash.Hash{}))
	require.NoError(t, err)

	reloaded, err := mainClient.CooperativeExit.Get(ctx, coopExit.ID)
	require.NoError(t, err)
	require.NotNil(t, reloaded.KeyTweakedHeight)
	assert.Equal(t, int64(200), *reloaded.KeyTweakedHeight)

	verifyCtx := entephemeral.Inject(ctx, db.NewDefaultEphemeralSessionFactory(ephemeralClient).NewSession(ctx))
	assertCoopExitTweaked(t, verifyCtx, mainClient, transferID, fixtures)
}

func TestTweakCoopExitInOwnTx_TweakFailureRollsBackBothTxs(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{11})
	mainClient := tc.Client

	ephemeralClient := ephemeralenttest.Open(t, "sqlite3", "file:coop_exit_rollback_ephemeral?mode=memory&_fk=1")
	t.Cleanup(func() { _ = ephemeralClient.Close() })

	coopExit, _, fixtures := setupCoopExitWithKeyTweaks(t, ctx, mainClient, ephemeralClient, rng, 1)

	setTweakFunc(t, func(ctx context.Context, _ *so.Config, _ *ent.CooperativeExit, _ int64) error {
		// Write through the injected tx-backed ephemeral session, then fail
		// without committing: the rollback arm must discard this write.
		ephemeralDb, err := entephemeral.GetDbFromContext(ctx)
		require.NoError(t, err)
		_, err = ephemeralDb.SigningKeyshareSecret.Create().
			SetSigningKeyshareID(fixtures[0].keyshareID).
			SetVersion(99).
			SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng)).
			Save(ctx)
		require.NoError(t, err)
		return assert.AnError
	})

	err := tweakCoopExitInOwnTx(ctx, &so.Config{}, mainClient, ephemeralClient, coopExit.ID, NewTip(200, chainhash.Hash{}))
	require.ErrorIs(t, err, assert.AnError)

	reloaded, err := mainClient.CooperativeExit.Get(ctx, coopExit.ID)
	require.NoError(t, err)
	require.Nil(t, reloaded.KeyTweakedHeight, "failed tweak must stay eligible for retry")

	leaked, err := ephemeralClient.SigningKeyshareSecret.Query().
		Where(signingkeysharesecret.VersionEQ(99)).
		Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, leaked, "ephemeral write must be rolled back with the main transaction")
}

func TestTweakCoopExitInOwnTx_PanicBeforeEphemeralCommitIsNotReclassified(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{12})
	mainClient := tc.Client

	ephemeralClient := ephemeralenttest.Open(t, "sqlite3", "file:coop_exit_panic_ephemeral?mode=memory&_fk=1")
	t.Cleanup(func() { _ = ephemeralClient.Close() })

	coopExit, _, _ := setupCoopExitWithKeyTweaks(t, ctx, mainClient, ephemeralClient, rng, 1)

	setTweakFunc(t, func(context.Context, *so.Config, *ent.CooperativeExit, int64) error {
		panic("boom")
	})

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = tweakCoopExitInOwnTx(ctx, &so.Config{}, mainClient, ephemeralClient, coopExit.ID, NewTip(200, chainhash.Hash{}))
	}()

	// The ephemeral tx has not committed, so retrying is safe: the guard must
	// re-panic the original value, not the divergence sentinel.
	require.Equal(t, "boom", recovered)

	reloaded, err := mainClient.CooperativeExit.Get(ctx, coopExit.ID)
	require.NoError(t, err)
	require.Nil(t, reloaded.KeyTweakedHeight, "panicked tweak must stay eligible for retry")
}

func TestProcessWatchtowerBroadcasts_EligibleNodesAttemptBroadcast(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{13})
	ctx, tc := db.NewTestSQLiteContext(t)
	client := tc.Client

	var broadcastCalls atomic.Int64
	// rpcclient in HTTP POST mode never reads the response id, so a fixed one is fine.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if req.Method == "sendrawtransaction" {
			broadcastCalls.Add(1)
			_, _ = io.WriteString(w, `{"jsonrpc":"1.0","id":1,"result":null,"error":{"code":-26,"message":"rejected by test"}}`)
			return
		}
		// Version detection and any other RPC succeed with a bitcoind-ish reply.
		_, _ = io.WriteString(w, `{"jsonrpc":"1.0","id":1,"result":{"version":250000,"subversion":"/Satoshi:25.0.0/"},"error":null}`)
	}))
	t.Cleanup(server.Close)
	bitcoinClient, err := rpcclient.New(&rpcclient.ConnConfig{
		Host:         strings.TrimPrefix(server.URL, "http://"),
		User:         "test",
		Pass:         "test",
		HTTPPostMode: true,
		DisableTLS:   true,
	}, nil)
	require.NoError(t, err)

	ownerIdentity := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	tree, err := client.Tree.Create().
		SetStatus(schematype.TreeStatusAvailable).
		SetBaseTxid(schematype.NewRandomTxIDForTesting(t)).
		SetOwnerIdentityPubkey(ownerIdentity).
		SetNetwork(btcnetwork.Testnet).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	newKeyshare := func() *ent.SigningKeyshare {
		keyshare, err := client.SigningKeyshare.Create().
			SetPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng)).
			SetMinSigners(1).
			SetPublicShares(map[string]keys.Public{}).
			SetStatus(schematype.KeyshareStatusInUse).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)
		return keyshare
	}
	newNode := func(build func(*ent.TreeNodeCreate)) *ent.TreeNode {
		create := client.TreeNode.Create().
			SetTree(tree).
			SetNetwork(btcnetwork.Testnet).
			SetValue(1_000).
			SetStatus(schematype.TreeNodeStatusAvailable).
			SetVerifyingPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			SetOwnerIdentityPubkey(ownerIdentity).
			SetOwnerSigningPubkey(ownerIdentity).
			SetVout(0).
			SetSigningKeyshare(newKeyshare())
		build(create)
		node, err := create.Save(ctx)
		require.NoError(t, err)
		return node
	}
	// Sequence 5 = block-based relative timelock (bits 31/22 clear), expired at
	// any height >= parent confirmation + 5.
	timelockedTx := &wire.MsgTx{
		Version: 2,
		TxIn:    []*wire.TxIn{{Sequence: 5}},
		TxOut:   []*wire.TxOut{{Value: 1_000}},
	}
	timelockedTxBytes, err := common.SerializeTx(timelockedTx)
	require.NoError(t, err)

	// Branch 1: parent confirmed, child unconfirmed with an expired direct-tx
	// timelock. Parent's refund is marked confirmed so only the child and the
	// refund node below are eligible.
	parent := newNode(func(c *ent.TreeNodeCreate) {
		c.SetRawTx(timelockedTxBytes).SetNodeConfirmationHeight(10).SetRefundConfirmationHeight(11)
	})
	newNode(func(c *ent.TreeNodeCreate) {
		c.SetRawTx(timelockedTxBytes).SetDirectTx(timelockedTxBytes).SetParent(parent)
	})
	// Branch 2: node confirmed with unconfirmed refund and a broadcastable
	// direct refund tx.
	refundNode := newNode(func(c *ent.TreeNodeCreate) {
		c.SetRawTx(timelockedTxBytes).SetNodeConfirmationHeight(20).SetDirectRefundTx(timelockedTxBytes)
	})

	xfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Testnet).
		SetStatus(schematype.TransferStatusSenderKeyTweaked).
		SetType(schematype.TransferTypeTransfer).
		SetSenderIdentityPubkey(ownerIdentity).
		SetReceiverIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetTotalValue(1_000).
		SetExpiryTime(time.Now().Add(time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.TransferLeaf.Create().
		SetTransfer(xfer).
		SetLeaf(refundNode).
		SetPreviousRefundTx(timelockedTxBytes).
		SetIntermediateRefundTx(timelockedTxBytes).
		SetIntermediateDirectRefundTx(timelockedTxBytes).
		Save(ctx)
	require.NoError(t, err)

	nodes, err := watchtower.QueryBroadcastableNodes(ctx, client, 100, btcnetwork.Testnet)
	require.NoError(t, err)
	require.Len(t, nodes, 2, "child with expired timelock and refund-pending node must be eligible")
	leaves, err := watchtower.QueryBroadcastableTransferLeaves(ctx, client, btcnetwork.Testnet)
	require.NoError(t, err)
	require.Len(t, leaves, 1)

	ctx = logging.Inject(ctx, zaptest.NewLogger(t))
	err = processWatchtowerBroadcasts(ctx, client, bitcoinClient, 100, btcnetwork.Testnet)
	require.NoError(t, err, "per-item broadcast failures must be isolated, not returned")
	// One node-tx broadcast (child), one refund broadcast (refundNode), one
	// transfer-leaf direct refund broadcast.
	require.Equal(t, int64(3), broadcastCalls.Load(), "every eligible broadcast must reach the RPC client")
}
