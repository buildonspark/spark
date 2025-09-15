package grpctest

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so/dkg"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGenerateDepositAddress(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)
	token, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	pubKeyBytes, err := hex.DecodeString("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")
	require.NoError(t, err)
	pubKey, err := keys.ParsePublicKey(pubKeyBytes)
	require.NoError(t, err)

	leafID := uuid.New().String()
	resp, err := wallet.GenerateDepositAddress(ctx, config, pubKey, &leafID, false)
	require.NoError(t, err)
	require.NotEmpty(t, resp.DepositAddress.Address)
	assert.False(t, resp.DepositAddress.IsStatic)

	unusedDepositAddresses, err := wallet.QueryUnusedDepositAddresses(ctx, config)
	require.NoError(t, err)

	require.Len(t, unusedDepositAddresses.DepositAddresses, 1)
	unusedAddress := unusedDepositAddresses.DepositAddresses[0]
	require.Equal(t, resp.DepositAddress.Address, unusedAddress.DepositAddress)
	require.Equal(t, pubKeyBytes, unusedAddress.UserSigningPublicKey)
	require.Equal(t, resp.DepositAddress.VerifyingKey, unusedAddress.VerifyingPublicKey)
}

func TestGenerateDepositAddressWithoutCustomLeafID(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)
	token, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	pubKeyBytes, err := hex.DecodeString("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")
	require.NoError(t, err)
	pubKey, err := keys.ParsePublicKey(pubKeyBytes)
	require.NoError(t, err)

	invalidLeafID := "invalidLeafID"
	_, err = wallet.GenerateDepositAddress(ctx, config, pubKey, &invalidLeafID, false)
	require.Error(t, err, "expected error when generating deposit address with invalid leaf id")
	require.ErrorContains(t, err, "value must be a valid UUID")
}

func TestGenerateDepositAddressConcurrentRequests(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)
	token, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	pubKeyBytes, err := hex.DecodeString("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")
	require.NoError(t, err)
	pubKey, err := keys.ParsePublicKey(pubKeyBytes)
	require.NoError(t, err)

	wg := sync.WaitGroup{}
	resultChannel := make(chan string, 5)
	errChannel := make(chan error, 5)

	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			leafID := uuid.New().String()
			resp, err := wallet.GenerateDepositAddress(ctx, config, pubKey, &leafID, false)
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
	config := sparktesting.TestWalletConfig(t)
	token, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	pubKeyBytes, err := hex.DecodeString("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")
	require.NoError(t, err)
	pubKey, err := keys.ParsePublicKey(pubKeyBytes)
	require.NoError(t, err)
	resp, err := wallet.GenerateStaticDepositAddress(ctx, config, pubKey)
	require.NoError(t, err)
	assert.True(t, resp.DepositAddress.IsStatic)

	// Static deposit addresses should not be returned by QueryUnusedDepositAddresses
	unusedDepositAddresses, err := wallet.QueryUnusedDepositAddresses(ctx, config)
	require.NoError(t, err)
	assert.Empty(t, unusedDepositAddresses.DepositAddresses)

	queryStaticDepositAddresses, err := wallet.QueryStaticDepositAddresses(ctx, config, pubKey)
	require.NoError(t, err)
	assert.Len(t, queryStaticDepositAddresses.DepositAddresses, 1)
	assert.Equal(t, resp.DepositAddress.Address, queryStaticDepositAddresses.DepositAddresses[0].DepositAddress)

	// Generating a new static deposit address should return an error
	_, err = wallet.GenerateStaticDepositAddress(ctx, config, pubKey)
	require.ErrorContains(t, err, fmt.Sprintf("static deposit address already exists: %s", resp.DepositAddress.Address))

	// No new address should be created
	queryStaticDepositAddresses, err = wallet.QueryStaticDepositAddresses(ctx, config, pubKey)
	require.NoError(t, err)
	assert.Len(t, queryStaticDepositAddresses.DepositAddresses, 1)
	assert.Equal(t, resp.DepositAddress.Address, queryStaticDepositAddresses.DepositAddresses[0].DepositAddress)
}

func TestGenerateStaticDepositAddressDedicatedEndpoint(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)
	token, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	pubKeyBytes, err := hex.DecodeString("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")
	require.NoError(t, err)
	pubKey, err := keys.ParsePublicKey(pubKeyBytes)
	require.NoError(t, err)
	resp1, err := wallet.GenerateStaticDepositAddressDedicatedEndpoint(ctx, config, pubKey)
	require.NoError(t, err)
	require.Len(t, resp1.DepositAddress.DepositAddressProof.AddressSignatures, 5)

	// Static deposit addresses should not be returned by QueryUnusedDepositAddresses
	unusedDepositAddresses, err := wallet.QueryUnusedDepositAddresses(ctx, config)
	require.NoError(t, err)
	assert.Empty(t, unusedDepositAddresses.DepositAddresses)

	queryStaticDepositAddresses, err := wallet.QueryStaticDepositAddresses(ctx, config, pubKey)
	require.NoError(t, err)
	assert.Len(t, queryStaticDepositAddresses.DepositAddresses, 1)
	assert.Equal(t, resp1.DepositAddress.Address, queryStaticDepositAddresses.DepositAddresses[0].DepositAddress)

	// Generating a new static deposit address should not return an error
	resp2, err := wallet.GenerateStaticDepositAddressDedicatedEndpoint(ctx, config, pubKey)
	require.NoError(t, err)
	require.Equal(t, resp1.DepositAddress.Address, resp2.DepositAddress.Address)
	require.Len(t, resp2.DepositAddress.DepositAddressProof.AddressSignatures, 5)

	// No new address should be created
	queryStaticDepositAddresses, err = wallet.QueryStaticDepositAddresses(ctx, config, pubKey)
	require.NoError(t, err)
	assert.Len(t, queryStaticDepositAddresses.DepositAddresses, 1)
	assert.Equal(t, resp2.DepositAddress.Address, queryStaticDepositAddresses.DepositAddresses[0].DepositAddress)
}

func TestStartDepositTreeCreationBasic(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, privKey.Public(), &leafID, false)
	require.NoError(t, err)

	unusedDepositAddresses, err := wallet.QueryUnusedDepositAddresses(ctx, config)
	require.NoError(t, err)
	require.Len(t, unusedDepositAddresses.DepositAddresses, 1)
	require.Equal(t, leafID, *unusedDepositAddresses.DepositAddresses[0].LeafId)

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
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
	signedDepositTx, err := sparktesting.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	if err != nil {
		t.Fatalf("failed to sign faucet coin: %v", err)
	}
	require.NoError(t, err)
	_, err = client.SendRawTransaction(signedDepositTx, true)
	require.NoError(t, err)

	randomKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), common.Regtest)
	if err != nil {
		t.Fatalf("failed to get p2tr raw address: %v", err)
	}
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	if err != nil {
		t.Fatalf("failed to generate to address: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	verifyingKey, err := keys.ParsePublicKey(depositResp.DepositAddress.VerifyingKey)
	require.NoError(t, err)
	resp, err := wallet.CreateTreeRoot(ctx, config, privKey, verifyingKey, depositTx, vout, false)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}
	require.Len(t, resp.Nodes, 1)

	sparkClient := pb.NewSparkServiceClient(conn)
	rootNode, err := sparktesting.WaitForPendingDepositNode(ctx, sparkClient, resp.Nodes[0])
	require.NoError(t, err)
	assert.Equal(t, rootNode.Id, leafID)
	assert.Equal(t, rootNode.Status, string(st.TreeNodeStatusAvailable))

	unusedDepositAddresses, err = wallet.QueryUnusedDepositAddresses(ctx, config)
	if err != nil {
		t.Fatalf("failed to query unused deposit addresses: %v", err)
	}

	if len(unusedDepositAddresses.DepositAddresses) != 0 {
		t.Fatalf("expected 0 unused deposit addresses, got %d", len(unusedDepositAddresses.DepositAddresses))
	}
}

func TestStartDepositTreeCreationUnknownAddress(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	leafID := uuid.NewString()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, privKey.Public(), &leafID, false)
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

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
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
	signedDepositTx, err := sparktesting.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	if err != nil {
		t.Fatalf("failed to sign faucet coin: %v", err)
	}
	_, err = client.SendRawTransaction(signedDepositTx, true)
	require.NoError(t, err)

	randomKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), common.Regtest)
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

	verifyingKey, err := keys.ParsePublicKey(depositResp.DepositAddress.VerifyingKey)
	require.NoError(t, err)
	_, err = wallet.CreateTreeRoot(ctx, config, privKey, verifyingKey, depositTx, vout, false)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.NotFound, grpcStatus.Code())
	assert.Contains(t, grpcStatus.Message(), "The requested deposit address could not be found")
}

func TestStartDepositTreeCreationWithoutCustomLeafID(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}

	depositResp, err := wallet.GenerateDepositAddress(ctx, config, privKey.Public(), nil, false)
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
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
	signedDepositTx, err := sparktesting.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	if err != nil {
		t.Fatalf("failed to sign faucet coin: %v", err)
	}
	_, err = client.SendRawTransaction(signedDepositTx, true)
	require.NoError(t, err)

	randomKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), common.Regtest)
	if err != nil {
		t.Fatalf("failed to get p2tr raw address: %v", err)
	}
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	if err != nil {
		t.Fatalf("failed to generate to address: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	verifyingKey, err := keys.ParsePublicKey(depositResp.DepositAddress.VerifyingKey)
	require.NoError(t, err)
	resp, err := wallet.CreateTreeRoot(ctx, config, privKey, verifyingKey, depositTx, vout, false)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}

	for _, node := range resp.Nodes {
		_, err := uuid.Parse(node.Id)
		require.NoError(t, err)
	}
}

func TestStartDepositTreeCreationConcurrentWithSameTx(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}

	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, privKey.Public(), &leafID, false)
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
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
	signedDepositTx, err := sparktesting.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	if err != nil {
		t.Fatalf("failed to sign faucet coin: %v", err)
	}
	_, err = client.SendRawTransaction(signedDepositTx, true)
	require.NoError(t, err)

	randomKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), common.Regtest)
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

	verifyingKey, err := keys.ParsePublicKey(depositResp.DepositAddress.VerifyingKey)
	require.NoError(t, err)
	for range 2 {
		go func() {
			resp, err := wallet.CreateTreeRoot(ctx, config, privKey, verifyingKey, depositTx, vout, false)

			if err != nil {
				errChannel <- err
			} else {
				resultChannel <- resp
			}
		}()
	}

	var resp *pb.FinalizeNodeSignaturesResponse
	respCount, errCount := 0, 0
	treeNodeCounts := make(map[string]int)

	for range 2 {
		select {
		case r := <-resultChannel:
			resp = r
			respCount++
			for _, node := range r.Nodes {
				treeNodeCounts[node.Id]++
			}
		case e := <-errChannel:
			err = e
			errCount++
		}
	}

	// This test is nondeterministic. Either of two outcomes are possible:
	// 1. One call makes the tree and the other finds it to already exist
	// 2. One call attempts to make a duplicate tree and fails
	assert.GreaterOrEqual(t, respCount, 1)
	assert.LessOrEqual(t, errCount, 1)

	if err != nil {
		log.Print("one failed call encountered")
		grpcStatus, ok := status.FromError(err)
		assert.True(t, ok)
		// Second call can either land in between tree creation
		// and finalize node signatures, which yields Already Exists
		// error, or after both calls, which yields failed precondition
		assert.Contains(t, []codes.Code{codes.FailedPrecondition, codes.AlreadyExists}, grpcStatus.Code())
	} else {
		log.Print("both calls succeeded")
		var duplicateNodes []string
		for nodeId, count := range treeNodeCounts {
			if count != 2 {
				duplicateNodes = append(duplicateNodes, nodeId)
			}
		}
		assert.Emptyf(t, duplicateNodes, "expected same nodes to be returned across concurrent calls; found duplicate nodes %v", duplicateNodes)
	}

	log.Printf("tree created: %v", resp)

	for _, node := range resp.Nodes {
		if node.Status == string(st.TreeNodeStatusCreating) {
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
	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	config := sparktesting.TestWalletConfig(t)

	// Setup Mock tx
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, privKey.Public(), &leafID, false)
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
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

	verifyingKey, err := keys.ParsePublicKey(depositResp.DepositAddress.VerifyingKey)
	require.NoError(t, err)
	resp, err := wallet.CreateTreeRoot(ctx, config, privKey, verifyingKey, depositTx, vout, false)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}

	log.Printf("tree created: %v", resp)

	// User should not be able to transfer funds since
	// L1 tx has not confirmed
	rootNode := resp.Nodes[0]
	newLeafPrivKey, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}

	receiverPrivKey, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create receiver private key: %v", err)
	}

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    privKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}

	_, err = wallet.SendTransferWithKeyTweaks(
		t.Context(),
		config,
		leavesToTransfer[:],
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	if err == nil {
		t.Fatalf("expected error when sending transfer")
	}

	// Sign, broadcast, and mine deposit tx
	signedDepositTx, err := sparktesting.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	if err != nil {
		t.Fatalf("failed to sign faucet coin: %v", err)
	}
	_, err = client.SendRawTransaction(signedDepositTx, true)
	require.NoError(t, err)

	randomKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), common.Regtest)
	if err != nil {
		t.Fatalf("failed to get p2tr raw address: %v", err)
	}
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	if err != nil {
		t.Fatalf("failed to generate to address: %v", err)
	}

	_, err = sparktesting.WaitForPendingDepositNode(ctx, pb.NewSparkServiceClient(conn), rootNode)
	require.NoError(t, err)

	// After L1 tx confirms, user should be able to transfer funds
	_, err = wallet.SendTransferWithKeyTweaks(
		t.Context(),
		config,
		leavesToTransfer[:],
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	if err != nil {
		t.Fatalf("failed to send transfer: %v", err)
	}
}

// Test that we cannot transfer a leaf before a deposit has confirmed
func TestStartDepositTreeCreationUnconfirmed(t *testing.T) {
	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	config := sparktesting.TestWalletConfig(t)

	// Setup Mock tx
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, privKey.Public(), &leafID, false)
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
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

	verifyingKey, err := keys.ParsePublicKey(depositResp.DepositAddress.VerifyingKey)
	require.NoError(t, err)
	resp, err := wallet.CreateTreeRoot(ctx, config, privKey, verifyingKey, depositTx, vout, false)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}

	log.Printf("tree created: %v", resp)

	// User should not be able to transfer funds since
	// L1 tx has not confirmed
	rootNode := resp.Nodes[0]
	newLeafPrivKey, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}

	receiverPrivKey, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create receiver private key: %v", err)
	}

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    privKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := []wallet.LeafKeyTweak{transferNode}

	// Sign and broadcast TX but do not await confirmation
	signedDepositTx, err := sparktesting.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	require.NoError(t, err)
	_, err = client.SendRawTransaction(signedDepositTx, true)
	require.NoError(t, err)

	_, err = wallet.SendTransferWithKeyTweaks(
		t.Context(),
		config,
		leavesToTransfer[:],
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	assert.ErrorContains(t, err, "failed to start transfer")
}

func TestStartDepositTreeCreationIdempotency(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	userPubKey := privKey.Public()

	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKey, &leafID, false)
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

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
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
	signedDepositTx, err := sparktesting.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	if err != nil {
		t.Fatalf("failed to sign faucet coin: %v", err)
	}
	require.NoError(t, err)
	_, err = client.SendRawTransaction(signedDepositTx, true)
	require.NoError(t, err)

	randomKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), common.Regtest)
	if err != nil {
		t.Fatalf("failed to get p2tr raw address: %v", err)
	}
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	if err != nil {
		t.Fatalf("failed to generate to address: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	verifyingKey, err := keys.ParsePublicKey(depositResp.DepositAddress.VerifyingKey)
	require.NoError(t, err)
	// Call CreateTreeRoot twice in a row
	_, err = wallet.CreateTreeRoot(ctx, config, privKey, verifyingKey, depositTx, vout, true)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}

	resp, err := wallet.CreateTreeRoot(ctx, config, privKey, verifyingKey, depositTx, vout, false)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}
	require.Len(t, resp.Nodes, 1)

	sparkClient := pb.NewSparkServiceClient(conn)
	rootNode, err := sparktesting.WaitForPendingDepositNode(ctx, sparkClient, resp.Nodes[0])
	require.NoError(t, err)
	assert.Equal(t, rootNode.Id, leafID)
	assert.Equal(t, rootNode.Status, string(st.TreeNodeStatusAvailable))

	unusedDepositAddresses, err = wallet.QueryUnusedDepositAddresses(ctx, config)
	if err != nil {
		t.Fatalf("failed to query unused deposit addresses: %v", err)
	}

	if len(unusedDepositAddresses.DepositAddresses) != 0 {
		t.Fatalf("expected 0 unused deposit addresses, got %d", len(unusedDepositAddresses.DepositAddresses))
	}
}

func TestStartDepositTreeCreationDoubleClaim(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	userPubKey := privKey.Public()

	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKey, &leafID, false)
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

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
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
	signedDepositTx, err := sparktesting.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	if err != nil {
		t.Fatalf("failed to sign faucet coin: %v", err)
	}
	require.NoError(t, err)
	_, err = client.SendRawTransaction(signedDepositTx, true)
	require.NoError(t, err)

	randomKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	randomPubKey := randomKey.Public()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey, common.Regtest)
	if err != nil {
		t.Fatalf("failed to get p2tr raw address: %v", err)
	}
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	if err != nil {
		t.Fatalf("failed to generate to address: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	verifyingKey, err := keys.ParsePublicKey(depositResp.DepositAddress.VerifyingKey)
	require.NoError(t, err)
	resp, err := wallet.CreateTreeRoot(ctx, config, privKey, verifyingKey, depositTx, vout, false)
	require.NoError(t, err, "failed to create tree root")
	require.Len(t, resp.Nodes, 1)

	sparkClient := pb.NewSparkServiceClient(conn)
	rootNode, err := sparktesting.WaitForPendingDepositNode(ctx, sparkClient, resp.Nodes[0])
	require.NoError(t, err)
	assert.Equal(t, rootNode.Id, leafID)
	assert.Equal(t, rootNode.Status, string(st.TreeNodeStatusAvailable))

	unusedDepositAddresses, err = wallet.QueryUnusedDepositAddresses(ctx, config)
	require.NoError(t, err, "failed to query unused deposit addresses")
	require.Empty(t, unusedDepositAddresses.DepositAddresses, "expected no unused deposit addresses")

	_, err = wallet.CreateTreeRoot(ctx, config, privKey, verifyingKey, depositTx, vout, false)
	require.Error(t, err, "expected error upon double claim")
	stat, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.FailedPrecondition, stat.Code())
}

func TestStartDepositTreeCreationDepositCleanup(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)

	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	userPubKey := privKey.Public()

	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKey, &leafID, false)
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

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
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
	signedDepositTx, err := sparktesting.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	if err != nil {
		t.Fatalf("failed to sign faucet coin: %v", err)
	}
	require.NoError(t, err)
	_, err = client.SendRawTransaction(signedDepositTx, true)
	require.NoError(t, err)

	randomKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	randomPubKey := randomKey.Public()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey, common.Regtest)
	if err != nil {
		t.Fatalf("failed to get p2tr raw address: %v", err)
	}
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	if err != nil {
		t.Fatalf("failed to generate to address: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	verifyingKey, err := keys.ParsePublicKey(depositResp.DepositAddress.VerifyingKey)
	require.NoError(t, err)
	resp, err := wallet.CreateTreeRoot(ctx, config, privKey, verifyingKey, depositTx, vout, false)
	require.NoError(t, err, "failed to create tree root")
	require.Len(t, resp.Nodes, 1)

	sparkClient := pb.NewSparkServiceClient(conn)
	rootNode, err := sparktesting.WaitForPendingDepositNode(ctx, sparkClient, resp.Nodes[0])
	require.NoError(t, err)
	assert.Equal(t, rootNode.Id, leafID)
	assert.Equal(t, rootNode.Status, string(st.TreeNodeStatusAvailable))

	unusedDepositAddresses, err = wallet.QueryUnusedDepositAddresses(ctx, config)
	require.NoError(t, err, "failed to query unused deposit addresses")
	require.Empty(t, unusedDepositAddresses.DepositAddresses, "expected no unused deposit addresses")

	// Create SSP client and call DepositCleanup
	sspConn, err := config.NewCoordinatorGRPCConnection()
	require.NoError(t, err, "failed to connect to SSP")
	defer sspConn.Close()

	sspToken, err := wallet.AuthenticateWithConnection(t.Context(), config, sspConn)
	require.NoError(t, err, "failed to authenticate with SSP")
	sspCtx := wallet.ContextWithToken(t.Context(), sspToken)

	sparkSspInternalClient := pbssp.NewSparkSspInternalServiceClient(sspConn)
	txHash := depositTx.TxHash()
	_, err = sparkSspInternalClient.DepositCleanup(sspCtx, &pbssp.DepositCleanupRequest{
		Txid: txHash[:],
	})
	require.NoError(t, err, "failed to call DepositCleanup")

	_, err = wallet.CreateTreeRoot(ctx, config, privKey, verifyingKey, depositTx, vout, false)
	require.NoError(t, err, "failed to create tree root")
	require.Len(t, resp.Nodes, 1)
}

func TestQueryUnusedDepositAddresses(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)
	soConfig := sparktesting.TestConfig(t)

	err := dkg.GenerateKeys(t.Context(), soConfig, 500)
	if err != nil {
		t.Fatal(err)
	}

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 225; i++ {
		leafID := uuid.New().String()
		_, err := wallet.GenerateDepositAddress(ctx, config, privKey.Public(), &leafID, false)
		if err != nil {
			t.Fatalf("failed to generate deposit address %d: %v", i+1, err)
		}
	}

	unusedDepositAddresses, err := wallet.QueryUnusedDepositAddresses(ctx, config)
	if err != nil {
		t.Fatalf("failed to query unused deposit addresses: %v", err)
	}

	if len(unusedDepositAddresses.DepositAddresses) != 225 {
		t.Fatalf("expected 225 unused deposit addresses, got %d", len(unusedDepositAddresses.DepositAddresses))
	}
}

func TestQueryUnusedDepositAddressesBackwardsCompatibility(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)
	soConfig := sparktesting.TestConfig(t)

	err := dkg.GenerateKeys(t.Context(), soConfig, 500)
	if err != nil {
		t.Fatal(err)
	}

	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(t.Context(), token)
	privKey, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}

	for i := range 225 {
		leafID := uuid.NewString()
		_, err := wallet.GenerateDepositAddress(ctx, config, privKey.Public(), &leafID, false)
		if err != nil {
			t.Fatalf("failed to generate deposit address %d: %v", i+1, err)
		}
	}

	unusedDepositAddresses, err := wallet.QueryUnusedDepositAddresses(ctx, config)
	if err != nil {
		t.Fatalf("failed to query unused deposit addresses: %v", err)
	}

	if len(unusedDepositAddresses.DepositAddresses) != 225 {
		t.Fatalf("expected 225 unused deposit addresses, got %d", len(unusedDepositAddresses.DepositAddresses))
	}
}
