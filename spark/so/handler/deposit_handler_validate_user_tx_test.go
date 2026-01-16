package handler

import (
	"context"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
)

// --- Helpers for constructing minimal valid transactions and DB state ---

const (
	depositTestTimeLock    = spark.InitialTimeLock
	depositTestSourceValue = 100000
)

type depositData struct {
	depositTx *wire.MsgTx
	// RootTx
	cpfpRootTx   *wire.MsgTx
	directRootTx *wire.MsgTx
	// Keys
	signingKey keys.Private // Signing key of the deposit address
}

func createDepositData(t *testing.T) *depositData {
	t.Helper()

	signingKey := keys.GeneratePrivateKey()
	srcScript, err := common.P2TRScriptFromPubKey(signingKey.Public())
	require.NoError(t, err)

	directSeq := spark.DirectTimelockOffset
	depositTx := newTestTx(depositTestSourceValue, 0, nil, srcScript)
	depositTxHash := depositTx.TxHash()

	// 2 types of rootTx
	cpfpRootTx := newTestTx(depositTestSourceValue, 0, &depositTxHash, srcScript)
	cpfpRootTx.AddTxOut(common.EphemeralAnchorOutput())
	directRootTx := newTestTx(depositTestSourceValue, directSeq, &depositTxHash, srcScript)
	directRootTx.TxOut[0].Value = common.MaybeApplyFee(depositTestSourceValue)

	return &depositData{
		depositTx:    depositTx,
		cpfpRootTx:   cpfpRootTx,
		directRootTx: directRootTx,
		signingKey:   signingKey,
	}
}

func makeClientCpfpTxForDeposit(t *testing.T, deposit *depositData, refundDest keys.Public) *wire.MsgTx {
	userScript, err := common.P2TRScriptFromPubKey(refundDest)
	require.NoError(t, err)
	expectedCpfp := depositTestTimeLock
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: deposit.cpfpRootTx.TxHash(), Index: 0},
		Sequence:         expectedCpfp,
	})
	tx.AddTxOut(&wire.TxOut{Value: depositTestSourceValue, PkScript: userScript})
	tx.AddTxOut(common.EphemeralAnchorOutput())
	return tx
}

func makeClientDirectTxForDeposit(t *testing.T, deposit *depositData, refundDest keys.Public) *wire.MsgTx {
	userScript, err := common.P2TRScriptFromPubKey(refundDest)
	require.NoError(t, err)
	expected := depositTestTimeLock + spark.DirectTimelockOffset
	currentValue := deposit.directRootTx.TxOut[0].Value
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: deposit.directRootTx.TxHash(), Index: 0},
		Sequence:         expected,
	})
	tx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(currentValue), PkScript: userScript})
	return tx
}

func makeClientDirectFromCpfpTxForDeposit(t *testing.T, deposit *depositData, refundDest keys.Public) *wire.MsgTx {
	userScript, err := common.P2TRScriptFromPubKey(refundDest)
	require.NoError(t, err)
	expected := depositTestTimeLock + spark.DirectTimelockOffset
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: deposit.cpfpRootTx.TxHash(), Index: 0},
		Sequence:         expected,
	})
	tx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(depositTestSourceValue), PkScript: userScript})
	return tx
}

func depositHandlerWithConfig() *DepositHandler {
	return &DepositHandler{config: &so.Config{}}
}

// callValidateBitcoinTransactions is a test helper that extracts parameters from a request
// and calls the validateBitcoinTransactions function
func callValidateBitcoinTransactions(
	ctx context.Context,
	req *pb.StartDepositTreeCreationRequest,
	rootDestPubkey keys.Public,
	refundDestPubkey keys.Public,
	networkString string,
) error {
	var directRootTxRaw, directRefundTxRaw []byte
	if req.DirectRootTxSigningJob != nil {
		directRootTxRaw = req.DirectRootTxSigningJob.RawTx
	}
	if req.DirectRefundTxSigningJob != nil {
		directRefundTxRaw = req.DirectRefundTxSigningJob.RawTx
	}
	var directFromCpfpRefundTxRaw []byte
	if req.DirectFromCpfpRefundTxSigningJob != nil {
		directFromCpfpRefundTxRaw = req.DirectFromCpfpRefundTxSigningJob.RawTx
	}

	return validateBitcoinTransactions(
		ctx,
		req.OnChainUtxo.RawTx,
		req.OnChainUtxo.Vout,
		req.RootTxSigningJob.RawTx,
		req.RefundTxSigningJob.RawTx,
		directFromCpfpRefundTxRaw,
		directRootTxRaw,
		directRefundTxRaw,
		rootDestPubkey,
		refundDestPubkey,
		networkString,
	)
}

// --- Tests ---
func TestValidateUserTxs_Cpfp_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	deposit := createDepositData(t)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: keys.GeneratePrivateKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			RawTx:   serializeTx(t, deposit.depositTx),
			Vout:    0,
			Txid:    []byte(deposit.depositTx.TxID()),
			Network: pb.Network_REGTEST,
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.cpfpRootTx),
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, makeClientCpfpTxForDeposit(t, deposit, refundDest)),
			SigningPublicKey: refundDest.Serialize(),
		},
	}

	_ = depositHandlerWithConfig()
	err := callValidateBitcoinTransactions(ctx, req, deposit.signingKey.Public(), refundDest, pb.Network_REGTEST.String())
	require.NoError(t, err)
}

func TestValidateUserDepositTxs_Legacy_Cpfp_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	deposit := createDepositData(t)
	refundDest := keys.GeneratePrivateKey().Public()
	cpfpRefundTx := makeClientCpfpTxForDeposit(t, deposit, refundDest)
	cpfpRefundTx.TxIn[0].Sequence = (1 << 30) | depositTestTimeLock

	req := &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: keys.GeneratePrivateKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			RawTx:   serializeTx(t, deposit.depositTx),
			Vout:    0,
			Txid:    []byte(deposit.depositTx.TxID()),
			Network: pb.Network_REGTEST,
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.cpfpRootTx),
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, cpfpRefundTx),
			SigningPublicKey: refundDest.Serialize(),
		},
	}

	_ = depositHandlerWithConfig()
	err := callValidateBitcoinTransactions(ctx, req, deposit.signingKey.Public(), refundDest, pb.Network_REGTEST.String())
	require.NoError(t, err)
}

func TestValidateUserTxs_Direct_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	deposit := createDepositData(t)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: keys.GeneratePrivateKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			RawTx:   serializeTx(t, deposit.depositTx),
			Vout:    0,
			Txid:    []byte(deposit.depositTx.TxID()),
			Network: pb.Network_REGTEST,
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.cpfpRootTx),
		},
		DirectRootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.directRootTx),
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, makeClientCpfpTxForDeposit(t, deposit, refundDest)),
			SigningPublicKey: refundDest.Serialize(),
		},
		DirectRefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, makeClientDirectTxForDeposit(t, deposit, refundDest)),
			SigningPublicKey: refundDest.Serialize(),
		},
	}

	_ = depositHandlerWithConfig()
	err := callValidateBitcoinTransactions(ctx, req, deposit.signingKey.Public(), refundDest, pb.Network_REGTEST.String())
	require.NoError(t, err)
}

func TestValidateUserTxs_DirectFromCpfp_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	deposit := createDepositData(t)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: keys.GeneratePrivateKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			RawTx:   serializeTx(t, deposit.depositTx),
			Vout:    0,
			Txid:    []byte(deposit.depositTx.TxID()),
			Network: pb.Network_REGTEST,
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.cpfpRootTx),
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, makeClientCpfpTxForDeposit(t, deposit, refundDest)),
			SigningPublicKey: refundDest.Serialize(),
		},
		DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, makeClientDirectFromCpfpTxForDeposit(t, deposit, refundDest)),
			SigningPublicKey: refundDest.Serialize(),
		},
	}

	_ = depositHandlerWithConfig()
	err := callValidateBitcoinTransactions(ctx, req, deposit.signingKey.Public(), refundDest, pb.Network_REGTEST.String())
	require.NoError(t, err)
}

func TestValidateUserTxs_InvalidRefundCpfp_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	deposit := createDepositData(t)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: keys.GeneratePrivateKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			RawTx:   serializeTx(t, deposit.depositTx),
			Vout:    0,
			Txid:    []byte(deposit.depositTx.TxID()),
			Network: pb.Network_REGTEST,
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.cpfpRootTx),
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:            []byte("invalid refund tx"),
			SigningPublicKey: refundDest.Serialize(),
		},
	}

	_ = depositHandlerWithConfig()
	err := callValidateBitcoinTransactions(ctx, req, deposit.signingKey.Public(), refundDest, pb.Network_REGTEST.String())
	require.ErrorContains(t, err, "cpfp refund transaction verification failed")
}

func TestValidateUserTxs_InvalidRootTxInput_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	deposit := createDepositData(t)
	randomScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	deposit.depositTx.AddTxOut(&wire.TxOut{
		Value:    100,
		PkScript: randomScript,
	})
	newDepositTxHash := deposit.depositTx.TxHash()
	newCpfpRootTx := newTestTx(depositTestSourceValue, 0, &newDepositTxHash, randomScript)
	newCpfpRootTx.TxIn[0].PreviousOutPoint.Index = 1

	req := &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: keys.GeneratePrivateKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			RawTx:   serializeTx(t, deposit.depositTx),
			Vout:    0,
			Txid:    []byte(deposit.depositTx.TxID()),
			Network: pb.Network_REGTEST,
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, newCpfpRootTx),
		},
		RefundTxSigningJob: &pb.SigningJob{},
	}

	_ = depositHandlerWithConfig()
	refundDest := keys.GeneratePrivateKey().Public()
	err = callValidateBitcoinTransactions(ctx, req, deposit.signingKey.Public(), refundDest, pb.Network_REGTEST.String())
	require.ErrorContains(t, err, "cpfp root transaction verification failed: transaction does not match expected construction")
}

func TestValidateUserTxs_CpfpRootTxInvalidSequence_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	deposit := createDepositData(t)
	refundDest := keys.GeneratePrivateKey().Public()
	deposit.cpfpRootTx.TxIn[0].Sequence = 1000 // Should be 0

	req := &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: keys.GeneratePrivateKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			RawTx:   serializeTx(t, deposit.depositTx),
			Vout:    0,
			Txid:    []byte(deposit.depositTx.TxID()),
			Network: pb.Network_REGTEST,
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.cpfpRootTx),
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, makeClientCpfpTxForDeposit(t, deposit, refundDest)),
			SigningPublicKey: refundDest.Serialize(),
		},
	}

	_ = depositHandlerWithConfig()
	err := callValidateBitcoinTransactions(ctx, req, deposit.signingKey.Public(), refundDest, pb.Network_REGTEST.String())
	require.ErrorContains(t, err, "failed to validate client sequence")
}

func TestValidateUserTxs_CpfpRootTxTwoOutputs_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	deposit := createDepositData(t)
	refundDest := keys.GeneratePrivateKey().Public()
	attackerDest := keys.GeneratePrivateKey().Public()
	attackerScript, err := common.P2TRScriptFromPubKey(attackerDest)
	require.NoError(t, err)
	currentValue := deposit.cpfpRootTx.TxOut[0].Value
	deposit.cpfpRootTx.TxOut[0].Value = 100
	deposit.cpfpRootTx.AddTxOut(&wire.TxOut{
		Value:    currentValue - 100,
		PkScript: attackerScript,
	})

	req := &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: keys.GeneratePrivateKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			RawTx:   serializeTx(t, deposit.depositTx),
			Vout:    0,
			Txid:    []byte(deposit.depositTx.TxID()),
			Network: pb.Network_REGTEST,
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.cpfpRootTx),
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, makeClientCpfpTxForDeposit(t, deposit, refundDest)),
			SigningPublicKey: refundDest.Serialize(),
		},
	}

	_ = depositHandlerWithConfig()
	err = callValidateBitcoinTransactions(ctx, req, deposit.signingKey.Public(), refundDest, pb.Network_REGTEST.String())
	require.ErrorContains(t, err, "cpfp root transaction verification failed: transaction does not match expected construction")
}

func TestValidateUserTxs_DirectRootTxInvalidSequence_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	deposit := createDepositData(t)
	refundDest := keys.GeneratePrivateKey().Public()
	deposit.directRootTx.TxIn[0].Sequence = 25 // Should be 50

	req := &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: keys.GeneratePrivateKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			RawTx:   serializeTx(t, deposit.depositTx),
			Vout:    0,
			Txid:    []byte(deposit.depositTx.TxID()),
			Network: pb.Network_REGTEST,
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.cpfpRootTx),
		},
		DirectRootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.directRootTx),
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, makeClientCpfpTxForDeposit(t, deposit, refundDest)),
			SigningPublicKey: refundDest.Serialize(),
		},
		DirectRefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, makeClientDirectTxForDeposit(t, deposit, refundDest)),
			SigningPublicKey: refundDest.Serialize(),
		},
	}

	_ = depositHandlerWithConfig()
	err := callValidateBitcoinTransactions(ctx, req, deposit.signingKey.Public(), refundDest, pb.Network_REGTEST.String())
	require.ErrorContains(t, err, "failed to validate client sequence")
}

func TestValidateUserTxs_DirectRootTxTwoOutputs_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	deposit := createDepositData(t)
	refundDest := keys.GeneratePrivateKey().Public()
	attackerDest := keys.GeneratePrivateKey().Public()
	attackerScript, err := common.P2TRScriptFromPubKey(attackerDest)
	require.NoError(t, err)
	currentValue := deposit.directRootTx.TxOut[0].Value
	deposit.directRootTx.TxOut[0].Value = 100
	deposit.directRootTx.AddTxOut(&wire.TxOut{
		Value:    currentValue - 100,
		PkScript: attackerScript,
	})

	req := &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: keys.GeneratePrivateKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			RawTx:   serializeTx(t, deposit.depositTx),
			Vout:    0,
			Txid:    []byte(deposit.depositTx.TxID()),
			Network: pb.Network_REGTEST,
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.cpfpRootTx),
		},
		DirectRootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.directRootTx),
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, makeClientCpfpTxForDeposit(t, deposit, refundDest)),
			SigningPublicKey: refundDest.Serialize(),
		},
		DirectRefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, makeClientDirectTxForDeposit(t, deposit, refundDest)),
			SigningPublicKey: refundDest.Serialize(),
		},
	}

	_ = depositHandlerWithConfig()
	err = callValidateBitcoinTransactions(ctx, req, deposit.signingKey.Public(), refundDest, pb.Network_REGTEST.String())
	require.ErrorContains(t, err, "direct root transaction verification failed: transaction does not match expected construction")
}

func TestValidateUserTxs_CpfpRefundTxFinalSequence_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	deposit := createDepositData(t)
	refundDest := keys.GeneratePrivateKey().Public()
	cpfpRefundTx := makeClientCpfpTxForDeposit(t, deposit, refundDest)
	cpfpRefundTx.TxIn[0].Sequence = 0xffffffff // Should be InitialTimelock

	req := &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: keys.GeneratePrivateKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			RawTx:   serializeTx(t, deposit.depositTx),
			Vout:    0,
			Txid:    []byte(deposit.depositTx.TxID()),
			Network: pb.Network_REGTEST,
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.cpfpRootTx),
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, cpfpRefundTx),
			SigningPublicKey: refundDest.Serialize(),
		},
	}

	_ = depositHandlerWithConfig()
	err := callValidateBitcoinTransactions(ctx, req, deposit.signingKey.Public(), refundDest, pb.Network_REGTEST.String())
	require.ErrorContains(t, err, "failed to validate user sequence")
}

func TestValidateUserTxs_DirectRefundTxFinalSequence_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	deposit := createDepositData(t)
	refundDest := keys.GeneratePrivateKey().Public()
	directRefundTx := makeClientDirectTxForDeposit(t, deposit, refundDest)
	directRefundTx.TxIn[0].Sequence = 0xffffffff // Should be InitialTimelock

	req := &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: keys.GeneratePrivateKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			RawTx:   serializeTx(t, deposit.depositTx),
			Vout:    0,
			Txid:    []byte(deposit.depositTx.TxID()),
			Network: pb.Network_REGTEST,
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.cpfpRootTx),
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, makeClientCpfpTxForDeposit(t, deposit, refundDest)),
			SigningPublicKey: refundDest.Serialize(),
		},
		DirectRootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.directRootTx),
		},
		DirectRefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, directRefundTx),
			SigningPublicKey: refundDest.Serialize(),
		},
	}

	_ = depositHandlerWithConfig()
	err := callValidateBitcoinTransactions(ctx, req, deposit.signingKey.Public(), refundDest, pb.Network_REGTEST.String())
	require.ErrorContains(t, err, "failed to validate user sequence")
}

func TestValidateUserTxs_DirectFromCpfpRefundTxFinalSequence_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	deposit := createDepositData(t)
	refundDest := keys.GeneratePrivateKey().Public()
	directFromCpfpRefundTx := makeClientDirectFromCpfpTxForDeposit(t, deposit, refundDest)
	directFromCpfpRefundTx.TxIn[0].Sequence = 0xffffffff // Should be InitialTimelock

	req := &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: keys.GeneratePrivateKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			RawTx:   serializeTx(t, deposit.depositTx),
			Vout:    0,
			Txid:    []byte(deposit.depositTx.TxID()),
			Network: pb.Network_REGTEST,
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx: serializeTx(t, deposit.cpfpRootTx),
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, makeClientCpfpTxForDeposit(t, deposit, refundDest)),
			SigningPublicKey: refundDest.Serialize(),
		},
		DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{
			RawTx:            serializeTx(t, directFromCpfpRefundTx),
			SigningPublicKey: refundDest.Serialize(),
		},
	}

	_ = depositHandlerWithConfig()
	err := callValidateBitcoinTransactions(ctx, req, deposit.signingKey.Public(), refundDest, pb.Network_REGTEST.String())
	require.ErrorContains(t, err, "failed to validate user sequence")
}
