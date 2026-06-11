package handler

import (
	"math"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestValidateFinalizeDepositTreeCreationRequestRejectsMissingFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		req     *pb.FinalizeDepositTreeCreationRequest
		wantErr string
	}{
		{
			name:    "nil request",
			req:     nil,
			wantErr: "request is required",
		},
		{
			name: "missing on chain utxo",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.OnChainUtxo = nil
			}),
			wantErr: "on_chain_utxo is required",
		},
		{
			name: "missing root signing job",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.RootTxSigningJob = nil
			}),
			wantErr: "root_tx_signing_job is required",
		},
		{
			name: "missing refund signing job",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.RefundTxSigningJob = nil
			}),
			wantErr: "refund_tx_signing_job is required",
		},
		{
			name: "missing direct from cpfp refund signing job",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.DirectFromCpfpRefundTxSigningJob = nil
			}),
			wantErr: "direct_from_cpfp_refund_tx_signing_job is required",
		},
		{
			name: "empty signing commitments map",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.RootTxSigningJob.SigningCommitments = &pb.SigningCommitments{}
			}),
			wantErr: "root_tx_signing_job.signing_commitments.signing_commitments map is empty",
		},
		{
			name: "too many additional utxos",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.AdditionalOnChainUtxos = make([]*pb.UTXO, 11)
			}),
			wantErr: "too many additional UTXOs",
		},
		{
			name: "nil additional on chain utxo",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.AdditionalOnChainUtxos = []*pb.UTXO{nil}
				req.RootTxSigningJob.AdditionalInputs = []*pb.InputSigningData{validInputSigningDataForValidation()}
			}),
			wantErr: "additional_on_chain_utxos[0] is required",
		},
		{
			name: "additional input count mismatch",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.AdditionalOnChainUtxos = []*pb.UTXO{{Network: pb.Network_REGTEST}}
				req.RootTxSigningJob.AdditionalInputs = nil
			}),
			wantErr: "additional_inputs count (0) must match additional_on_chain_utxos count (1)",
		},
		{
			name: "nil additional input",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.AdditionalOnChainUtxos = []*pb.UTXO{{Network: pb.Network_REGTEST}}
				req.RootTxSigningJob.AdditionalInputs = []*pb.InputSigningData{nil}
			}),
			wantErr: "root_tx_signing_job.additional_inputs[0] is required",
		},
		{
			name: "additional input missing commitments",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.AdditionalOnChainUtxos = []*pb.UTXO{{Network: pb.Network_REGTEST}}
				req.RootTxSigningJob.AdditionalInputs = []*pb.InputSigningData{{
					SigningNonceCommitment: validSigningCommitmentForValidation(),
					UserSignature:          []byte{0x03},
				}}
			}),
			wantErr: "root_tx_signing_job.additional_inputs[0].signing_commitments is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFinalizeDepositTreeCreationRequest(tc.req)
			require.Error(t, err)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestValidateFinalizeDepositTreeCreationRequestAcceptsMinimalValidRequest(t *testing.T) {
	err := validateFinalizeDepositTreeCreationRequest(finalizeDepositTreeCreationRequestWith(func(*pb.FinalizeDepositTreeCreationRequest) {}))
	require.NoError(t, err)
}

func TestVerifyMultiInputRootTransactionRejectsMutatedRoots(t *testing.T) {
	depositScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	primaryTx := newTestDepositFundingTx(t, 60_000, depositScript, testDepositSourceHash(1))
	additionalTx1 := newTestDepositFundingTx(t, 40_000, depositScript, testDepositSourceHash(2))
	additionalTx2 := newTestDepositFundingTx(t, 30_000, depositScript, testDepositSourceHash(3))
	additionalUtxos := []additionalUtxoData{
		{onChainTx: additionalTx1, onChainOutput: additionalTx1.TxOut[0], vout: 0},
		{onChainTx: additionalTx2, onChainOutput: additionalTx2.TxOut[0], vout: 0},
	}

	validRoot := newExpectedMultiInputDepositRootTx(primaryTx, 0, primaryTx.TxOut[0], additionalUtxos)
	require.NoError(t, verifyMultiInputRootTransaction(validRoot, primaryTx, 0, primaryTx.TxOut[0], additionalUtxos))

	wrongOutputScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	for _, tc := range []struct {
		name        string
		mutate      func(*wire.MsgTx)
		wantErrText string
	}{
		{
			name: "wrong version",
			mutate: func(tx *wire.MsgTx) {
				tx.Version = 2
			},
		},
		{
			name: "unsupported version",
			mutate: func(tx *wire.MsgTx) {
				tx.Version = 1
			},
			wantErrText: "root tx version validation failed",
		},
		{
			name: "nonzero locktime",
			mutate: func(tx *wire.MsgTx) {
				tx.LockTime = 1
			},
		},
		{
			name: "wrong primary outpoint with same script and value",
			mutate: func(tx *wire.MsgTx) {
				tx.TxIn[0].PreviousOutPoint.Hash = testDepositSourceHash(10)
			},
		},
		{
			name: "wrong primary vout",
			mutate: func(tx *wire.MsgTx) {
				tx.TxIn[0].PreviousOutPoint.Index = 1
			},
		},
		{
			name: "wrong additional outpoint with same script and value",
			mutate: func(tx *wire.MsgTx) {
				tx.TxIn[1].PreviousOutPoint.Hash = testDepositSourceHash(11)
			},
		},
		{
			name: "wrong additional vout",
			mutate: func(tx *wire.MsgTx) {
				tx.TxIn[1].PreviousOutPoint.Index = 1
			},
		},
		{
			name: "additional inputs reordered",
			mutate: func(tx *wire.MsgTx) {
				tx.TxIn[1], tx.TxIn[2] = tx.TxIn[2], tx.TxIn[1]
			},
		},
		{
			name: "primary input moved after additional input",
			mutate: func(tx *wire.MsgTx) {
				tx.TxIn[0], tx.TxIn[1] = tx.TxIn[1], tx.TxIn[0]
			},
		},
		{
			name: "duplicate primary input",
			mutate: func(tx *wire.MsgTx) {
				tx.TxIn[1].PreviousOutPoint = tx.TxIn[0].PreviousOutPoint
			},
		},
		{
			name: "extra input",
			mutate: func(tx *wire.MsgTx) {
				tx.AddTxIn(&wire.TxIn{
					PreviousOutPoint: wire.OutPoint{Hash: testDepositSourceHash(12), Index: 0},
					Sequence:         spark.ZeroSequence,
				})
			},
			wantErrText: "expected 3 inputs, got 4",
		},
		{
			name: "missing additional input",
			mutate: func(tx *wire.MsgTx) {
				tx.TxIn = tx.TxIn[:2]
			},
			wantErrText: "expected 3 inputs, got 2",
		},
		{
			name: "missing ephemeral anchor output",
			mutate: func(tx *wire.MsgTx) {
				tx.TxOut = tx.TxOut[:1]
			},
		},
		{
			name: "extra change output",
			mutate: func(tx *wire.MsgTx) {
				tx.AddTxOut(wire.NewTxOut(1, depositScript))
			},
		},
		{
			name: "wrong output script",
			mutate: func(tx *wire.MsgTx) {
				tx.TxOut[0].PkScript = wrongOutputScript
			},
		},
		{
			name: "anchor value changed",
			mutate: func(tx *wire.MsgTx) {
				tx.TxOut[1].Value++
			},
		},
		{
			name: "anchor script changed",
			mutate: func(tx *wire.MsgTx) {
				tx.TxOut[1].PkScript = []byte{0x51}
			},
		},
		{
			name: "outputs reordered",
			mutate: func(tx *wire.MsgTx) {
				tx.TxOut[0], tx.TxOut[1] = tx.TxOut[1], tx.TxOut[0]
			},
		},
		{
			name: "wrong total value",
			mutate: func(tx *wire.MsgTx) {
				tx.TxOut[0].Value--
			},
		},
		{
			name: "primary sequence high bit changed",
			mutate: func(tx *wire.MsgTx) {
				tx.TxIn[0].Sequence = spark.ZeroSequence | 1<<16
			},
			wantErrText: "sequence contains unsupported high bits",
		},
		{
			name: "additional sequence high bit changed",
			mutate: func(tx *wire.MsgTx) {
				tx.TxIn[1].Sequence = spark.ZeroSequence | 1<<16
			},
			wantErrText: "sequence contains unsupported high bits",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutatedRoot := validRoot.Copy()
			tc.mutate(mutatedRoot)

			err := verifyMultiInputRootTransaction(mutatedRoot, primaryTx, 0, primaryTx.TxOut[0], additionalUtxos)
			require.Error(t, err)
			wantErrText := tc.wantErrText
			if wantErrText == "" {
				wantErrText = "multi-input root tx does not match expected construction"
			}
			require.ErrorContains(t, err, wantErrText)
		})
	}
}

func TestLoadAndValidateDepositAddressRejectsInvalidAdditionalUtxos(t *testing.T) {
	depositScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)
	wrongDepositScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	primaryTx := newTestDepositFundingTx(t, 60_000, depositScript, testDepositSourceHash(1))
	additionalTx := newTestDepositFundingTx(t, 40_000, depositScript, testDepositSourceHash(2))
	wrongAddressTx := newTestDepositFundingTx(t, 30_000, wrongDepositScript, testDepositSourceHash(3))

	for _, tc := range []struct {
		name        string
		additional  []*pb.UTXO
		wantErrText string
	}{
		{
			name: "duplicate additional outpoint",
			additional: []*pb.UTXO{
				testDepositUtxo(t, additionalTx, 0),
				testDepositUtxo(t, additionalTx, 0),
			},
			wantErrText: "duplicate utxo",
		},
		{
			name: "additional outpoint pays to different address",
			additional: []*pb.UTXO{
				testDepositUtxo(t, wrongAddressTx, 0),
			},
			wantErrText: "additional utxo 0 pays to different address than primary utxo",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.OnChainUtxo = testDepositUtxo(t, primaryTx, 0)
				req.AdditionalOnChainUtxos = tc.additional
			})

			_, _, _, _, err := loadAndValidateDepositAddress(t.Context(), btcnetwork.Regtest, req, keys.GeneratePrivateKey().Public(), false)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.wantErrText)
		})
	}
}

func testDepositUtxo(t *testing.T, tx *wire.MsgTx, vout uint32) *pb.UTXO {
	t.Helper()

	return &pb.UTXO{
		Network: pb.Network_REGTEST,
		RawTx:   serializeTx(t, tx),
		Vout:    vout,
	}
}

func newTestDepositFundingTx(t *testing.T, value int64, pkScript []byte, sourceHash chainhash.Hash) *wire.MsgTx {
	t.Helper()

	tx := newTestTx(value, 0, &sourceHash, pkScript)
	require.Len(t, tx.TxOut, 1)
	return tx
}

func newExpectedMultiInputDepositRootTx(
	primaryTx *wire.MsgTx,
	primaryVout uint32,
	primaryOutput *wire.TxOut,
	additionalUtxos []additionalUtxoData,
) *wire.MsgTx {
	rootTx := wire.NewMsgTx(3)
	rootTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: primaryTx.TxHash(), Index: primaryVout},
		Sequence:         spark.ZeroSequence,
	})

	totalValue := primaryOutput.Value
	for _, additionalUtxo := range additionalUtxos {
		rootTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: additionalUtxo.onChainTx.TxHash(), Index: additionalUtxo.vout},
			Sequence:         spark.ZeroSequence,
		})
		totalValue += additionalUtxo.onChainOutput.Value
	}

	rootTx.AddTxOut(wire.NewTxOut(totalValue, primaryOutput.PkScript))
	rootTx.AddTxOut(common.EphemeralAnchorOutput())
	return rootTx
}

func testDepositSourceHash(tag byte) chainhash.Hash {
	var hash chainhash.Hash
	hash[0] = tag
	return hash
}

func TestVerifyMultiInputRootTransactionRejectsTotalValueOverflow(t *testing.T) {
	depositScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	var primarySource chainhash.Hash
	primarySource[0] = 1
	primaryTx := newTestTx(math.MaxInt64, 0, &primarySource, depositScript)
	primaryTxHash := primaryTx.TxHash()

	var additionalSource chainhash.Hash
	additionalSource[0] = 2
	additionalTx := newTestTx(1, 0, &additionalSource, depositScript)
	additionalTxHash := additionalTx.TxHash()
	additionalUtxos := []additionalUtxoData{{
		onChainTx:     additionalTx,
		onChainOutput: additionalTx.TxOut[0],
		vout:          0,
	}}

	rootTx := wire.NewMsgTx(3)
	rootTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: primaryTxHash, Index: 0},
		Sequence:         0,
	})
	rootTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: additionalTxHash, Index: 0},
		Sequence:         0,
	})

	err = verifyMultiInputRootTransaction(rootTx, primaryTx, 0, primaryTx.TxOut[0], additionalUtxos)
	require.Error(t, err)
	require.ErrorContains(t, err, "total deposit value overflows int64 transaction output limit")
}

func TestVerifyMultiInputRootTransactionAcceptsValidRootSequences(t *testing.T) {
	depositScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	var primarySource chainhash.Hash
	primarySource[0] = 1
	primaryTx := newTestTx(1000, 0, &primarySource, depositScript)
	primaryTxHash := primaryTx.TxHash()

	var additionalSource chainhash.Hash
	additionalSource[0] = 2
	additionalTx := newTestTx(2000, 0, &additionalSource, depositScript)
	additionalTxHash := additionalTx.TxHash()
	additionalUtxos := []additionalUtxoData{{
		onChainTx:     additionalTx,
		onChainOutput: additionalTx.TxOut[0],
		vout:          0,
	}}

	for _, tc := range []struct {
		name     string
		sequence uint32
	}{
		{name: "zero", sequence: 0},
		{name: "spark zero", sequence: spark.ZeroSequence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rootTx := wire.NewMsgTx(3)
			rootTx.AddTxIn(&wire.TxIn{
				PreviousOutPoint: wire.OutPoint{Hash: primaryTxHash, Index: 0},
				Sequence:         tc.sequence,
			})
			rootTx.AddTxIn(&wire.TxIn{
				PreviousOutPoint: wire.OutPoint{Hash: additionalTxHash, Index: 0},
				Sequence:         tc.sequence,
			})
			rootTx.AddTxOut(wire.NewTxOut(3000, depositScript))
			rootTx.AddTxOut(common.EphemeralAnchorOutput())

			err = verifyMultiInputRootTransaction(rootTx, primaryTx, 0, primaryTx.TxOut[0], additionalUtxos)
			require.NoError(t, err)
		})
	}
}

func TestVerifyMultiInputRootTransactionRejectsUnsupportedSequenceHighBits(t *testing.T) {
	depositScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	var primarySource chainhash.Hash
	primarySource[0] = 1
	primaryTx := newTestTx(1000, 0, &primarySource, depositScript)
	primaryTxHash := primaryTx.TxHash()

	var additionalSource chainhash.Hash
	additionalSource[0] = 2
	additionalTx := newTestTx(2000, 0, &additionalSource, depositScript)
	additionalTxHash := additionalTx.TxHash()
	additionalUtxos := []additionalUtxoData{{
		onChainTx:     additionalTx,
		onChainOutput: additionalTx.TxOut[0],
		vout:          0,
	}}

	rootTx := wire.NewMsgTx(3)
	rootTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: primaryTxHash, Index: 0},
		Sequence:         0x00010000,
	})
	rootTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: additionalTxHash, Index: 0},
		Sequence:         0,
	})
	rootTx.AddTxOut(wire.NewTxOut(3000, depositScript))
	rootTx.AddTxOut(common.EphemeralAnchorOutput())

	err = verifyMultiInputRootTransaction(rootTx, primaryTx, 0, primaryTx.TxOut[0], additionalUtxos)
	require.ErrorContains(t, err, "input 0 sequence validation failed")
	require.ErrorContains(t, err, "unsupported high bits 0x00010000")
}

func validFinalizeDepositTreeCreationRequestForValidation() *pb.FinalizeDepositTreeCreationRequest {
	return &pb.FinalizeDepositTreeCreationRequest{
		OnChainUtxo:                      &pb.UTXO{Network: pb.Network_REGTEST, RawTx: []byte{0x01}},
		RootTxSigningJob:                 validUserSignedTxSigningJobForValidation(),
		RefundTxSigningJob:               validUserSignedTxSigningJobForValidation(),
		DirectFromCpfpRefundTxSigningJob: validUserSignedTxSigningJobForValidation(),
		AdditionalOnChainUtxos:           nil,
	}
}

func finalizeDepositTreeCreationRequestWith(mutate func(*pb.FinalizeDepositTreeCreationRequest)) *pb.FinalizeDepositTreeCreationRequest {
	req := validFinalizeDepositTreeCreationRequestForValidation()
	mutate(req)
	return req
}

func validUserSignedTxSigningJobForValidation() *pb.UserSignedTxSigningJob {
	return &pb.UserSignedTxSigningJob{
		SigningPublicKey:       []byte{0x02},
		RawTx:                  []byte{0x01},
		SigningNonceCommitment: validSigningCommitmentForValidation(),
		UserSignature:          []byte{0x03},
		SigningCommitments:     validSigningCommitmentsForValidation(),
	}
}

func validInputSigningDataForValidation() *pb.InputSigningData {
	return &pb.InputSigningData{
		SigningNonceCommitment: validSigningCommitmentForValidation(),
		UserSignature:          []byte{0x03},
		SigningCommitments:     validSigningCommitmentsForValidation(),
	}
}

func validSigningCommitmentsForValidation() *pb.SigningCommitments {
	return &pb.SigningCommitments{
		SigningCommitments: map[string]*pbcommon.SigningCommitment{
			"0000000000000000000000000000000000000000000000000000000000000001": validSigningCommitmentForValidation(),
		},
	}
}

func validSigningCommitmentForValidation() *pbcommon.SigningCommitment {
	return &pbcommon.SigningCommitment{
		Hiding:  []byte{0x02},
		Binding: []byte{0x03},
	}
}
