package grpctest

import (
	"bytes"
	"context"
	"encoding/hex"
	"log"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
	"github.com/stretchr/testify/assert"
)

func TestGenerateDepositAddress(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	token, err := wallet.AuthenticateWithServer(context.Background(), config)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(context.Background(), token)

	pubkey, err := hex.DecodeString("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")
	if err != nil {
		t.Fatalf("failed to decode public key: %v", err)
	}

	resp, err := wallet.GenerateDepositAddress(ctx, config, pubkey, "")
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	if resp.DepositAddress.Address == "" {
		t.Fatalf("deposit address is empty")
	}

	unusedDepositAddresses, err := wallet.QueryUnusedDepositAddresses(ctx, config)
	if err != nil {
		t.Fatalf("failed to query unused deposit addresses: %v", err)
	}

	if len(unusedDepositAddresses.DepositAddresses) != 1 {
		t.Fatalf("expected 1 unused deposit address, got %d", len(unusedDepositAddresses.DepositAddresses))
	}

	if unusedDepositAddresses.DepositAddresses[0].DepositAddress != resp.DepositAddress.Address {
		t.Fatalf("expected unused deposit address to be %s, got %s", resp.DepositAddress.Address, unusedDepositAddresses.DepositAddresses[0])
	}

	if !bytes.Equal(unusedDepositAddresses.DepositAddresses[0].UserSigningPublicKey, pubkey) {
		t.Fatalf("expected user signing public key to be %s, got %s", hex.EncodeToString(pubkey), hex.EncodeToString(unusedDepositAddresses.DepositAddresses[0].UserSigningPublicKey))
	}

	if !bytes.Equal(unusedDepositAddresses.DepositAddresses[0].VerifyingPublicKey, resp.DepositAddress.VerifyingKey) {
		t.Fatalf("expected verifying public key to be %s, got %s", hex.EncodeToString(resp.DepositAddress.VerifyingKey), hex.EncodeToString(unusedDepositAddresses.DepositAddresses[0].VerifyingPublicKey))
	}
}

func TestStartDepositTreeCreation(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	conn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(context.Background(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(context.Background(), token)

	privKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	userPubKey := privKey.PubKey()
	userPubKeyBytes := userPubKey.SerializeCompressed()

	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKeyBytes, leafID)
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	client, err := testutil.NewRegtestClient()
	assert.NoError(t, err)

	coin, err := faucet.Fund()
	assert.NoError(t, err)

	depositTx, err := testutil.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
	if err != nil {
		t.Fatalf("failed to create deposit tx: %v", err)
	}
	vout := 0
	var buf bytes.Buffer
	err = depositTx.Serialize(&buf)
	if err != nil {
		t.Fatalf("failed to serialize deposit tx: %v", err)
	}
	depositTxHex := hex.EncodeToString(buf.Bytes())
	decodedBytes, err := hex.DecodeString(depositTxHex)
	if err != nil {
		t.Fatalf("failed to decode deposit tx hex: %v", err)
	}
	depositTx, err = common.TxFromRawTxBytes(decodedBytes)
	if err != nil {
		t.Fatalf("failed to deserilize deposit tx: %v", err)
	}

	// Sign, broadcast, and mine deposit tx
	signedDepositTx, err := testutil.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	assert.NoError(t, err)
	_, err = client.SendRawTransaction(signedDepositTx, true)
	assert.NoError(t, err)

	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	assert.NoError(t, err)
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	assert.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	resp, err := wallet.CreateTreeRoot(ctx, config, privKey.Serialize(), depositResp.DepositAddress.VerifyingKey, depositTx, vout)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}

	log.Printf("tree created: %v", resp)

	for _, node := range resp.Nodes {
		if node.Status == string(schema.TreeNodeStatusCreating) {
			t.Fatalf("tree node is in status TreeNodeStatusCreating %s", node.Id)
		}
		if node.Id != leafID {
			t.Fatalf("tree node id is not the expected leaf id %s", node.Id)
		}
	}

	unusedDepositAddresses, err := wallet.QueryUnusedDepositAddresses(ctx, config)
	if err != nil {
		t.Fatalf("failed to query unused deposit addresses: %v", err)
	}

	if len(unusedDepositAddresses.DepositAddresses) != 0 {
		t.Fatalf("expected 0 unused deposit addresses, got %d", len(unusedDepositAddresses.DepositAddresses))
	}
}

func TestStartDepositTreeCreationConcurrentWithSameTx(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	conn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(context.Background(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(context.Background(), token)

	privKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	userPubKey := privKey.PubKey()
	userPubKeyBytes := userPubKey.SerializeCompressed()

	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKeyBytes, "")
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	client, err := testutil.NewRegtestClient()
	assert.NoError(t, err)

	coin, err := faucet.Fund()
	assert.NoError(t, err)

	depositTx, err := testutil.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
	if err != nil {
		t.Fatalf("failed to create deposit tx: %v", err)
	}
	vout := 0
	var buf bytes.Buffer
	err = depositTx.Serialize(&buf)
	if err != nil {
		t.Fatalf("failed to serialize deposit tx: %v", err)
	}
	depositTxHex := hex.EncodeToString(buf.Bytes())
	decodedBytes, err := hex.DecodeString(depositTxHex)
	if err != nil {
		t.Fatalf("failed to decode deposit tx hex: %v", err)
	}
	depositTx, err = common.TxFromRawTxBytes(decodedBytes)
	if err != nil {
		t.Fatalf("failed to deserilize deposit tx: %v", err)
	}

	// Sign, broadcast, and mine deposit tx
	signedDepositTx, err := testutil.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	assert.NoError(t, err)
	_, err = client.SendRawTransaction(signedDepositTx, true)
	assert.NoError(t, err)

	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	assert.NoError(t, err)
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	assert.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	resultChannel := make(chan *spark.FinalizeNodeSignaturesResponse, 2)
	errChannel := make(chan error, 2)

	for range 2 {
		go func() {
			resp, err := wallet.CreateTreeRoot(ctx, config, privKey.Serialize(), depositResp.DepositAddress.VerifyingKey, depositTx, vout)

			if err != nil {
				errChannel <- err
			} else {
				resultChannel <- resp
			}
		}()
	}

	var resp *spark.FinalizeNodeSignaturesResponse
	respCount, errCount := 0, 0

	for range 2 {
		select {
		case r := <-resultChannel:
			resp = r
			respCount++
		case <-errChannel:
			errCount++
		}
	}

	assert.Equal(t, 1, respCount)
	assert.Equal(t, 1, errCount)

	log.Printf("tree created: %v", resp)

	for _, node := range resp.Nodes {
		if node.Status == string(schema.TreeNodeStatusCreating) {
			t.Fatalf("tree node is in status TreeNodeStatusCreating %s", node.Id)
		}
	}

	unusedDepositAddresses, err := wallet.QueryUnusedDepositAddresses(ctx, config)
	if err != nil {
		t.Fatalf("failed to query unused deposit addresses: %v", err)
	}

	if len(unusedDepositAddresses.DepositAddresses) != 0 {
		t.Fatalf("expected 0 unused deposit addresses, got %d", len(unusedDepositAddresses.DepositAddresses))
	}
}

// Test that we can get refund signatures for a tree before depositing funds on-chain,
// and that after we confirm funds on-chain, our leaves are available for transfer.
func TestStartDepositTreeCreationOffchain(t *testing.T) {
	client, err := testutil.NewRegtestClient()
	assert.NoError(t, err)

	coin, err := faucet.Fund()
	assert.NoError(t, err)

	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	// Setup Mock tx
	conn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(context.Background(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(context.Background(), token)

	privKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	userPubKey := privKey.PubKey()
	userPubKeyBytes := userPubKey.SerializeCompressed()

	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKeyBytes, "")
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	depositTx, err := testutil.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
	if err != nil {
		t.Fatalf("failed to create deposit tx: %v", err)
	}
	vout := 0
	var buf bytes.Buffer
	err = depositTx.Serialize(&buf)
	if err != nil {
		t.Fatalf("failed to serialize deposit tx: %v", err)
	}
	depositTxHex := hex.EncodeToString(buf.Bytes())
	decodedBytes, err := hex.DecodeString(depositTxHex)
	if err != nil {
		t.Fatalf("failed to decode deposit tx hex: %v", err)
	}
	depositTx, err = common.TxFromRawTxBytes(decodedBytes)
	if err != nil {
		t.Fatalf("failed to deserilize deposit tx: %v", err)
	}

	resp, err := wallet.CreateTreeRoot(ctx, config, privKey.Serialize(), depositResp.DepositAddress.VerifyingKey, depositTx, vout)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}

	log.Printf("tree created: %v", resp)

	// User should not be able to transfer funds since
	// L1 tx has not confirmed
	rootNode := resp.Nodes[0]
	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}

	receiverPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create receiver private key: %v", err)
	}

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    privKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}
	_, err = wallet.SendTransfer(
		context.Background(),
		config,
		leavesToTransfer[:],
		receiverPrivKey.PubKey().SerializeCompressed(),
		time.Now().Add(10*time.Minute),
	)
	if err == nil {
		t.Fatalf("expected error when sending transfer")
	}

	// Sign, broadcast, and mine deposit tx
	signedDepositTx, err := testutil.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	assert.NoError(t, err)
	_, err = client.SendRawTransaction(signedDepositTx, true)
	assert.NoError(t, err)

	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	assert.NoError(t, err)
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	assert.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// After L1 tx confirms, user should be able to transfer funds
	_, err = wallet.SendTransfer(
		context.Background(),
		config,
		leavesToTransfer[:],
		receiverPrivKey.PubKey().SerializeCompressed(),
		time.Now().Add(10*time.Minute),
	)
	if err != nil {
		t.Fatalf("failed to send transfer: %v", err)
	}
}
