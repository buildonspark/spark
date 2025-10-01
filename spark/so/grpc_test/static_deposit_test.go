package grpctest

import (
	"bytes"
	"encoding/hex"
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
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/utxo"
	"github.com/lightsparkdev/spark/so/ent/utxoswap"
	"github.com/lightsparkdev/spark/so/handler"
	"github.com/lightsparkdev/spark/so/objects"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateUtxoIsNotSpent(t *testing.T) {
	skipIfGithubActions(t)
	bitcoinClient := sparktesting.GetBitcoinClient()

	// Test with faucet transaction
	coin, err := faucet.Fund()
	require.NoError(t, err)
	txidString := hex.EncodeToString(coin.OutPoint.Hash[:])
	txIDBytes, err := hex.DecodeString(txidString)
	require.NoError(t, err)
	err = handler.ValidateUtxoIsNotSpent(bitcoinClient, txIDBytes, 0)
	if err != nil {
		t.Fatalf("utxo is spent: %v, txid: %s", err, txidString)
	}

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
	err = handler.ValidateUtxoIsNotSpent(bitcoinClient, txIDBytes, 0)
	require.Error(t, err)

	// deposit tx is not spent
	err = handler.ValidateUtxoIsNotSpent(bitcoinClient, newTxID[:], 0)
	require.NoError(t, err)
}

func TestStaticDepositSSPLegacy(t *testing.T) {
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

	// 2. Initiate SSP
	sspConfig := wallet.NewTestWalletConfig(t)
	sspLeafPrivKey := keys.GeneratePrivateKey()
	sspRootNode, err := wallet.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 90_000)
	require.NoError(t, err)

	sspConn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(sspConfig.CoordinatorAddress(), nil)
	require.NoError(t, err)
	defer sspConn.Close()

	sspConnectionToken, err := wallet.AuthenticateWithConnection(t.Context(), sspConfig, sspConn)
	require.NoError(t, err)
	sspCtx := wallet.ContextWithToken(t.Context(), sspConnectionToken)

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

	// Make sure the deposit tx gets enough confirmations
	// Confirm extra buffer to scan more blocks than needed
	// So that we don't race the chain watcher in this test
	_, err = bitcoinClient.GenerateToAddress(6, randomAddress, nil)
	require.NoError(t, err)
	time.Sleep(10000 * time.Millisecond)

	// *********************************************************************************
	// Create request signatures
	// *********************************************************************************
	// SSP signature committing to a fixed amount quote.
	// Can be obtained from a call for a quote to SSP.
	sspSignature, err := wallet.CreateSspFixedQuoteSignature(
		signedDepositTx.TxHash().String(),
		uint32(vout),
		common.Regtest,
		quoteAmount,
		sspConfig.IdentityPrivateKey,
	)
	require.NoError(t, err)

	// User signature authorizing the SSP to claim the deposit
	// in return for a transfer of a fixed amount
	userSignature := wallet.CreateUserSignature(
		signedDepositTx.TxHash().String(),
		uint32(vout),
		common.Regtest,
		pb.UtxoSwapRequestType_Fixed,
		quoteAmount,
		sspSignature,
		aliceConfig.IdentityPrivateKey,
	)
	// *********************************************************************************
	// Create a Transfer from SSP to Alice
	// *********************************************************************************
	newLeafPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              sspRootNode,
		SigningPrivKey:    sspLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}

	// *********************************************************************************
	// Create spend tx from Alice's deposit to SSP L1 Wallet Address
	// *********************************************************************************
	depositOutPoint := &wire.OutPoint{Hash: signedDepositTx.TxHash(), Index: uint32(vout)}
	spendTx := wire.NewMsgTx(2)
	spendTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: *depositOutPoint,
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         wire.MaxTxInSequenceNum,
	})
	spendPkScript, err := common.P2TRScriptFromPubKey(sspConfig.IdentityPrivateKey.Public())
	require.NoError(t, err)
	spendTx.AddTxOut(wire.NewTxOut(int64(quoteAmount), spendPkScript))

	// *********************************************************************************
	// Get signing commitments to use for frost signing
	// *********************************************************************************
	nodeIDs := make([]string, len(leavesToTransfer))
	for i, leaf := range leavesToTransfer {
		nodeIDs[i] = leaf.Leaf.Id
	}

	// *********************************************************************************
	// Claim Static Deposit
	// *********************************************************************************
	signedSpendTx, transfer, err := wallet.ClaimStaticDepositLegacy(
		sspCtx,
		sspConfig,
		common.Regtest,
		leavesToTransfer[:],
		spendTx,
		pb.UtxoSwapRequestType_Fixed,
		aliceDepositPrivKey,
		userSignature,
		sspSignature,
		aliceConfig.IdentityPrivateKey.Public(),
		sspConn,
		signedDepositTx.TxOut[vout],
	)
	require.NoError(t, err)

	config := sparktesting.TestConfig(t)
	ctx, dbCtx := db.NewTestContext(t, config.DatabaseDriver(), config.DatabasePath)

	schemaNetwork, err := common.SchemaNetworkFromProtoNetwork(pb.Network_REGTEST)
	require.NoError(t, err)

	depositTxID, err := hex.DecodeString(spendTx.TxIn[0].PreviousOutPoint.Hash.String())
	require.NoError(t, err)
	targetUtxo, err := dbCtx.Client.Utxo.Query().
		Where(utxo.NetworkEQ(schemaNetwork)).
		Where(utxo.Txid(depositTxID)).
		Where(utxo.Vout(depositOutPoint.Index)).
		Only(ctx)
	require.NoError(t, err)

	utxoSwap, err := dbCtx.Client.UtxoSwap.Query().Where(utxoswap.HasUtxoWith(utxo.IDEQ(targetUtxo.ID))).Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCompleted, utxoSwap.Status)
	dbTransferSspToAlice, err := utxoSwap.QueryTransfer().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderKeyTweaked, dbTransferSspToAlice.Status)

	_, err = common.SerializeTx(signedSpendTx)
	require.NoError(t, err)

	// Sign, broadcast, and mine spend tx
	_, err = bitcoinClient.SendRawTransaction(signedSpendTx, true)
	require.NoError(t, err)
	assert.Equal(t, pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, transfer.Status)

	// Claim transfer
	pendingTransfer, err := wallet.QueryPendingTransfers(aliceCtx, aliceConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	assert.Equal(t, pb.TransferType_UTXO_SWAP, receiverTransfer.Type)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	res, err := wallet.ClaimTransfer(aliceCtx, receiverTransfer, aliceConfig, leavesToClaim[:])
	require.NoError(t, err, "failed to ClaimTransfer")
	require.Equal(t, res[0].Id, transferNode.Leaf.Id)

	// *********************************************************************************
	// Claiming a Static Deposit again should return the same result
	// *********************************************************************************
	sparkClient := pb.NewSparkServiceClient(sspConn)
	depositTxID, err = hex.DecodeString(spendTx.TxIn[0].PreviousOutPoint.Hash.String())
	require.NoError(t, err)

	// Prepare a signing job for another spend tx, SSP should be able to make it sign by SE
	var spendTxBytes bytes.Buffer
	err = spendTx.Serialize(&spendTxBytes)
	require.NoError(t, err)
	hidingPriv := keys.GeneratePrivateKey()
	bindingPriv := keys.GeneratePrivateKey()
	hidingPubBytes := hidingPriv.Public().Serialize()
	bindingPubBytes := bindingPriv.Public().Serialize()
	spendTxNonceCommitment, err := objects.NewSigningCommitment(bindingPubBytes, hidingPubBytes)
	require.NoError(t, err)
	spendTxNonceCommitmentProto, err := spendTxNonceCommitment.MarshalProto()
	require.NoError(t, err)

	spendTxSigningJob := &pb.SigningJob{
		RawTx:                  spendTxBytes.Bytes(),
		SigningPublicKey:       aliceDepositPrivKey.Public().Serialize(),
		SigningNonceCommitment: spendTxNonceCommitmentProto,
	}

	swapResponse2, err := sparkClient.InitiateUtxoSwap(sspCtx, &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    depositTxID,
			Vout:    uint32(vout),
			Network: pb.Network_REGTEST,
		},
		RequestType:   pb.UtxoSwapRequestType_Fixed,
		Amount:        &pb.InitiateUtxoSwapRequest_CreditAmountSats{CreditAmountSats: quoteAmount},
		UserSignature: userSignature,
		SspSignature:  sspSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                transfer.Id,
			OwnerIdentityPublicKey:    sspConfig.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: aliceConfig.IdentityPublicKey().Serialize(),
			ExpiryTime:                nil,
			TransferPackage:           nil,
		},
		SpendTxSigningJob: spendTxSigningJob,
	})
	require.NoError(t, err)
	require.Equal(t, transfer.Id, swapResponse2.Transfer.Id)
	require.Equal(t, pb.TransferStatus_TRANSFER_STATUS_COMPLETED, swapResponse2.Transfer.Status)
	require.Equal(t, transfer.Leaves[0].Leaf.Id, swapResponse2.Transfer.Leaves[0].Leaf.Id)

	// *********************************************************************************
	// A call to rollback should fail
	// *********************************************************************************
	sparkInternalClient := pbinternal.NewSparkInternalServiceClient(sspConn)
	rollbackUtxoSwapRequestMessageHash, err := handler.CreateUtxoSwapStatement(
		handler.UtxoSwapStatementTypeRollback,
		hex.EncodeToString(depositOutPoint.Hash[:]),
		depositOutPoint.Index,
		common.Regtest,
	)
	require.NoError(t, err)
	rollbackUtxoSwapRequestSignature := ecdsa.Sign(sspConfig.IdentityPrivateKey.ToBTCEC(), rollbackUtxoSwapRequestMessageHash)

	_, err = sparkInternalClient.RollbackUtxoSwap(sspCtx, &pbinternal.RollbackUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    depositOutPoint.Hash[:],
			Vout:    depositOutPoint.Index,
			Network: pb.Network_REGTEST,
		},
		Signature:            rollbackUtxoSwapRequestSignature.Serialize(),
		CoordinatorPublicKey: aliceConfig.IdentityPublicKey().Serialize(),
	})
	require.Error(t, err)
}

func TestStaticDepositUserRefundLegacy(t *testing.T) {
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

	// Make sure the deposit tx gets enough confirmations
	// Confirm extra buffer to scan more blocks than needed
	// So that we don't race the chain watcher in this test
	_, err = bitcoinClient.GenerateToAddress(6, randomAddress, nil)
	require.NoError(t, err)
	time.Sleep(10000 * time.Millisecond)

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
		spendTxSighash[:],
		aliceConfig.IdentityPrivateKey,
	)

	// *********************************************************************************
	// Refund Static Deposit
	// *********************************************************************************
	signedSpendTx, err := wallet.RefundStaticDepositLegacy(
		aliceCtx,
		aliceConfig,
		common.Regtest,
		spendTx,
		aliceDepositPrivKey,
		userSignature,
		aliceConfig.IdentityPublicKey(),
		signedDepositTx.TxOut[vout],
		aliceConn,
	)
	require.NoError(t, err)

	spendTxBytes, err := common.SerializeTx(signedSpendTx)
	require.NoError(t, err)
	assert.NotEmpty(t, spendTxBytes)

	// Sign, broadcast, and mine spend tx
	txid, err := bitcoinClient.SendRawTransaction(signedSpendTx, true)
	require.NoError(t, err)
	assert.Len(t, txid, 32)

	// *********************************************************************************
	// Refunding a Static Deposit again should fail
	// *********************************************************************************
	_, err = wallet.RefundStaticDepositLegacy(
		aliceCtx,
		aliceConfig,
		common.Regtest,
		spendTx,
		aliceDepositPrivKey,
		userSignature,
		aliceConfig.IdentityPublicKey(),
		signedDepositTx.TxOut[vout],
		aliceConn,
	)
	require.Error(t, err)

	// *********************************************************************************
	// A call to rollback should fail
	// *********************************************************************************
	sparkInternalClient := pbinternal.NewSparkInternalServiceClient(aliceConn)
	rollbackUtxoSwapRequestMessageHash, err := handler.CreateUtxoSwapStatement(
		handler.UtxoSwapStatementTypeRollback,
		hex.EncodeToString(depositOutPoint.Hash[:]),
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

		userSignature := wallet.CreateUserSignature(
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
				UserSignature:           userSignature,
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
		pubkeyBytes, err := hex.DecodeString("0252f2cfa8d1f87718c0f3f61b581b7a3dce6bf9a14efd0a501d8969d6ace73a3d")
		require.NoError(t, err)
		withdrawalPubKey, err := keys.ParsePublicKey(pubkeyBytes)
		require.NoError(t, err)
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
			hex.EncodeToString(depositOutPoint.Hash[:]),
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
