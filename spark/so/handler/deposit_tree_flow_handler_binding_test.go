package handler

import (
	"context"
	"encoding/hex"
	"math"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/sighash"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestDepositTreePrepareBindingProtoValidation(t *testing.T) {
	require.NoError(t, (&pbinternal.DepositTreePrepareRequest{}).ValidateAll(), "legacy prepares must remain valid during rollout")
	require.Error(t, (&pbinternal.DepositTreePrepareRequest{SigningKeyshareId: "not-a-uuid"}).ValidateAll())
	require.Error(t, (&pbinternal.DepositTreePrepareRequest{VerifyingPubkey: make([]byte, 32)}).ValidateAll())
}

func TestBuildDepositCoordinatorFlowPopulatesPrepareBinding(t *testing.T) {
	ctx, cfg, req, expectedKeyshareID, expectedVerifyingKey := depositTreePrepareBoundaryFixture(t)

	flow, err := buildDepositCoordinatorFlow(ctx, cfg, req)
	require.NoError(t, err)

	prepare, ok := flow.PrepareOp().(*pbinternal.DepositTreePrepareRequest)
	require.True(t, ok)
	require.Equal(t, expectedKeyshareID.String(), prepare.GetSigningKeyshareId())
	require.Equal(t, expectedVerifyingKey.Serialize(), prepare.GetVerifyingPubkey())
	require.Same(t, req, prepare.GetOriginalRequest())
}

func TestDepositTreePrepareRejectsBindingMismatchFromDatabase(t *testing.T) {
	t.Run("signing keyshare", func(t *testing.T) {
		ctx, cfg, req, _, expectedVerifyingKey := depositTreePrepareBoundaryFixture(t)
		prepare := &pbinternal.DepositTreePrepareRequest{
			OriginalRequest:   req,
			SigningKeyshareId: uuid.NewString(),
			VerifyingPubkey:   expectedVerifyingKey.Serialize(),
		}

		_, err := NewDepositTreeFlowHandler(cfg).Prepare(ctx, prepare)
		require.ErrorContains(t, err, "signing keyshare id does not match deposit address")
	})

	t.Run("verifying key", func(t *testing.T) {
		ctx, cfg, req, expectedKeyshareID, _ := depositTreePrepareBoundaryFixture(t)
		prepare := &pbinternal.DepositTreePrepareRequest{
			OriginalRequest:   req,
			SigningKeyshareId: expectedKeyshareID.String(),
			VerifyingPubkey:   keys.GeneratePrivateKey().Public().Serialize(),
		}

		_, err := NewDepositTreeFlowHandler(cfg).Prepare(ctx, prepare)
		require.ErrorContains(t, err, "verifying public key does not match deposit address")
	})
}

// Participants must check the coordinator-supplied binding against local
// deposit-address state; decision-only equality would let a malicious
// coordinator substitute the same attacker-chosen values in both phases.
func TestValidateDepositPrepareBinding(t *testing.T) {
	expectedKeyshareID := uuid.New()
	expectedVerifyingKey := keys.GeneratePrivateKey().Public()
	legacyCtx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(nil))
	strictCtx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobRequireDepositTreePrepareBinding: 1,
	}))

	newRequest := func() *pbinternal.DepositTreePrepareRequest {
		return &pbinternal.DepositTreePrepareRequest{
			SigningKeyshareId: expectedKeyshareID.String(),
			VerifyingPubkey:   expectedVerifyingKey.Serialize(),
		}
	}

	require.NoError(t, validateDepositPrepareBinding(legacyCtx, newRequest(), expectedKeyshareID, expectedVerifyingKey))
	require.NoError(t, validateDepositPrepareBinding(strictCtx, newRequest(), expectedKeyshareID, expectedVerifyingKey))

	t.Run("legacy fields absent while knob is off", func(t *testing.T) {
		require.NoError(t, validateDepositPrepareBinding(
			legacyCtx,
			&pbinternal.DepositTreePrepareRequest{},
			expectedKeyshareID,
			expectedVerifyingKey,
		))
	})

	t.Run("legacy fields absent while knob is on", func(t *testing.T) {
		require.ErrorContains(t, validateDepositPrepareBinding(
			strictCtx,
			&pbinternal.DepositTreePrepareRequest{},
			expectedKeyshareID,
			expectedVerifyingKey,
		), "deposit prepare binding is required")
	})

	tests := []struct {
		name        string
		mutate      func(*pbinternal.DepositTreePrepareRequest)
		errContains string
	}{
		{
			name: "partially populated keyshare id",
			mutate: func(req *pbinternal.DepositTreePrepareRequest) {
				req.SigningKeyshareId = ""
			},
			errContains: "must be provided together",
		},
		{
			name: "malformed keyshare id",
			mutate: func(req *pbinternal.DepositTreePrepareRequest) {
				req.SigningKeyshareId = "not-a-uuid"
			},
			errContains: "invalid signing keyshare id",
		},
		{
			name: "substituted keyshare id",
			mutate: func(req *pbinternal.DepositTreePrepareRequest) {
				req.SigningKeyshareId = uuid.NewString()
			},
			errContains: "does not match deposit address",
		},
		{
			name: "partially populated verifying key",
			mutate: func(req *pbinternal.DepositTreePrepareRequest) {
				req.VerifyingPubkey = nil
			},
			errContains: "must be provided together",
		},
		{
			name: "malformed verifying key",
			mutate: func(req *pbinternal.DepositTreePrepareRequest) {
				req.VerifyingPubkey = []byte{0x01}
			},
			errContains: "invalid verifying public key",
		},
		{
			name: "substituted verifying key",
			mutate: func(req *pbinternal.DepositTreePrepareRequest) {
				req.VerifyingPubkey = keys.GeneratePrivateKey().Public().Serialize()
			},
			errContains: "does not match deposit address",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := newRequest()
			tc.mutate(req)
			require.ErrorContains(t, validateDepositPrepareBinding(legacyCtx, req, expectedKeyshareID, expectedVerifyingKey), tc.errContains)
		})
	}
}

func TestDepositTreeValidateDecisionAgainstPrepare(t *testing.T) {
	handler := NewDepositTreeFlowHandler(nil)
	validateWithContext := func(ctx context.Context, prepareOp proto.Message, decisionOp proto.Message) error {
		return handler.ValidateDecisionAgainstPrepare(ctx, prepareOp, decisionOp)
	}
	validate := func(prepareOp proto.Message, decisionOp proto.Message) error {
		return validateWithContext(t.Context(), prepareOp, decisionOp)
	}

	t.Run("legacy prepare fields absent while knob is off", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		prepare.SigningKeyshareId = ""
		prepare.VerifyingPubkey = nil
		require.NoError(t, validate(prepare, commit))
	})

	t.Run("legacy prepare fields absent while knob is on", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		prepare.SigningKeyshareId = ""
		prepare.VerifyingPubkey = nil
		ctx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRequireDepositTreePrepareBinding: 1,
		}))
		require.ErrorContains(t, validateWithContext(ctx, prepare, commit), "deposit prepare binding is required")
	})

	t.Run("legacy prepare still binds value while knob is off", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		prepare.SigningKeyshareId = ""
		prepare.VerifyingPubkey = nil
		commit.Nodes[0].Value = 1_000_000
		require.ErrorContains(t, validate(prepare, commit), "does not match prepared deposit value 1000")
	})

	t.Run("partially populated prepare binding is rejected while knob is off", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		prepare.VerifyingPubkey = nil
		require.ErrorContains(t, validate(prepare, commit), "must be provided together")
	})

	t.Run("matching single utxo commit", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		require.NoError(t, validate(prepare, commit))
	})

	t.Run("matching multi utxo commit", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000, 2_000, 3_000)
		require.NoError(t, validate(prepare, commit))
	})

	for _, tc := range []struct {
		name  string
		value uint64
	}{
		{name: "inflated value", value: 1_000_000},
		{name: "deflated value", value: 999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepare, commit := depositTreeDecisionFixture(t, 1_000)
			commit.Nodes[0].Value = tc.value
			require.ErrorContains(t, validate(prepare, commit), "does not match prepared deposit value 1000")
		})
	}

	t.Run("substituted identity owner", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Nodes[0].OwnerIdentityPubkey = keys.GeneratePrivateKey().Public().Serialize()
		require.ErrorContains(t, validate(prepare, commit), "does not match prepared identity owner")
	})

	t.Run("substituted signing owner", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Nodes[0].OwnerSigningPubkey = keys.GeneratePrivateKey().Public().Serialize()
		require.ErrorContains(t, validate(prepare, commit), "does not match prepared signing owner")
	})

	t.Run("substituted signing keyshare", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Nodes[0].SigningKeyshareId = uuid.NewString()
		require.ErrorContains(t, validate(prepare, commit), "does not match prepared signing keyshare")
	})

	t.Run("malformed commit signing keyshare", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Nodes[0].SigningKeyshareId = "not-a-uuid"
		require.ErrorContains(t, validate(prepare, commit), "invalid commit signing keyshare id")
	})

	t.Run("substituted verifying key", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Nodes[0].VerifyingPubkey = keys.GeneratePrivateKey().Public().Serialize()
		require.ErrorContains(t, validate(prepare, commit), "does not match prepared verifying key")
	})

	t.Run("substituted network", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Network = pb.Network_MAINNET
		require.ErrorContains(t, validate(prepare, commit), "does not match prepared network")
	})

	t.Run("substituted vout", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Nodes[0].Vout = 1
		require.ErrorContains(t, validate(prepare, commit), "does not match prepared primary utxo vout")
	})

	t.Run("substituted root transaction", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Nodes[0].RawTx = mutateDepositTreeTestTx(t, commit.GetNodes()[0].GetRawTx())
		require.ErrorContains(t, validate(prepare, commit), "decision root tx does not match the prepared one")
	})

	t.Run("substituted refund transaction", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Nodes[0].RawRefundTx = mutateDepositTreeTestTx(t, commit.GetNodes()[0].GetRawRefundTx())
		require.ErrorContains(t, validate(prepare, commit), "decision refund tx does not match the prepared one")
	})

	t.Run("substituted direct from cpfp refund transaction", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Nodes[0].DirectFromCpfpRefundTx = mutateDepositTreeTestTx(t, commit.GetNodes()[0].GetDirectFromCpfpRefundTx())
		require.ErrorContains(t, validate(prepare, commit), "decision direct-from-cpfp refund tx does not match the prepared one")
	})

	t.Run("invalid root signature", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Nodes[0].RawTx = corruptDepositTreeTestSignature(t, commit.GetNodes()[0].GetRawTx())
		require.ErrorContains(t, validate(prepare, commit), "root tx signature verification failed")
	})

	t.Run("unexpected direct transaction", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Nodes[0].DirectTx = []byte{0x01}
		require.ErrorContains(t, validate(prepare, commit), "direct transaction must be empty")
	})

	t.Run("unexpected direct refund transaction", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Nodes[0].DirectRefundTx = []byte{0x01}
		require.ErrorContains(t, validate(prepare, commit), "direct refund transaction must be empty")
	})

	t.Run("unexpected refund timelock", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Nodes[0].RefundTimelock = 1
		require.ErrorContains(t, validate(prepare, commit), "refund timelock must be zero")
	})

	t.Run("multiple commit nodes", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Nodes = append(commit.Nodes, &pbinternal.TreeNode{})
		require.ErrorContains(t, validate(prepare, commit), "exactly one root node")
	})

	t.Run("nil commit node", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		commit.Nodes[0] = nil
		require.ErrorContains(t, validate(prepare, commit), "exactly one root node")
	})

	t.Run("child commit node", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		parentID := uuid.NewString()
		commit.Nodes[0].ParentNodeId = &parentID
		require.ErrorContains(t, validate(prepare, commit), "exactly one root node")
	})

	t.Run("unexpected prepare type", func(t *testing.T) {
		_, commit := depositTreeDecisionFixture(t, 1_000)
		require.ErrorContains(t,
			validate(&pbinternal.SendTransferPrepareRequest{}, commit),
			"unexpected prepare operation type",
		)
	})

	t.Run("missing original request", func(t *testing.T) {
		_, commit := depositTreeDecisionFixture(t, 1_000)
		require.ErrorContains(t,
			validate(&pbinternal.DepositTreePrepareRequest{}, commit),
			"has no original request",
		)
	})

	t.Run("unexpected decision type", func(t *testing.T) {
		prepare, _ := depositTreeDecisionFixture(t, 1_000)
		require.ErrorContains(t,
			validate(prepare, &pbinternal.SendTransferCommitRequest{}),
			"unexpected decision operation type",
		)
	})

	t.Run("missing prepared primary utxo", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		prepare.OriginalRequest.OnChainUtxo = nil
		require.ErrorContains(t, validate(prepare, commit), "prepared primary utxo is required")
	})

	t.Run("malformed prepared primary transaction", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		prepare.OriginalRequest.OnChainUtxo.RawTx = []byte{0x01}
		require.ErrorContains(t, validate(prepare, commit), "invalid prepared primary utxo transaction")
	})

	t.Run("prepared primary vout out of bounds", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		prepare.OriginalRequest.OnChainUtxo.Vout = 1
		require.ErrorContains(t, validate(prepare, commit), "prepared primary utxo vout 1 is out of bounds")
	})

	t.Run("missing prepared additional utxo", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000, 2_000)
		prepare.OriginalRequest.AdditionalOnChainUtxos[0] = nil
		require.ErrorContains(t, validate(prepare, commit), "prepared additional utxo 0 is required")
	})

	t.Run("malformed prepared additional transaction", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000, 2_000)
		prepare.OriginalRequest.AdditionalOnChainUtxos[0].RawTx = []byte{0x01}
		require.ErrorContains(t, validate(prepare, commit), "invalid prepared additional utxo 0 transaction")
	})

	t.Run("prepared additional vout out of bounds", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000, 2_000)
		prepare.OriginalRequest.AdditionalOnChainUtxos[0].Vout = 1
		require.ErrorContains(t, validate(prepare, commit), "prepared additional utxo 0 vout 1 is out of bounds")
	})

	t.Run("nonpositive prepared value", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000)
		prepare.OriginalRequest.OnChainUtxo.RawTx = depositTreeTestUTXO(t, 0, keys.GeneratePrivateKey().Public(), 99).GetRawTx()
		require.ErrorContains(t, validate(prepare, commit), "output value must be greater than zero")
	})

	t.Run("prepared value overflow", func(t *testing.T) {
		prepare, commit := depositTreeDecisionFixture(t, 1_000, 2_000)
		fundingKey := keys.GeneratePrivateKey().Public()
		prepare.OriginalRequest.OnChainUtxo.RawTx = depositTreeTestUTXO(t, math.MaxInt64, fundingKey, 100).GetRawTx()
		prepare.OriginalRequest.AdditionalOnChainUtxos[0].RawTx = depositTreeTestUTXO(t, 1, fundingKey, 101).GetRawTx()
		require.ErrorContains(t, validate(prepare, commit), "total deposit value overflows int64")
	})

	t.Run("prepare shaped rollback", func(t *testing.T) {
		prepare, _ := depositTreeDecisionFixture(t, 1_000)
		require.NoError(t, validate(prepare, &pbinternal.DepositTreePrepareRequest{}))
		require.NoError(t, validate(prepare, prepare))
	})
}

func depositTreePrepareBoundaryFixture(t *testing.T) (context.Context, *so.Config, *pb.FinalizeDepositTreeCreationRequest, uuid.UUID, keys.Public) {
	t.Helper()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	cfg.AuthzEnforced = false

	identityKey := keys.GeneratePrivateKey().Public()
	ownerSigningKey := keys.GeneratePrivateKey().Public()
	keyshareSecret := keys.GeneratePrivateKey()
	keyshare, err := sessionCtx.Client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keyshareSecret).
		SetPublicShares(map[string]keys.Public{cfg.Identifier: keyshareSecret.Public()}).
		SetPublicKey(keyshareSecret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	verifyingKey := keyshare.PublicKey.Add(ownerSigningKey)
	depositScript, err := common.P2TRScriptFromPubKey(verifyingKey)
	require.NoError(t, err)

	fundingTx := wire.NewMsgTx(2)
	fundingTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Index: 1}})
	fundingTx.AddTxOut(wire.NewTxOut(1_000, depositScript))
	depositAddressString, err := common.P2TRAddressFromPkScript(depositScript, btcnetwork.Regtest)
	require.NoError(t, err)

	depositAddress, err := sessionCtx.Client.DepositAddress.Create().
		SetAddress(depositAddressString).
		SetOwnerIdentityPubkey(identityKey).
		SetOwnerSigningPubkey(ownerSigningKey).
		SetSigningKeyshare(keyshare).
		SetNetwork(btcnetwork.Regtest).
		SetIsStatic(false).
		SetConfirmationTxid(fundingTx.TxHash().String()).
		SetConfirmationHeight(100).
		Save(ctx)
	require.NoError(t, err)

	txidBytes, err := hex.DecodeString(fundingTx.TxHash().String())
	require.NoError(t, err)
	_, err = sessionCtx.Client.Utxo.Create().
		SetNetwork(btcnetwork.Regtest).
		SetTxid(txidBytes).
		SetVout(0).
		SetBlockHeight(100).
		SetAmount(1_000).
		SetPkScript(depositScript).
		SetAvailabilityConfirmedAt(time.Now()).
		SetDepositAddress(depositAddress).
		Save(ctx)
	require.NoError(t, err)

	rootTx := wire.NewMsgTx(3)
	rootTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: fundingTx.TxHash(), Index: 0},
		Sequence:         spark.ZeroSequence,
	})
	rootTx.AddTxOut(wire.NewTxOut(1_000, depositScript))
	rootTx.AddTxOut(common.EphemeralAnchorOutput())

	refundScript, err := common.P2TRScriptFromPubKey(ownerSigningKey)
	require.NoError(t, err)
	refundTx := wire.NewMsgTx(3)
	refundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		Sequence:         spark.InitialSequence(),
	})
	refundTx.AddTxOut(wire.NewTxOut(rootTx.TxOut[0].Value, refundScript))
	refundTx.AddTxOut(common.EphemeralAnchorOutput())

	directFromCpfpRefundTx := wire.NewMsgTx(3)
	directFromCpfpRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		Sequence:         spark.InitialSequence() + spark.DirectTimelockOffset,
	})
	directFromCpfpRefundTx.AddTxOut(wire.NewTxOut(common.MaybeApplyFee(rootTx.TxOut[0].Value), refundScript))

	newSigningJob := func(tx *wire.MsgTx) *pb.UserSignedTxSigningJob {
		return &pb.UserSignedTxSigningJob{
			SigningPublicKey:       ownerSigningKey.Serialize(),
			RawTx:                  serializeDepositTreeTestTx(t, tx),
			SigningNonceCommitment: frost.GenerateSigningNonce().SigningCommitment().MarshalProto(),
			UserSignature:          []byte{0x01},
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: map[string]*pbcommon.SigningCommitment{
					cfg.Identifier: frost.GenerateSigningNonce().SigningCommitment().MarshalProto(),
				},
			},
		}
	}

	return ctx, cfg, &pb.FinalizeDepositTreeCreationRequest{
		IdentityPublicKey: identityKey.Serialize(),
		OnChainUtxo: &pb.UTXO{
			RawTx:   serializeDepositTreeTestTx(t, fundingTx),
			Vout:    0,
			Network: pb.Network_REGTEST,
		},
		RootTxSigningJob:                 newSigningJob(rootTx),
		RefundTxSigningJob:               newSigningJob(refundTx),
		DirectFromCpfpRefundTxSigningJob: newSigningJob(directFromCpfpRefundTx),
	}, keyshare.ID, verifyingKey
}

func depositTreeDecisionFixture(t *testing.T, primaryValue int64, additionalValues ...int64) (*pbinternal.DepositTreePrepareRequest, *pbinternal.FinalizeTreeCreationRequest) {
	t.Helper()
	identityKey := keys.GeneratePrivateKey().Public().Serialize()
	signer := keys.GeneratePrivateKey()
	signingKey := signer.Public().Serialize()
	keyshareID := uuid.New()
	fundingScript, err := common.P2TRScriptFromPubKey(signer.Public())
	require.NoError(t, err)

	utxos := make([]*pb.UTXO, 0, len(additionalValues)+1)
	utxos = append(utxos, depositTreeTestUTXO(t, primaryValue, signer.Public(), 1))
	for i, value := range additionalValues {
		utxos = append(utxos, depositTreeTestUTXO(t, value, signer.Public(), uint32(i+2)))
	}

	totalValue := uint64(primaryValue)
	rootTx := wire.NewMsgTx(3)
	prevOutputs := make(map[wire.OutPoint]*wire.TxOut, len(utxos))
	for _, utxo := range utxos {
		fundingTx, parseErr := common.TxFromRawTxBytes(utxo.GetRawTx())
		require.NoError(t, parseErr)
		outpoint := wire.OutPoint{Hash: fundingTx.TxHash(), Index: utxo.GetVout()}
		rootTx.AddTxIn(&wire.TxIn{PreviousOutPoint: outpoint})
		prevOutputs[outpoint] = fundingTx.TxOut[utxo.GetVout()]
	}
	for _, value := range additionalValues {
		totalValue += uint64(value)
	}
	rootTx.AddTxOut(wire.NewTxOut(int64(totalValue), fundingScript))
	unsignedRootTx := serializeDepositTreeTestTx(t, rootTx)
	for i := range rootTx.TxIn {
		var hash sighash.Hash
		if len(rootTx.TxIn) == 1 {
			hash, err = sighash.FromTx(rootTx, i, prevOutputs[rootTx.TxIn[i].PreviousOutPoint])
		} else {
			hash, err = sighash.FromMultiPrevOutTx(rootTx, i, prevOutputs)
		}
		require.NoError(t, err)
		rootTx.TxIn[i].Witness = wire.TxWitness{signDepositTreeTestHash(t, signer, hash)}
	}
	signedRootTx := serializeDepositTreeTestTx(t, rootTx)

	refundTx := depositTreeTestRefundTx(t, rootTx, signer, totalValue-1, 0)
	directFromCpfpRefundTx := depositTreeTestRefundTx(t, rootTx, signer, totalValue-2, 1)
	prepare := &pbinternal.DepositTreePrepareRequest{
		SigningKeyshareId: keyshareID.String(),
		VerifyingPubkey:   signer.Public().Serialize(),
		OriginalRequest: &pb.FinalizeDepositTreeCreationRequest{
			IdentityPublicKey: identityKey,
			OnChainUtxo:       utxos[0],
			RootTxSigningJob: &pb.UserSignedTxSigningJob{
				SigningPublicKey: signingKey,
				RawTx:            unsignedRootTx,
			},
			RefundTxSigningJob: &pb.UserSignedTxSigningJob{
				RawTx: serializeDepositTreeTestTx(t, refundTx.unsigned),
			},
			DirectFromCpfpRefundTxSigningJob: &pb.UserSignedTxSigningJob{
				RawTx: serializeDepositTreeTestTx(t, directFromCpfpRefundTx.unsigned),
			},
		},
	}
	prepare.OriginalRequest.AdditionalOnChainUtxos = append(prepare.OriginalRequest.AdditionalOnChainUtxos, utxos[1:]...)
	commit := &pbinternal.FinalizeTreeCreationRequest{
		Network: pb.Network_REGTEST,
		Nodes: []*pbinternal.TreeNode{{
			Value:                  totalValue,
			VerifyingPubkey:        signer.Public().Serialize(),
			OwnerIdentityPubkey:    identityKey,
			OwnerSigningPubkey:     signingKey,
			RawTx:                  signedRootTx,
			RawRefundTx:            refundTx.signed,
			SigningKeyshareId:      keyshareID.String(),
			Vout:                   utxos[0].GetVout(),
			DirectFromCpfpRefundTx: directFromCpfpRefundTx.signed,
		}},
	}
	return prepare, commit
}

func depositTreeTestUTXO(t *testing.T, value int64, publicKey keys.Public, discriminator uint32) *pb.UTXO {
	t.Helper()
	tx := wire.NewMsgTx(2)
	tx.LockTime = discriminator
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Index: discriminator}})
	pkScript, err := common.P2TRScriptFromPubKey(publicKey)
	require.NoError(t, err)
	tx.AddTxOut(&wire.TxOut{Value: value, PkScript: pkScript})
	rawTx, err := common.SerializeTx(tx)
	require.NoError(t, err)
	return &pb.UTXO{RawTx: rawTx, Vout: 0, Network: pb.Network_REGTEST}
}

type depositTreeTestRefund struct {
	unsigned *wire.MsgTx
	signed   []byte
}

func depositTreeTestRefundTx(t *testing.T, rootTx *wire.MsgTx, signer keys.Private, value uint64, lockTime uint32) depositTreeTestRefund {
	t.Helper()
	tx := wire.NewMsgTx(3)
	tx.LockTime = lockTime
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: rootTx.TxHash(), Index: 0}})
	tx.AddTxOut(wire.NewTxOut(int64(value), rootTx.TxOut[0].PkScript))
	unsigned := tx.Copy()
	hash, err := sighash.FromTx(tx, 0, rootTx.TxOut[0])
	require.NoError(t, err)
	tx.TxIn[0].Witness = wire.TxWitness{signDepositTreeTestHash(t, signer, hash)}
	return depositTreeTestRefund{unsigned: unsigned, signed: serializeDepositTreeTestTx(t, tx)}
}

func signDepositTreeTestHash(t *testing.T, signer keys.Private, hash sighash.Hash) []byte {
	t.Helper()
	taprootKey := txscript.TweakTaprootPrivKey(*signer.ToBTCEC(), nil)
	signature, err := schnorr.Sign(taprootKey, hash.Serialize())
	require.NoError(t, err)
	return signature.Serialize()
}

func serializeDepositTreeTestTx(t *testing.T, tx *wire.MsgTx) []byte {
	t.Helper()
	rawTx, err := common.SerializeTx(tx)
	require.NoError(t, err)
	return rawTx
}

func mutateDepositTreeTestTx(t *testing.T, rawTx []byte) []byte {
	t.Helper()
	tx, err := common.TxFromRawTxBytes(rawTx)
	require.NoError(t, err)
	tx.LockTime++
	return serializeDepositTreeTestTx(t, tx)
}

func corruptDepositTreeTestSignature(t *testing.T, rawTx []byte) []byte {
	t.Helper()
	tx, err := common.TxFromRawTxBytes(rawTx)
	require.NoError(t, err)
	require.NotEmpty(t, tx.TxIn)
	require.NotEmpty(t, tx.TxIn[0].Witness)
	tx.TxIn[0].Witness[0][0] ^= 0x01
	return serializeDepositTreeTestTx(t, tx)
}
