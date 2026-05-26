package handler

import (
	"bytes"
	"encoding/hex"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/distributed-lab/gripmock"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

func TestStartDepositTreeCreationRejectsDistinctRootTxForSameDepositUTXO(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()

	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	depositHandler := NewDepositHandler(cfg)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{42})
	ownerIdentityKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerSigningKey := keys.MustGeneratePrivateKeyFromRand(rng)
	keyshare := createTestSigningKeyshare(t, ctx, rng, dbTx)
	combinedKey := keyshare.PublicKey.Add(ownerSigningKey.Public())

	depositScript, err := common.P2TRScriptFromPubKey(combinedKey)
	require.NoError(t, err)
	depositTx := newTestTx(depositTestSourceValue, 0, nil, depositScript)
	depositTxHash := depositTx.TxHash()
	rootTemplate := newTestTx(depositTestSourceValue, spark.ZeroSequence, &depositTxHash, depositScript)
	rootTemplate.AddTxOut(common.EphemeralAnchorOutput())

	depositAddress, err := common.P2TRAddressFromPublicKey(combinedKey, btcnetwork.Regtest)
	require.NoError(t, err)
	createdDepositAddress, err := dbTx.DepositAddress.Create().
		SetAddress(depositAddress).
		SetOwnerIdentityPubkey(ownerIdentityKey.Public()).
		SetOwnerSigningPubkey(ownerSigningKey.Public()).
		SetSigningKeyshare(keyshare).
		SetNetwork(btcnetwork.Regtest).
		SetIsStatic(false).
		Save(ctx)
	require.NoError(t, err)

	depositTxidBytes, err := hex.DecodeString(depositTxHash.String())
	require.NoError(t, err)
	_, err = dbTx.Utxo.Create().
		SetNetwork(btcnetwork.Regtest).
		SetTxid(depositTxidBytes).
		SetVout(0).
		SetBlockHeight(100).
		SetAmount(uint64(depositTestSourceValue)).
		SetPkScript(depositScript).
		SetDepositAddress(createdDepositAddress).
		SetAvailabilityConfirmedAt(time.Now()).
		Save(ctx)
	require.NoError(t, err)

	require.NoError(t, gripmock.AddStub("spark_internal.SparkInternalService", "frost_round1", nil, frostRound1StubOutput))
	require.NoError(t, gripmock.AddStub("spark_internal.SparkInternalService", "frost_round2", nil, frostRound2StubOutput))

	buildRequest := func(mutateRoot func(*wire.MsgTx)) *pb.StartDepositTreeCreationRequest {
		rootTx, err := common.TxFromRawTxBytes(serializeTx(t, rootTemplate))
		require.NoError(t, err)
		if mutateRoot != nil {
			mutateRoot(rootTx)
		}
		depositVariant := &depositData{
			depositTx:  depositTx,
			cpfpRootTx: rootTx,
			signingKey: ownerSigningKey,
		}

		return &pb.StartDepositTreeCreationRequest{
			IdentityPublicKey: ownerIdentityKey.Public().Serialize(),
			OnChainUtxo: &pb.UTXO{
				RawTx:   serializeTx(t, depositTx),
				Vout:    0,
				Txid:    []byte(depositTx.TxID()),
				Network: pb.Network_REGTEST,
			},
			RootTxSigningJob: &pb.SigningJob{
				RawTx:                  serializeTx(t, rootTx),
				SigningPublicKey:       ownerSigningKey.Public().Serialize(),
				SigningNonceCommitment: createTestSigningCommitment(rng),
			},
			RefundTxSigningJob: &pb.SigningJob{
				RawTx:                  serializeTx(t, makeClientCpfpTxForDeposit(t, depositVariant, ownerSigningKey.Public())),
				SigningPublicKey:       ownerSigningKey.Public().Serialize(),
				SigningNonceCommitment: createTestSigningCommitment(rng),
			},
			DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{
				RawTx:                  serializeTx(t, makeClientDirectFromCpfpTxForDeposit(t, depositVariant, ownerSigningKey.Public())),
				SigningPublicKey:       ownerSigningKey.Public().Serialize(),
				SigningNonceCommitment: createTestSigningCommitment(rng),
			},
		}
	}

	reqA := buildRequest(nil)
	respA, err := depositHandler.StartDepositTreeCreation(ctx, cfg, reqA)
	require.NoError(t, err)
	require.NotNil(t, respA.RootNodeSignatureShares.GetNodeTxSigningResult())

	root, err := dbTx.TreeNode.Query().Only(ctx)
	require.NoError(t, err)
	originalRawTx := append([]byte(nil), root.RawTx...)
	conflictingRawTx := append([]byte(nil), root.RawTx...)
	conflictingRawTx[len(conflictingRawTx)-1] ^= 0x01
	_, err = root.Update().SetRawTx(conflictingRawTx).Save(ctx)
	require.NoError(t, err)

	_, err = depositHandler.StartDepositTreeCreation(ctx, cfg, reqA)
	require.Error(t, err)
	require.ErrorContains(t, err, "already has different root tx bytes")
	_, err = root.Update().SetRawTx(originalRawTx).Save(ctx)
	require.NoError(t, err)

	reqC := buildRequest(func(rootTx *wire.MsgTx) {
		rootTx.TxIn[0].Sequence = spark.ZeroSequence | 0x00010000
	})
	require.False(t, bytes.Equal(reqA.RootTxSigningJob.RawTx, reqC.RootTxSigningJob.RawTx))

	_, err = depositHandler.StartDepositTreeCreation(ctx, cfg, reqC)
	require.Error(t, err)
	require.ErrorContains(t, err, "unsupported high bits 0x00010000")

	roots, err := dbTx.TreeNode.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, roots, 1)
	require.True(t, bytes.Equal(reqA.RootTxSigningJob.RawTx, roots[0].RawTx))
}
