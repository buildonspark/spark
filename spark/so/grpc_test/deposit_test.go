package grpctest

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/handler"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGenerateDepositAddress(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	token, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)
	pubKey := keys.MustParsePublicKeyHex("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")

	leafID := uuid.NewString()
	resp, err := wallet.GenerateDepositAddress(ctx, config, pubKey, &leafID, false)
	require.NoError(t, err)
	assert.False(t, resp.DepositAddress.IsStatic)

	unusedDepositAddresses, err := wallet.QueryUnusedDepositAddresses(ctx, config)
	require.NoError(t, err)

	require.Len(t, unusedDepositAddresses.DepositAddresses, 1)
	unusedAddress := unusedDepositAddresses.DepositAddresses[0]
	assert.Equal(t, resp.DepositAddress.Address, unusedAddress.DepositAddress)
	assert.Equal(t, pubKey.Serialize(), unusedAddress.UserSigningPublicKey)
	assert.Equal(t, resp.DepositAddress.VerifyingKey, unusedAddress.VerifyingPublicKey)
}

func TestGenerateDepositAddressWithoutCustomLeafID(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	token, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)
	pubKey := keys.MustParsePublicKeyHex("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")

	invalidLeafID := "invalidLeafID"
	_, err = wallet.GenerateDepositAddress(ctx, config, pubKey, &invalidLeafID, false)
	require.ErrorContains(t, err, "value must be a valid UUID")
}

func TestGenerateDepositAddressConcurrentRequests(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	token, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)
	pubKey := keys.MustParsePublicKeyHex("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")

	wg := sync.WaitGroup{}
	resultChannel := make(chan string, 5)
	errChannel := make(chan error, 5)

	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			leafID := uuid.NewString()
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
	config := wallet.NewTestWalletConfig(t)
	token, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)
	pubKey := keys.MustParsePublicKeyHex("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")
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

	// Generating a new static deposit address should not return an error
	resp2, err := wallet.GenerateStaticDepositAddress(ctx, config, pubKey)
	require.NoError(t, err)
	require.Equal(t, resp.DepositAddress.Address, resp2.DepositAddress.Address)
	require.Len(t, resp2.DepositAddress.DepositAddressProof.AddressSignatures, len(config.SigningOperators))

	// No new address should be created
	queryStaticDepositAddresses, err = wallet.QueryStaticDepositAddresses(ctx, config, pubKey)
	require.NoError(t, err)
	assert.Len(t, queryStaticDepositAddresses.DepositAddresses, 1)
	assert.Equal(t, resp.DepositAddress.Address, queryStaticDepositAddresses.DepositAddresses[0].DepositAddress)
}

func TestGenerateStaticDepositAddressDedicatedEndpoint(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	token, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	pubKey := keys.MustParsePublicKeyHex("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")
	require.NoError(t, err)
	resp1, err := wallet.GenerateStaticDepositAddress(ctx, config, pubKey)
	require.NoError(t, err)
	require.Len(t, resp1.DepositAddress.DepositAddressProof.AddressSignatures, len(config.SigningOperators))

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
	require.Len(t, resp2.DepositAddress.DepositAddressProof.AddressSignatures, len(config.SigningOperators))

	// No new address should be created
	queryStaticDepositAddresses, err = wallet.QueryStaticDepositAddresses(ctx, config, pubKey)
	require.NoError(t, err)
	assert.Len(t, queryStaticDepositAddresses.DepositAddresses, 1)
	assert.Equal(t, resp2.DepositAddress.Address, queryStaticDepositAddresses.DepositAddresses[0].DepositAddress)
}

func TestStartDepositTreeCreationBasic(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey := keys.GeneratePrivateKey()
	leafID := uuid.NewString()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, privKey.Public(), &leafID, false)
	require.NoError(t, err)

	unusedDepositAddresses, err := wallet.QueryUnusedDepositAddresses(ctx, config)
	require.NoError(t, err)
	require.Len(t, unusedDepositAddresses.DepositAddresses, 1)
	require.Equal(t, leafID, unusedDepositAddresses.DepositAddresses[0].GetLeafId())

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
	require.NoError(t, err, "failed to create deposit tx")
	vout := 0

	// Sign, broadcast, and mine deposit tx
	signedDepositTx, err := sparktesting.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	require.NoError(t, err, "failed to sign faucet coin")
	_, err = client.SendRawTransaction(signedDepositTx, true)
	require.NoError(t, err)

	randomKey := keys.GeneratePrivateKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), btcnetwork.Regtest)
	require.NoError(t, err, "failed to get p2tr raw address")
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err, "failed to generate to address")

	time.Sleep(100 * time.Millisecond)

	verifyingKey, err := keys.ParsePublicKey(depositResp.DepositAddress.VerifyingKey)
	require.NoError(t, err)
	resp, err := wallet.CreateTreeRoot(ctx, config, privKey, verifyingKey, depositTx, vout, false)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}
	require.Len(t, resp.Nodes, 1)

	sparkClient := pb.NewSparkServiceClient(conn)
	rootNode, err := wallet.WaitForPendingDepositNode(ctx, sparkClient, resp.Nodes[0])
	require.NoError(t, err)
	assert.Equal(t, rootNode.Id, leafID)
	assert.Equal(t, rootNode.Status, string(st.TreeNodeStatusAvailable))

	unusedDepositAddresses, err = wallet.QueryUnusedDepositAddresses(ctx, config)
	require.NoError(t, err, "failed to query unused deposit addresses")

	assert.Zero(t, unusedDepositAddresses.GetDepositAddresses())
}

func TestFinalizeDepositTreeCreationBasic(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey := keys.GeneratePrivateKey()
	leafID := uuid.NewString()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, privKey.Public(), &leafID, false)
	require.NoError(t, err)

	unusedDepositAddresses, err := wallet.QueryUnusedDepositAddresses(ctx, config)
	require.NoError(t, err)
	require.Len(t, unusedDepositAddresses.DepositAddresses, 1)
	require.Equal(t, leafID, unusedDepositAddresses.DepositAddresses[0].GetLeafId())

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)

	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
	require.NoError(t, err, "failed to create deposit tx")
	vout := 0

	// Sign, broadcast, and mine deposit tx
	signedDepositTx, err := sparktesting.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	require.NoError(t, err, "failed to sign faucet coin")
	_, err = client.SendRawTransaction(signedDepositTx, true)
	require.NoError(t, err)

	randomKey := keys.GeneratePrivateKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), btcnetwork.Regtest)
	require.NoError(t, err, "failed to get p2tr raw address")
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err, "failed to generate to address")

	time.Sleep(100 * time.Millisecond)

	verifyingKey, err := keys.ParsePublicKey(depositResp.DepositAddress.VerifyingKey)
	require.NoError(t, err)
	resp, err := wallet.CreateTreeRootWithFinalizeDepositTreeCreation(ctx, config, privKey, verifyingKey, depositTx, vout)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}

	require.NotNil(t, resp.RootNode)

	require.Nil(t, resp.RootNode.ParentNodeId, "must be a root node")
	require.Equal(t, resp.RootNode.Id, leafID)
	require.Equal(t, uint64(100_000), resp.RootNode.Value)
	require.Equal(t, resp.RootNode.Status, string(st.TreeNodeStatusCreating))

	tx, err := common.TxFromRawTxBytes(resp.RootNode.NodeTx)
	require.NoError(t, err)
	require.Len(t, tx.TxIn, 1)
	require.NotNil(t, tx.TxIn[0])
	require.Len(t, tx.TxIn[0].Witness, 1)
	require.Len(t, tx.TxOut, 2)

	refundTx, err := common.TxFromRawTxBytes(resp.RootNode.RefundTx)
	require.NoError(t, err)
	require.Len(t, refundTx.TxIn, 1)
	require.NotNil(t, refundTx.TxIn[0])
	require.Len(t, refundTx.TxIn[0].Witness, 1)
	require.Len(t, refundTx.TxOut, 2)

	directFromCpfpRefundTx, err := common.TxFromRawTxBytes(resp.RootNode.DirectFromCpfpRefundTx)
	require.NoError(t, err)
	require.Len(t, directFromCpfpRefundTx.TxIn, 1)
	require.NotNil(t, directFromCpfpRefundTx.TxIn[0])
	require.Len(t, directFromCpfpRefundTx.TxIn[0].Witness, 1)
	require.Len(t, directFromCpfpRefundTx.TxOut, 1)

	// Mine 2 more blocks because deposits won't be available until there are 3 confirmations
	_, err = client.GenerateToAddress(2, randomAddress, nil)
	require.NoError(t, err, "failed to generate to address")

	sparkClient := pb.NewSparkServiceClient(conn)
	rootNode, err := wallet.WaitForPendingDepositNode(ctx, sparkClient, resp.RootNode)
	require.NoError(t, err)
	assert.Equal(t, rootNode.Id, leafID)
	assert.Equal(t, rootNode.Status, string(st.TreeNodeStatusAvailable))

	unusedDepositAddresses, err = wallet.QueryUnusedDepositAddresses(ctx, config)
	require.NoError(t, err, "failed to query unused deposit addresses")

	assert.Zero(t, unusedDepositAddresses.GetDepositAddresses())
}

func TestStartDepositTreeCreationUnknownAddress(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)

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

	privKey := keys.GeneratePrivateKey()

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

	randomKey := keys.GeneratePrivateKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), btcnetwork.Regtest)
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
	assert.Contains(t, grpcStatus.Message(), "the requested deposit address could not be found")
}

func TestStartDepositTreeCreationWithoutCustomLeafID(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)

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

	privKey := keys.GeneratePrivateKey()
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

	randomKey := keys.GeneratePrivateKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), btcnetwork.Regtest)
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
		require.NoError(t, uuid.Validate(node.Id))
	}
}

func TestStartDepositTreeCreationConcurrentWithSameTx(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)

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

	privKey := keys.GeneratePrivateKey()
	leafID := uuid.NewString()
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

	randomKey := keys.GeneratePrivateKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), btcnetwork.Regtest)
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

	config := wallet.NewTestWalletConfig(t)

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

	privKey := keys.GeneratePrivateKey()
	leafID := uuid.NewString()
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
	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    privKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := []wallet.LeafKeyTweak{transferNode}

	_, err = wallet.SendTransferWithKeyTweaks(
		t.Context(),
		config,
		leavesToTransfer,
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

	randomKey := keys.GeneratePrivateKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), btcnetwork.Regtest)
	if err != nil {
		t.Fatalf("failed to get p2tr raw address: %v", err)
	}
	_, err = client.GenerateToAddress(3, randomAddress, nil)
	if err != nil {
		t.Fatalf("failed to generate to address: %v", err)
	}

	_, err = wallet.WaitForPendingDepositNode(ctx, pb.NewSparkServiceClient(conn), rootNode)
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

	config := wallet.NewTestWalletConfig(t)

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

	privKey := keys.GeneratePrivateKey()
	leafID := uuid.NewString()
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
	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()

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
		leavesToTransfer,
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	assert.ErrorContains(t, err, "failed to start transfer")
}

func TestStartDepositTreeCreationIdempotency(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)

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

	privKey := keys.GeneratePrivateKey()
	userPubKey := privKey.Public()

	leafID := uuid.NewString()
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

	randomKey := keys.GeneratePrivateKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), btcnetwork.Regtest)
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
	rootNode, err := wallet.WaitForPendingDepositNode(ctx, sparkClient, resp.Nodes[0])
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
	config := wallet.NewTestWalletConfig(t)

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

	privKey := keys.GeneratePrivateKey()
	userPubKey := privKey.Public()

	leafID := uuid.NewString()
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

	if unusedDepositAddresses.DepositAddresses[0].GetLeafId() != leafID {
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

	randomKey := keys.GeneratePrivateKey()
	randomPubKey := randomKey.Public()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey, btcnetwork.Regtest)
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
	rootNode, err := wallet.WaitForPendingDepositNode(ctx, sparkClient, resp.Nodes[0])
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

func TestQueryUnusedDepositAddresses(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
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

	privKey := keys.GeneratePrivateKey()

	for i := range handler.DefaultMaxUnusedDepositAddresses {
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

	if len(unusedDepositAddresses.DepositAddresses) != handler.DefaultMaxUnusedDepositAddresses {
		t.Fatalf("expected %d unused deposit addresses, got %d", handler.DefaultMaxUnusedDepositAddresses, len(unusedDepositAddresses.DepositAddresses))
	}
}

func TestQueryUnusedDepositAddressesBackwardsCompatibility(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)

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
	privKey := keys.GeneratePrivateKey()
	for i := range handler.DefaultMaxUnusedDepositAddresses {
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

	if len(unusedDepositAddresses.DepositAddresses) != handler.DefaultMaxUnusedDepositAddresses {
		t.Fatalf("expected %d unused deposit addresses, got %d", handler.DefaultMaxUnusedDepositAddresses, len(unusedDepositAddresses.DepositAddresses))
	}
}

func TestStartDepositTreeCreationWithDirectFromCpfpRefundAlongsideRegularRefund(t *testing.T) {
	// --- setup ---

	config := wallet.NewTestWalletConfig(t)
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey := keys.GeneratePrivateKey()
	leafID := uuid.NewString()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, privKey.Public(), &leafID, false)
	require.NoError(t, err)

	client := sparktesting.GetBitcoinClient()
	coin, err := faucet.Fund()
	require.NoError(t, err)

	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
	require.NoError(t, err)
	vout := 0
	var depositTxSerial bytes.Buffer
	err = depositTx.Serialize(&depositTxSerial)
	require.NoError(t, err)

	// // Sign, broadcast, and mine the deposit tx.
	// signedDepositTx, err := sparktesting.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	// require.NoError(t, err)
	// _, err = client.SendRawTransaction(signedDepositTx, true)
	// require.NoError(t, err)

	randomKey := keys.GeneratePrivateKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), btcnetwork.Regtest)
	require.NoError(t, err)
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// --- test ---
	//
	// Test that tree creation works even when the direct from CPFP refund transaction
	// is the only direct transaction passed.

	sparkClient := pb.NewSparkServiceClient(conn)

	// Create root transaction (required) - this acts as the "node tx"
	rootTx := wire.NewMsgTx(3)
	txIn := wire.NewTxIn(&wire.OutPoint{Hash: depositTx.TxHash(), Index: uint32(vout)}, nil, nil)
	txIn.Sequence = spark.ZeroSequence
	rootTx.AddTxIn(txIn)
	rootTx.AddTxOut(wire.NewTxOut(depositTx.TxOut[0].Value, depositTx.TxOut[0].PkScript))
	rootTx.AddTxOut(common.EphemeralAnchorOutput())

	initialRefundSequence, initialDirectSequence := wallet.InitialRefundSequences()

	refundTx, directFromCpfpRefundTx, err := wallet.CreateRefundTxs(
		initialRefundSequence,
		initialDirectSequence,
		&wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		rootTx.TxOut[0].Value,
		privKey.Public(),
		true,
	)
	require.NoError(t, err)

	_, err = sparkClient.StartDepositTreeCreation(ctx, &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: config.IdentityPublicKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			Vout:    uint32(vout),
			RawTx:   depositTxSerial.Bytes(),
			Network: config.ProtoNetwork(),
		},
		RootTxSigningJob:                 signingJobFromTx(t, privKey.Public(), rootTx),
		RefundTxSigningJob:               signingJobFromTx(t, privKey.Public(), refundTx),
		DirectFromCpfpRefundTxSigningJob: signingJobFromTx(t, privKey.Public(), directFromCpfpRefundTx),
	})

	require.NoError(t, err, "Expected StartDepositTreeCreation to succeed with direct from CPFP refund transaction alongside regular refund transaction")
}

func TestStartDepositTreeCreationDirectTxValidation(t *testing.T) {
	t.Skip("the feature being tested is disabled by a knob, so this test wouldn't pass")

	// Setup
	config := wallet.NewTestWalletConfig(t)
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey := keys.GeneratePrivateKey()
	leafID := uuid.NewString()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, privKey.Public(), &leafID, false)
	require.NoError(t, err)

	client := sparktesting.GetBitcoinClient()
	coin, err := faucet.Fund()
	require.NoError(t, err)

	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 100_000)
	require.NoError(t, err)
	vout := 0
	var depositTxSerial bytes.Buffer
	err = depositTx.Serialize(&depositTxSerial)
	require.NoError(t, err)

	// Generate a block to ensure the deposit tx is confirmed
	randomKey := keys.GeneratePrivateKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), btcnetwork.Regtest)
	require.NoError(t, err)
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Create test transactions
	rootTx := wire.NewMsgTx(3)
	txIn := wire.NewTxIn(&wire.OutPoint{Hash: depositTx.TxHash(), Index: uint32(vout)}, nil, nil)
	txIn.Sequence = spark.ZeroSequence
	rootTx.AddTxIn(txIn)
	rootTx.AddTxOut(wire.NewTxOut(depositTx.TxOut[0].Value, depositTx.TxOut[0].PkScript))
	rootTx.AddTxOut(common.EphemeralAnchorOutput())

	initialRefundSequence, initialDirectSequence := wallet.InitialRefundSequences()

	refundTx, directFromCpfpRefundTx, err := wallet.CreateRefundTxs(
		initialRefundSequence,
		initialDirectSequence,
		&wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		rootTx.TxOut[0].Value,
		privKey.Public(),
		true,
	)
	require.NoError(t, err)

	// Each test case uses a variation of this base request
	baseRequest := &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: config.IdentityPublicKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			Vout:    uint32(vout),
			RawTx:   depositTxSerial.Bytes(),
			Network: config.ProtoNetwork(),
		},
		RootTxSigningJob:                 signingJobFromTx(t, privKey.Public(), rootTx),
		RefundTxSigningJob:               signingJobFromTx(t, privKey.Public(), refundTx),
		DirectFromCpfpRefundTxSigningJob: signingJobFromTx(t, privKey.Public(), directFromCpfpRefundTx),
	}

	sparkClient := pb.NewSparkServiceClient(conn)

	t.Run("all_direct_txs_present", func(t *testing.T) {
		request := baseRequest

		_, err := sparkClient.StartDepositTreeCreation(ctx, request)
		require.NoError(t, err, "Expected StartDepositTreeCreation to succeed when all direct transactions are provided")
	})

	t.Run("missing_directFromCpfpRefundTx", func(t *testing.T) {
		request := baseRequest
		request.DirectFromCpfpRefundTxSigningJob = nil

		_, err := sparkClient.StartDepositTreeCreation(ctx, request)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "DirectFromCpfpRefundTxSigningJob is required. Please upgrade to the latest SDK version")
	})
}

func signingJobFromTx(t *testing.T, publicKey keys.Public, tx *wire.MsgTx) *pb.SigningJob {
	var txBuf bytes.Buffer
	require.NoError(t, tx.Serialize(&txBuf))

	nonceCommitmentProto, _ := frost.GenerateSigningNonce().SigningCommitment().MarshalProto()
	return &pb.SigningJob{
		RawTx:                  txBuf.Bytes(),
		SigningPublicKey:       publicKey.Serialize(),
		SigningNonceCommitment: nonceCommitmentProto,
	}
}
