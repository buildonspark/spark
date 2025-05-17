package grpctest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/handler"
	testutil "github.com/lightsparkdev/spark/test_util"
	"github.com/lightsparkdev/spark/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateUtxoIsNotSpent(t *testing.T) {
	bitcoinClient, err := testutil.NewRegtestClient()
	testutil.OnErrFatal(t, err)

	// Test with faucet transaction
	coin, err := faucet.Fund()
	testutil.OnErrFatal(t, err)
	txidString := hex.EncodeToString(coin.OutPoint.Hash[:])
	txIDBytes, err := hex.DecodeString(txidString)
	testutil.OnErrFatal(t, err)
	err = handler.ValidateUtxoIsNotSpent(bitcoinClient, txIDBytes, 0)
	if err != nil {
		t.Fatalf("utxo is spent: %v, txid: %s", err, txidString)
	}

	// Spend the faucet transaction and test with a new one
	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	assert.NoError(t, err)

	pkScript, err := txscript.PayToAddrScript(randomAddress)
	testutil.OnErrFatal(t, err)
	txOut := wire.NewTxOut(10_000, pkScript)
	unsignedDepositTx := testutil.CreateTestTransaction([]*wire.TxIn{wire.NewTxIn(coin.OutPoint, nil, [][]byte{})}, []*wire.TxOut{txOut})
	signedDepositTx, err := testutil.SignFaucetCoin(unsignedDepositTx, coin.TxOut, coin.Key)
	testutil.OnErrFatal(t, err)
	newTxID, err := bitcoinClient.SendRawTransaction(signedDepositTx, true)
	testutil.OnErrFatal(t, err)

	// Make sure the deposit tx gets enough confirmations
	randomKey, err = secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey = randomKey.PubKey()
	randomAddress, err = common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	assert.NoError(t, err)
	_, err = bitcoinClient.GenerateToAddress(1, randomAddress, nil)
	assert.NoError(t, err)

	// faucet coin is spent
	err = handler.ValidateUtxoIsNotSpent(bitcoinClient, txIDBytes, 0)
	assert.Error(t, err)

	// deposit tx is not spent
	err = handler.ValidateUtxoIsNotSpent(bitcoinClient, newTxID[:], 0)
	assert.NoError(t, err)
}

func TestStaticDeposit(t *testing.T) {
	bitcoinClient, err := testutil.NewRegtestClient()
	testutil.OnErrFatal(t, err)

	coin, err := faucet.Fund()
	testutil.OnErrFatal(t, err)

	// *********************************************************************************
	// Initiate Users
	// *********************************************************************************
	// 1. Initiate Alice
	aliceConfig, err := testutil.TestWalletConfig()
	testutil.OnErrFatal(t, err)

	aliceLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	testutil.OnErrFatal(t, err)
	_, err = testutil.CreateNewTree(aliceConfig, faucet, aliceLeafPrivKey, 100_000)
	testutil.OnErrFatal(t, err)

	aliceConn, err := common.NewGRPCConnectionWithTestTLS(aliceConfig.CoodinatorAddress(), nil)
	testutil.OnErrFatal(t, err)
	defer aliceConn.Close()

	aliceConnectionToken, err := wallet.AuthenticateWithConnection(context.Background(), aliceConfig, aliceConn)
	testutil.OnErrFatal(t, err)
	aliceCtx := wallet.ContextWithToken(context.Background(), aliceConnectionToken)

	// 2. Initiate SSP
	sspConfig, err := testutil.TestWalletConfig()
	testutil.OnErrFatal(t, err)

	sspLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	testutil.OnErrFatal(t, err)
	sspRootNode, err := testutil.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 90_000)
	testutil.OnErrFatal(t, err)

	sspConn, err := common.NewGRPCConnectionWithTestTLS(sspConfig.CoodinatorAddress(), nil)
	testutil.OnErrFatal(t, err)
	defer sspConn.Close()

	sspConnectionToken, err := wallet.AuthenticateWithConnection(context.Background(), sspConfig, sspConn)
	testutil.OnErrFatal(t, err)
	sspCtx := wallet.ContextWithToken(context.Background(), sspConnectionToken)

	// *********************************************************************************
	// Generate a new static deposit address for Alice
	// *********************************************************************************

	// Generate a new private key for Alice. In a real Wallet that key would be derived from
	// a Signing key using derivation schema
	aliceDepositPrivKey, err := secp256k1.GeneratePrivateKey()
	testutil.OnErrFatal(t, err)
	aliceDepositPubKey := aliceDepositPrivKey.PubKey()
	aliceDepositPubKeyBytes := aliceDepositPubKey.SerializeCompressed()

	leafID := uuid.New().String()

	depositResp, err := wallet.GenerateDepositAddress(
		aliceCtx,
		aliceConfig,
		aliceDepositPubKeyBytes,
		&leafID,
		true,
	)
	testutil.OnErrFatal(t, err)
	time.Sleep(100 * time.Millisecond)
	// *********************************************************************************
	// Create Test Deposit TX from Alice
	// *********************************************************************************
	depositAmount := uint64(100_000)
	quoteAmount := uint64(90_000)

	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	assert.NoError(t, err)

	unsignedDepositTx, err := testutil.CreateTestDepositTransactionManyOutputs(
		coin.OutPoint,
		[]string{randomAddress.String(), depositResp.DepositAddress.Address},
		int64(depositAmount),
	)
	testutil.OnErrFatal(t, err)
	vout := 1
	if unsignedDepositTx.TxOut[vout].Value != int64(depositAmount) {
		t.Fatalf("deposit tx output value is not equal to the deposit amount")
	}
	signedDepositTx, err := testutil.SignFaucetCoin(unsignedDepositTx, coin.TxOut, coin.Key)
	testutil.OnErrFatal(t, err)
	_, err = bitcoinClient.SendRawTransaction(signedDepositTx, true)
	testutil.OnErrFatal(t, err)

	// Make sure the deposit tx gets enough confirmations
	// Confirm extra buffer to scan more blocks than needed
	// So that we don't race the chain watcher in this test
	_, err = bitcoinClient.GenerateToAddress(6, randomAddress, nil)
	assert.NoError(t, err)
	time.Sleep(10000 * time.Millisecond)

	// *********************************************************************************
	// Create request signatures
	// *********************************************************************************
	// SSP signature committing to a fixed amount quote.
	// Can be obtained from a call for a quote to SSP.
	sspSignature, err := createSspFixedQuoteSignature(
		signedDepositTx.TxHash().String(),
		uint32(vout),
		common.Regtest,
		quoteAmount,
		&sspConfig.IdentityPrivateKey,
	)
	testutil.OnErrFatal(t, err)

	// User signature authorizing the SSP to claim the deposit
	// in return for a transfer of a fixed amount
	userSignature, err := createUserFixedQuoteSignature(
		signedDepositTx.TxHash().String(),
		uint32(vout),
		common.Regtest,
		quoteAmount,
		sspSignature,
		&aliceConfig.IdentityPrivateKey,
	)
	testutil.OnErrFatal(t, err)
	// *********************************************************************************
	// Create a Transfer from SSP to Alice
	// *********************************************************************************
	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	testutil.OnErrFatal(t, err)

	transferNode := wallet.LeafKeyTweak{
		Leaf:              sspRootNode,
		SigningPrivKey:    sspLeafPrivKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}
	// transfer, refundSignatureMap, _, err := wallet.SendTransferSignRefund(
	// 	sspCtx,
	// 	sspConfig,
	// 	leavesToTransfer[:],
	// 	aliceConfig.IdentityPublicKey(),
	// 	time.Now().Add(10*time.Minute),
	// )
	// testutil.OnErrFatal(t, err)

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
	spendPkScript, err := common.P2TRScriptFromPubKey(sspConfig.IdentityPrivateKey.PubKey())
	testutil.OnErrFatal(t, err)
	spendTx.AddTxOut(wire.NewTxOut(int64(quoteAmount), spendPkScript))

	// *********************************************************************************
	// Claim Static Deposit
	// *********************************************************************************
	signedSpendTx, transferToAliceKeysTweaked, err := wallet.ClaimStaticDeposit(
		sspCtx,
		sspConfig,
		common.Regtest,
		leavesToTransfer[:],
		spendTx,
		pb.UtxoSwapRequestType_Fixed,
		aliceDepositPrivKey,
		userSignature,
		sspSignature,
		aliceConfig.IdentityPrivateKey.PubKey(),
		sspConn,
		signedDepositTx.TxOut[vout],
	)
	testutil.OnErrFatal(t, err)

	_, err = common.SerializeTx(signedSpendTx)
	testutil.OnErrFatal(t, err)

	// Sign, broadcast, and mine spend tx
	_, err = bitcoinClient.SendRawTransaction(signedSpendTx, true)
	assert.NoError(t, err)

	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              transferToAliceKeysTweaked.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	res, err := wallet.ClaimTransfer(
		aliceCtx,
		transferToAliceKeysTweaked,
		aliceConfig,
		leavesToClaim[:],
	)
	require.NoError(t, err, "failed to ClaimTransfer")
	require.Equal(t, res[0].Id, transferNode.Leaf.Id)
}

func createUserFixedQuoteSignature(
	transactionID string,
	outputIndex uint32,
	network common.Network,
	creditAmountSats uint64,
	sspSignature []byte,
	identityPrivateKey *secp256k1.PrivateKey,
) ([]byte, error) {
	// Create a buffer to hold all the data
	var payload bytes.Buffer

	// Add action name
	_, err := payload.WriteString("claim_static_deposit")
	if err != nil {
		return nil, err
	}

	// Add network value as UTF-8 bytes
	_, err = payload.WriteString(network.String())
	if err != nil {
		return nil, err
	}

	// Add transaction ID as UTF-8 bytes
	_, err = payload.WriteString(transactionID)
	if err != nil {
		return nil, err
	}

	// Add output index as 4-byte unsigned integer (little-endian)
	err = binary.Write(&payload, binary.LittleEndian, outputIndex)
	if err != nil {
		return nil, err
	}

	// Request type fixed amount
	err = binary.Write(&payload, binary.LittleEndian, uint8(0))
	if err != nil {
		return nil, err
	}

	// Add credit amount as 8-byte unsigned integer (little-endian)
	err = binary.Write(&payload, binary.LittleEndian, uint64(creditAmountSats))
	if err != nil {
		return nil, err
	}

	// Add SSP signature as UTF-8 bytes
	_, err = payload.Write(sspSignature)
	if err != nil {
		return nil, err
	}

	// Hash the payload with SHA-256
	hash, err := handler.CreateUserFixedQuoteStatement(
		transactionID,
		outputIndex,
		network,
		creditAmountSats,
		sspSignature,
	)
	if err != nil {
		return nil, err
	}

	// Sign the hash of the payload using ECDSA
	signature := ecdsa.Sign(identityPrivateKey, hash[:])

	return signature.Serialize(), nil
}

func createSspFixedQuoteSignature(
	transactionID string,
	outputIndex uint32,
	network common.Network,
	creditAmountSats uint64,
	identityPrivateKey *secp256k1.PrivateKey,
) ([]byte, error) {
	// Create a buffer to hold all the data
	var payload bytes.Buffer

	// Add network value as UTF-8 bytes
	_, err := payload.WriteString(network.String())
	if err != nil {
		return nil, err
	}

	// Add transaction ID as UTF-8 bytes
	_, err = payload.WriteString(transactionID)
	if err != nil {
		return nil, err
	}

	// Add output index as 4-byte unsigned integer (little-endian)
	err = binary.Write(&payload, binary.LittleEndian, outputIndex)
	if err != nil {
		return nil, err
	}

	// Request type fixed amount
	err = binary.Write(&payload, binary.LittleEndian, uint8(0))
	if err != nil {
		return nil, err
	}

	// Add credit amount as 8-byte unsigned integer (little-endian)
	err = binary.Write(&payload, binary.LittleEndian, uint64(creditAmountSats))
	if err != nil {
		return nil, err
	}

	// Hash the payload with SHA-256
	hash := sha256.Sum256(payload.Bytes())

	// Sign the hash of the payload using ECDSA
	signature := ecdsa.Sign(identityPrivateKey, hash[:])

	return signature.Serialize(), nil
}
