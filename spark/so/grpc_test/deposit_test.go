package grpctest

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent/schema"
	testutil "github.com/lightsparkdev/spark/test_util"
	"github.com/lightsparkdev/spark/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

	leafID := uuid.New().String()
	resp, err := wallet.GenerateDepositAddress(ctx, config, pubkey, &leafID, false)
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	if resp.DepositAddress.Address == "" {
		t.Fatalf("deposit address is empty")
	}

	assert.False(t, resp.DepositAddress.IsStatic)

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

func TestGenerateDepositAddressWithoutCustomLeafID(t *testing.T) {
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

	invalidLeafID := "invalidLeafID"
	_, err = wallet.GenerateDepositAddress(ctx, config, pubkey, &invalidLeafID, false)
	require.Error(t, err, "expected error when generating deposit address with invalid leaf id")
	require.Contains(t, err.Error(), "value must be a valid UUID")
}

func TestGenerateDepositAddressConcurrentRequests(t *testing.T) {
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

	wg := sync.WaitGroup{}
	resultChannel := make(chan string, 5)
	errChannel := make(chan error, 5)

	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			leafID := uuid.New().String()
			resp, err := wallet.GenerateDepositAddress(ctx, config, pubkey, &leafID, false)
			if err != nil {
				errChannel <- err
				return
			}
			if resp.DepositAddress.Address == "" {
				errChannel <- fmt.Errorf("deposit address is empty")
				return
			}

			resultChannel <- resp.DepositAddress.Address
		}()
	}

	wg.Wait()

	addresses := make(map[string]bool)
	for range 5 {
		select {
		case err := <-errChannel:
			t.Errorf("failed to generate deposit address: %v", err)
		case resp := <-resultChannel:
			if _, found := addresses[resp]; found {
				t.Errorf("duplicate deposit address generated: %s", resp)
			}
			addresses[resp] = true
		}
	}
}

func TestGenerateStaticDepositAddress(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	token, err := wallet.AuthenticateWithServer(context.Background(), config)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(context.Background(), token)

	pubkey, err := hex.DecodeString("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c8")
	assert.NoError(t, err)
	resp, err := wallet.GenerateStaticDepositAddress(ctx, config, pubkey)
	assert.NoError(t, err)
	assert.True(t, resp.DepositAddress.IsStatic)

	// Static deposit addresses should not be returned by QueryUnusedDepositAddresses
	unusedDepositAddresses, err := wallet.QueryUnusedDepositAddresses(ctx, config)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(unusedDepositAddresses.DepositAddresses))
}

func TestStartDepositTreeCreationBasic(t *testing.T) {
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
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKeyBytes, &leafID, false)
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	unusedDepositAddresses, err := wallet.QueryUnusedDepositAddresses(ctx, config)
	if err != nil {
		t.Fatalf("failed to query unused deposit addresses: %v", err)
	}

	if len(unusedDepositAddresses.DepositAddresses) != 1 {
		t.Fatalf("expected 1 unused deposit address, got %d", len(unusedDepositAddresses.DepositAddresses))
	}

	if *unusedDepositAddresses.DepositAddresses[0].LeafId != leafID {
		t.Fatalf("expected leaf id to be %s, got %s", leafID, *unusedDepositAddresses.DepositAddresses[0].LeafId)
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
	if err != nil {
		t.Fatalf("failed to sign faucet coin: %v", err)
	}
	assert.NoError(t, err)
	_, err = client.SendRawTransaction(signedDepositTx, true)
	assert.NoError(t, err)

	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	if err != nil {
		t.Fatalf("failed to get p2tr raw address: %v", err)
	}
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	if err != nil {
		t.Fatalf("failed to generate to address: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	resp, err := wallet.CreateTreeRoot(ctx, config, privKey.Serialize(), depositResp.DepositAddress.VerifyingKey, depositTx, vout)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}
	require.Equal(t, 1, len(resp.Nodes))

	sparkClient := pb.NewSparkServiceClient(conn)
	rootNode, err := testutil.WaitForPendingDepositNode(ctx, sparkClient, resp.Nodes[0])
	assert.NoError(t, err)
	assert.Equal(t, rootNode.Id, leafID)
	assert.Equal(t, rootNode.Status, string(schema.TreeNodeStatusAvailable))

	unusedDepositAddresses, err = wallet.QueryUnusedDepositAddresses(ctx, config)
	if err != nil {
		t.Fatalf("failed to query unused deposit addresses: %v", err)
	}

	if len(unusedDepositAddresses.DepositAddresses) != 0 {
		t.Fatalf("expected 0 unused deposit addresses, got %d", len(unusedDepositAddresses.DepositAddresses))
	}
}

func TestStartDepositTreeCreationUnknownAddress(t *testing.T) {
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
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKeyBytes, &leafID, false)
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	unusedDepositAddresses, err := wallet.QueryUnusedDepositAddresses(ctx, config)
	if err != nil {
		t.Fatalf("failed to query unused deposit addresses: %v", err)
	}

	if len(unusedDepositAddresses.DepositAddresses) != 1 {
		t.Fatalf("expected 1 unused deposit address, got %d", len(unusedDepositAddresses.DepositAddresses))
	}

	if *unusedDepositAddresses.DepositAddresses[0].LeafId != leafID {
		t.Fatalf("expected leaf id to be %s, got %s", leafID, *unusedDepositAddresses.DepositAddresses[0].LeafId)
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
	if err != nil {
		t.Fatalf("failed to sign faucet coin: %v", err)
	}
	_, err = client.SendRawTransaction(signedDepositTx, true)
	assert.NoError(t, err)

	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	if err != nil {
		t.Fatalf("failed to get p2tr raw address: %v", err)
	}
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	if err != nil {
		t.Fatalf("failed to generate to address: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// flip a bit in the pk script to simulate an unknown address
	depositTx.TxOut[0].PkScript[30] = depositTx.TxOut[0].PkScript[30] ^ 1

	_, err = wallet.CreateTreeRoot(ctx, config, privKey.Serialize(), depositResp.DepositAddress.VerifyingKey, depositTx, vout)
	assert.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.NotFound, grpcStatus.Code())
	assert.Contains(t, grpcStatus.Message(), "The requested deposit address could not be found")
}

func TestStartDepositTreeCreationWithoutCustomLeafID(t *testing.T) {
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

	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKeyBytes, nil, false)
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
	if err != nil {
		t.Fatalf("failed to sign faucet coin: %v", err)
	}
	_, err = client.SendRawTransaction(signedDepositTx, true)
	assert.NoError(t, err)

	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	if err != nil {
		t.Fatalf("failed to get p2tr raw address: %v", err)
	}
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	if err != nil {
		t.Fatalf("failed to generate to address: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	resp, err := wallet.CreateTreeRoot(ctx, config, privKey.Serialize(), depositResp.DepositAddress.VerifyingKey, depositTx, vout)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}

	for _, node := range resp.Nodes {
		_, err := uuid.Parse(node.Id)
		require.NoError(t, err)
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

	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKeyBytes, &leafID, false)
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
	if err != nil {
		t.Fatalf("failed to sign faucet coin: %v", err)
	}
	_, err = client.SendRawTransaction(signedDepositTx, true)
	assert.NoError(t, err)

	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	if err != nil {
		t.Fatalf("failed to get p2tr raw address: %v", err)
	}
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	if err != nil {
		t.Fatalf("failed to generate to address: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	resultChannel := make(chan *pb.FinalizeNodeSignaturesResponse, 2)
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

	var resp *pb.FinalizeNodeSignaturesResponse
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

	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKeyBytes, &leafID, false)
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
	if err != nil {
		t.Fatalf("failed to sign faucet coin: %v", err)
	}
	_, err = client.SendRawTransaction(signedDepositTx, true)
	assert.NoError(t, err)

	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	if err != nil {
		t.Fatalf("failed to get p2tr raw address: %v", err)
	}
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	if err != nil {
		t.Fatalf("failed to generate to address: %v", err)
	}

	sparkClient := pb.NewSparkServiceClient(conn)
	_, err = testutil.WaitForPendingDepositNode(ctx, sparkClient, rootNode)
	assert.NoError(t, err)

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
