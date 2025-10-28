package grpctest

import (
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/handler"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateUtxoIsNotSpent(t *testing.T) {
	sparktesting.SkipIfGithubActions(t)
	bitcoinClient := sparktesting.GetBitcoinClient()

	// Test with faucet transaction
	coin, err := faucet.Fund()
	require.NoError(t, err)
	err = handler.ValidateUtxoIsNotSpent(bitcoinClient, coin.OutPoint.Hash, 0)
	require.NoError(t, err)

	// Spend the faucet transaction and test with a new one
	randomKey := keys.GeneratePrivateKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), common.Regtest)
	require.NoError(t, err)

	pkScript, err := txscript.PayToAddrScript(randomAddress)
	require.NoError(t, err)
	txOut := wire.NewTxOut(10_000, pkScript)
	unsignedDepositTx := sparktesting.CreateTestTransaction([]*wire.TxIn{wire.NewTxIn(coin.OutPoint, nil, [][]byte{})}, []*wire.TxOut{txOut})
	signedDepositTx, err := sparktesting.SignFaucetCoin(unsignedDepositTx, coin.TxOut, coin.Key)
	require.NoError(t, err)
	newTxID, err := bitcoinClient.SendRawTransaction(signedDepositTx, true)
	require.NoError(t, err)

	// Make sure the deposit tx gets enough confirmations
	randomKey = keys.GeneratePrivateKey()

	randomAddress, err = common.P2TRRawAddressFromPublicKey(randomKey.Public(), common.Regtest)
	require.NoError(t, err)
	_, err = bitcoinClient.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	// faucet coin is spent
	err = handler.ValidateUtxoIsNotSpent(bitcoinClient, coin.OutPoint.Hash, 0)
	require.Error(t, err)

	// deposit tx is not spent
	err = handler.ValidateUtxoIsNotSpent(bitcoinClient, *newTxID, 0)
	require.NoError(t, err)
}

func TestStaticDepositUserRefund(t *testing.T) {
	bitcoinClient := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	// *********************************************************************************
	// Initiate Users
	// *********************************************************************************
	// 1. Initiate Alice
	aliceConfig := wallet.NewTestWalletConfig(t)

	aliceLeafPrivKey := keys.GeneratePrivateKey()
	_, err = wallet.CreateNewTree(aliceConfig, faucet, aliceLeafPrivKey, 100_000)
	require.NoError(t, err)

	aliceConn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(aliceConfig.CoordinatorAddress(), nil)
	require.NoError(t, err)
	defer aliceConn.Close()

	aliceConnectionToken, err := wallet.AuthenticateWithConnection(t.Context(), aliceConfig, aliceConn)
	require.NoError(t, err)
	aliceCtx := wallet.ContextWithToken(t.Context(), aliceConnectionToken)

	// *********************************************************************************
	// Generate a new static deposit address for Alice
	// *********************************************************************************

	// Generate a new private key for Alice. In a real Wallet that key would be derived from
	// a Signing key using derivation schema
	aliceDepositPrivKey := keys.GeneratePrivateKey()
	leafID := uuid.NewString()

	depositResp, err := wallet.GenerateDepositAddress(
		aliceCtx,
		aliceConfig,
		aliceDepositPrivKey.Public(),
		&leafID,
		true,
	)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	// *********************************************************************************
	// Create Test Deposit TX from Alice
	// *********************************************************************************
	depositAmount := uint64(100_000)
	quoteAmount := uint64(90_000)

	randomKey := keys.GeneratePrivateKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), common.Regtest)
	require.NoError(t, err)

	unsignedDepositTx, err := sparktesting.CreateTestDepositTransactionManyOutputs(
		coin.OutPoint,
		[]string{randomAddress.String(), depositResp.DepositAddress.Address},
		int64(depositAmount),
	)
	require.NoError(t, err)
	vout := 1
	if unsignedDepositTx.TxOut[vout].Value != int64(depositAmount) {
		t.Fatalf("deposit tx output value is not equal to the deposit amount")
	}
	signedDepositTx, err := sparktesting.SignFaucetCoin(unsignedDepositTx, coin.TxOut, coin.Key)
	require.NoError(t, err)
	_, err = bitcoinClient.SendRawTransaction(signedDepositTx, true)
	require.NoError(t, err)

	// *********************************************************************************
	// Create spend tx from Alice's deposit to an Alice wallet address
	// *********************************************************************************
	depositOutPoint := &wire.OutPoint{Hash: signedDepositTx.TxHash(), Index: uint32(vout)}
	spendTx := wire.NewMsgTx(2)
	spendTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: *depositOutPoint,
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         wire.MaxTxInSequenceNum,
	})
	spendPkScript, err := common.P2TRScriptFromPubKey(aliceConfig.IdentityPublicKey())
	require.NoError(t, err)
	spendTx.AddTxOut(wire.NewTxOut(int64(quoteAmount), spendPkScript))

	// *********************************************************************************
	// Create request signature
	// *********************************************************************************
	spendTxSighash, err := common.SigHashFromTx(
		spendTx,
		0,
		signedDepositTx.TxOut[vout],
	)
	require.NoError(t, err)
	userSignature := wallet.CreateUserSignature(
		signedDepositTx.TxHash().String(),
		uint32(vout),
		common.Regtest,
		pb.UtxoSwapRequestType_Refund,
		quoteAmount,
		spendTxSighash,
		aliceConfig.IdentityPrivateKey,
	)

	// *********************************************************************************
	// Refund Static Deposit
	// *********************************************************************************
	t.Run("Refund Static Deposit with unconfirmed UTXO fails", func(t *testing.T) {
		_, err := wallet.RefundStaticDeposit(
			aliceCtx,
			aliceConfig,
			wallet.RefundStaticDepositParams{
				Network:                 common.Regtest,
				SpendTx:                 spendTx,
				DepositAddressSecretKey: aliceDepositPrivKey,
				UserSignature:           userSignature,
				PrevTxOut:               signedDepositTx.TxOut[vout],
			},
		)
		require.ErrorContains(t, err, "utxo not found")
	})

	// Make sure the deposit tx gets enough confirmations
	// Confirm extra buffer to scan more blocks than needed
	// So that we don't race the chain watcher in this test
	_, err = bitcoinClient.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)
	time.Sleep(1000 * time.Millisecond)

	t.Run("Refund Static Deposit by a wrong user fails", func(t *testing.T) {
		bobConfig := wallet.NewTestWalletConfig(t)
		bobConn, err := bobConfig.NewCoordinatorGRPCConnection()
		require.NoError(t, err)
		defer bobConn.Close()

		bobConnectionToken, err := wallet.AuthenticateWithConnection(t.Context(), bobConfig, bobConn)
		require.NoError(t, err)
		bobCtx := wallet.ContextWithToken(t.Context(), bobConnectionToken)

		wrongUserSignature := wallet.CreateUserSignature(
			signedDepositTx.TxHash().String(),
			uint32(vout),
			common.Regtest,
			pb.UtxoSwapRequestType_Refund,
			quoteAmount,
			spendTxSighash,
			bobConfig.IdentityPrivateKey,
		)

		_, err = wallet.RefundStaticDeposit(
			bobCtx,
			bobConfig,
			wallet.RefundStaticDepositParams{
				Network:                 common.Regtest,
				SpendTx:                 spendTx,
				DepositAddressSecretKey: aliceDepositPrivKey,
				UserSignature:           wrongUserSignature,
				PrevTxOut:               signedDepositTx.TxOut[vout],
			},
		)
		require.Error(t, err)
		require.ErrorContains(t, err, "user signature validation failed")
	})

	// Declare outside the t.Run to use in the next t.Run
	var spendTxBytes []byte
	t.Run("Refund Static Deposit with confirmed UTXO succeeds", func(t *testing.T) {
		signedSpendTx, err := wallet.RefundStaticDeposit(
			aliceCtx,
			aliceConfig,
			wallet.RefundStaticDepositParams{
				Network:                 common.Regtest,
				SpendTx:                 spendTx,
				DepositAddressSecretKey: aliceDepositPrivKey,
				UserSignature:           userSignature,
				PrevTxOut:               signedDepositTx.TxOut[vout],
			},
		)
		require.NoError(t, err)
		spendTxBytes, err = common.SerializeTx(signedSpendTx)
		require.NoError(t, err)
		assert.NotEmpty(t, spendTxBytes)

		// Sign, broadcast, and mine spend tx
		txID, err := bitcoinClient.SendRawTransaction(signedSpendTx, true)
		require.NoError(t, err)
		require.Len(t, txID, 32)
	})

	t.Run("Refunding a Static Deposit again to another address produces another transaction", func(t *testing.T) {
		spendTx2 := wire.NewMsgTx(2)
		spendTx2.AddTxIn(&wire.TxIn{
			PreviousOutPoint: *depositOutPoint,
			SignatureScript:  nil,
			Witness:          nil,
			Sequence:         wire.MaxTxInSequenceNum,
		})
		withdrawalPubKey := keys.MustParsePublicKeyHex("0252f2cfa8d1f87718c0f3f61b581b7a3dce6bf9a14efd0a501d8969d6ace73a3d")
		spendPkScript2, err := common.P2TRScriptFromPubKey(withdrawalPubKey)
		require.NoError(t, err)
		spendTx2.AddTxOut(wire.NewTxOut(int64(quoteAmount), spendPkScript2))

		spendTxSighash2, err := common.SigHashFromTx(spendTx2, 0, signedDepositTx.TxOut[vout])
		require.NoError(t, err)
		userSignature2 := wallet.CreateUserSignature(
			signedDepositTx.TxHash().String(),
			uint32(vout),
			common.Regtest,
			pb.UtxoSwapRequestType_Refund,
			quoteAmount,
			spendTxSighash2,
			aliceConfig.IdentityPrivateKey,
		)

		signedSpendTx2, err := wallet.RefundStaticDeposit(
			aliceCtx,
			aliceConfig,
			wallet.RefundStaticDepositParams{
				Network:                 common.Regtest,
				SpendTx:                 spendTx2,
				DepositAddressSecretKey: aliceDepositPrivKey,
				UserSignature:           userSignature2,
				PrevTxOut:               signedDepositTx.TxOut[vout],
			},
		)
		require.NoError(t, err)
		spendTxBytes2, err := common.SerializeTx(signedSpendTx2)
		require.NoError(t, err)
		assert.NotEqual(t, spendTxBytes, spendTxBytes2)
	})

	// *********************************************************************************
	// A call to rollback should fail
	// *********************************************************************************
	t.Run("Rollback a Static Deposit fails", func(t *testing.T) {
		sparkInternalClient := pbinternal.NewSparkInternalServiceClient(aliceConn)
		rollbackUtxoSwapRequestMessageHash, err := handler.CreateUtxoSwapStatement(
			handler.UtxoSwapStatementTypeRollback,
			depositOutPoint.Hash.String(),
			depositOutPoint.Index,
			common.Regtest,
		)
		require.NoError(t, err)
		rollbackUtxoSwapRequestSignature := ecdsa.Sign(aliceConfig.IdentityPrivateKey.ToBTCEC(), rollbackUtxoSwapRequestMessageHash)

		_, err = sparkInternalClient.RollbackUtxoSwap(aliceCtx, &pbinternal.RollbackUtxoSwapRequest{
			OnChainUtxo: &pb.UTXO{
				Txid:    depositOutPoint.Hash[:],
				Vout:    depositOutPoint.Index,
				Network: pb.Network_REGTEST,
			},
			Signature:            rollbackUtxoSwapRequestSignature.Serialize(),
			CoordinatorPublicKey: aliceConfig.IdentityPublicKey().Serialize(),
		})
		require.Error(t, err)
	})

	// *********************************************************************************
	// A call to RefundStaticDeposit should fail if the caller is not the owner of the utxo swap
	// *********************************************************************************
	t.Run("Refund Static Deposit again if the caller is not the owner of the utxo swap fails", func(t *testing.T) {
		bobConfig := wallet.NewTestWalletConfig(t)
		bobLeafPrivKey := keys.GeneratePrivateKey()
		_, err = wallet.CreateNewTree(bobConfig, faucet, bobLeafPrivKey, 100_000)
		require.NoError(t, err)

		bobConn, err := bobConfig.NewCoordinatorGRPCConnection()
		require.NoError(t, err)
		defer bobConn.Close()

		bobConnectionToken, err := wallet.AuthenticateWithConnection(t.Context(), bobConfig, bobConn)
		require.NoError(t, err)
		bobCtx := wallet.ContextWithToken(t.Context(), bobConnectionToken)

		_, err = wallet.RefundStaticDeposit(
			bobCtx,
			bobConfig,
			wallet.RefundStaticDepositParams{
				Network:                 common.Regtest,
				SpendTx:                 spendTx,
				DepositAddressSecretKey: aliceDepositPrivKey,
				UserSignature:           userSignature,
				PrevTxOut:               signedDepositTx.TxOut[vout],
			},
		)
		require.ErrorContains(t, err, "utxo swap is already completed by another user")
	})
}
