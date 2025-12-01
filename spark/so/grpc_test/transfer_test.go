package grpctest

import (
	"crypto/sha256"
	"math/big"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
)

const amountSatsToSend = 100_000

func TestTransfer(t *testing.T) {
	// Sender initiates transfer
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey := keys.GeneratePrivateKey()

	receiverPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(senderConfig.CoordinatorAddress(), nil)
	require.NoError(t, err, "failed to create grpc connection")
	defer conn.Close()

	authToken, err := wallet.AuthenticateWithServer(t.Context(), senderConfig)
	require.NoError(t, err, "failed to authenticate sender")
	senderCtx := wallet.ContextWithToken(t.Context(), authToken)

	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		senderCtx,
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to transfer tree node")

	// Receiver queries pending transfer
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	require.NoError(t, err, "failed to create wallet config")
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)
	require.Equal(t, pb.TransferType_TRANSFER, receiverTransfer.Type)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.Id: newLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	res, err := wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to ClaimTransfer")
	require.Equal(t, res[0].Id, claimingNode.Leaf.Id)
}

func TestQueryPendingTransferByNetwork(t *testing.T) {
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey := keys.GeneratePrivateKey()

	receiverPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(senderConfig.CoordinatorAddress(), nil)
	require.NoError(t, err, "failed to create grpc connection")
	defer conn.Close()

	authToken, err := wallet.AuthenticateWithServer(t.Context(), senderConfig)
	require.NoError(t, err, "failed to authenticate sender")
	senderCtx := wallet.ContextWithToken(t.Context(), authToken)

	_, err = wallet.SendTransferWithKeyTweaks(
		senderCtx,
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to transfer tree node")

	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	require.NoError(t, err, "failed to create wallet config")
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)

	incorrectNetworkReceiverConfig := receiverConfig
	incorrectNetworkReceiverConfig.Network = common.Mainnet
	incorrectNetworkReceiverToken, err := wallet.AuthenticateWithServer(t.Context(), incorrectNetworkReceiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	incorrectNetworkReceiverCtx := wallet.ContextWithToken(t.Context(), incorrectNetworkReceiverToken)
	pendingTransfer, err = wallet.QueryPendingTransfers(incorrectNetworkReceiverCtx, incorrectNetworkReceiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Empty(t, pendingTransfer.Transfers)
}

func TestTransferInterrupt(t *testing.T) {
	sparktesting.RequireMinikube(t)

	// Sender initiates transfer
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(senderConfig.CoordinatorAddress(), nil)
	require.NoError(t, err, "failed to create grpc connection")
	defer conn.Close()

	authToken, err := wallet.AuthenticateWithServer(t.Context(), senderConfig)
	require.NoError(t, err, "failed to authenticate sender")
	senderCtx := wallet.ContextWithToken(t.Context(), authToken)

	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		senderCtx,
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to transfer tree node")

	// Receiver queries pending transfer
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)
	require.Equal(t, pb.TransferType_TRANSFER, receiverTransfer.Type)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.Id: newLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	proofs, err := wallet.ClaimTransferTweakKeys(receiverCtx, receiverTransfer, receiverConfig, leavesToClaim)
	require.NoError(t, err, "failed to ClaimTransferTweakKeys")

	// Bring SO 1 down and try to finish claiming.
	soController, err := sparktesting.NewSparkOperatorController(t)
	require.NoError(t, err, "failed to create SO controller")

	err = soController.DisableOperator(t, 1)
	require.NoError(t, err, "failed to disable operator 1")

	_, err = wallet.ClaimTransferSignRefunds(receiverCtx, receiverTransfer, receiverConfig, leavesToClaim, proofs, keys.Public{})
	require.Error(t, err, "expected error when claiming transfer")

	err = soController.EnableOperator(t, 1)
	require.NoError(t, err, "failed to enable operator 1")

	attempts := 0
	var claimedNodes []*pb.TreeNode

	// In theory we should be able to claim right away, but in practice, depending on the state of
	// the SOs, it may take a few attempts for it to get back to the right state. Since changing the
	// SO is scary, just retry a few times with a delay.
	for attempts < 5 {
		pendingTransfer, err = wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
		require.NoError(t, err, "failed to query pending transfers")
		require.Len(t, pendingTransfer.Transfers, 1)

		receiverTransfer = pendingTransfer.Transfers[0]
		require.Equal(t, senderTransfer.Id, receiverTransfer.Id)
		require.Equal(t, pb.TransferType_TRANSFER, receiverTransfer.Type)

		res, err := wallet.ClaimTransfer(receiverCtx, receiverTransfer, receiverConfig, leavesToClaim[:])
		if err != nil {
			t.Logf("Failed to ClaimTransfer: %v (attempt %d / 5)", err, attempts+1)
		} else {
			claimedNodes = res
			break
		}

		time.Sleep(1 * time.Second)
		attempts++
	}

	require.NotEmpty(t, claimedNodes, "failed to claim transfer after %d attempts", attempts)
	require.Equal(t, claimingNode.Leaf.Id, claimedNodes[0].Id)
}

func TestTransferRecoverFinalizeSignatures(t *testing.T) {
	// Sender initiates transfer
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")
	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := []wallet.LeafKeyTweak{transferNode}

	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		t.Context(),
		senderConfig,
		leavesToTransfer,
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to transfer tree node")

	// Receiver queries pending transfer
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)
	require.Equal(t, pb.TransferType_TRANSFER, receiverTransfer.Type)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.Id: newLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransferWithoutFinalizeSignatures(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to ClaimTransfer")

	pendingTransfer, err = wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer = pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

	res, err := wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to ClaimTransfer")
	require.Equal(t, res[0].Id, claimingNode.Leaf.Id)
}

func TestTransferZeroLeaves(t *testing.T) {
	senderConfig := wallet.NewTestWalletConfig(t)
	receiverPrivKey := keys.GeneratePrivateKey()

	var leavesToTransfer []wallet.LeafKeyTweak
	_, err := wallet.SendTransferWithKeyTweaks(
		t.Context(),
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.Error(t, err, "expected error when transferring zero leaves")
}

func TestTransferWithSeparateSteps(t *testing.T) {
	// Sender initiates transfer
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")
	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}
	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		t.Context(),
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to transfer tree node")

	// Receiver queries pending transfer
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.Id: newLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}

	_, err = wallet.ClaimTransferTweakKeys(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to ClaimTransferTweakKeys")

	pendingTransfer, err = wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer = pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

	leafPrivKeyMap, err = wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.Id: newLeafPrivKey}, leafPrivKeyMap)

	_, err = wallet.ClaimTransferSignRefunds(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim,
		nil,
		keys.Public{},
	)
	require.NoError(t, err, "failed to ClaimTransferSignRefunds")

	pendingTransfer, err = wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)

	_, err = wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to ClaimTransfer")
}

// Enable strict finalize signature knob for double claim transfer test
type TestDoubleClaimTransferKnobProvider struct{}

func (TestDoubleClaimTransferKnobProvider) GetValue(key string, defaultValue float64) float64 {
	return defaultValue
}

func (TestDoubleClaimTransferKnobProvider) RolloutRandom(key string, defaultValue float64) bool {
	if key == knobs.KnobEnableStrictFinalizeSignature {
		return true
	}
	return false
}

func TestDoubleClaimTransfer(t *testing.T) {
	// Sender initiates transfer
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := []wallet.LeafKeyTweak{transferNode}
	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		t.Context(),
		senderConfig,
		leavesToTransfer,
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to transfer tree node")

	// Receiver queries pending transfer
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtxTmp := wallet.ContextWithToken(t.Context(), receiverToken)

	receiverCtx := knobs.InjectKnobsService(receiverCtxTmp, knobs.New(TestDoubleClaimTransferKnobProvider{}))

	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.Id: newLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}

	errCount := 0
	wg := sync.WaitGroup{}
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err = wallet.ClaimTransfer(receiverCtx, receiverTransfer, receiverConfig, leavesToClaim)
			if err != nil {
				errCount++
			}
		}()
	}
	wg.Wait()

	if errCount == 5 {
		pendingTransfer, err = wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
		require.NoError(t, err, "failed to query pending transfers")
		require.Len(t, pendingTransfer.Transfers, 1)
		receiverTransfer = pendingTransfer.Transfers[0]
		require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

		res, err := wallet.ClaimTransfer(
			receiverCtx,
			receiverTransfer,
			receiverConfig,
			leavesToClaim,
		)
		if err != nil {
			// if the claim failed, the transfer should revert back to sender key tweaked status
			pendingTransfer, err = wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
			require.NoError(t, err, "failed to query pending transfers")
			require.Len(t, pendingTransfer.Transfers, 1)
			receiverTransfer = pendingTransfer.Transfers[0]
			require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

			res, err = wallet.ClaimTransfer(
				receiverCtx,
				receiverTransfer,
				receiverConfig,
				leavesToClaim,
			)
			require.NoError(t, err, "failed to ClaimTransfer")
		}

		require.Equal(t, res[0].Id, claimingNode.Leaf.Id)
	}
}

func TestValidSparkInvoiceTransfer(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	memoString := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	tenMinutesFromNow := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	amountSats := &amountToSend
	expiryTime := &tenMinutesFromNow
	memo := &memoString

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		amountSats,
		memo,
		senderPublicKey,
		expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)
	sigBytes := sig.Serialize()

	invoice, err := common.EncodeSparkAddressWithSignature(receiverPublicKey, network, invoiceFields, sigBytes)
	require.NoError(t, err)

	// Should succeed on first attempt.
	testTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)

	// Single Use Invoice.
	// Should fail on second attempt.
	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestValidSparkInvoiceTransferEmptySenderPublicKey(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountSats := uint64(amountSatsToSend)
	memo := "test memo"
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tenMinutesFromNow := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	emptySenderPublicKey := keys.Public{}
	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		&amountSats,
		&memo,
		emptySenderPublicKey,
		&tenMinutesFromNow,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)
	sigBytes := sig.Serialize()

	invoice, err := common.EncodeSparkAddressWithSignature(receiverPublicKey, network, invoiceFields, sigBytes)
	require.NoError(t, err)

	// Should succeed on first attempt.
	testTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)

	// Single Use Invoice.
	// Should fail on second attempt.
	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestValidSparkInvoiceTransferEmptyExpiry(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountSats := uint64(amountSatsToSend)
	memo := "test memo"
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	network := common.Regtest

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		&amountSats,
		&memo,
		senderPublicKey,
		nil,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)
	sigBytes := sig.Serialize()

	invoice, err := common.EncodeSparkAddressWithSignature(receiverPublicKey, network, invoiceFields, sigBytes)
	require.NoError(t, err)

	// Should succeed on first attempt.
	testTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)

	// Single Use Invoice.
	// Should fail on second attempt.
	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestValidSparkInvoiceTransferEmptyMemo(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountSats := uint64(amountSatsToSend)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	network := common.Regtest
	tenMinutesFromNow := time.Now().Add(10 * time.Minute)

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		&amountSats,
		nil,
		senderPublicKey,
		&tenMinutesFromNow,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)
	sigBytes := sig.Serialize()

	invoice, err := common.EncodeSparkAddressWithSignature(receiverPublicKey, network, invoiceFields, sigBytes)
	require.NoError(t, err)

	// Should succeed on first attempt.
	testTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)

	// Single Use Invoice.
	// Should fail on second attempt.
	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestValidSparkInvoiceTransferEmptyAmount(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	memoString := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	network := common.Regtest
	tenMinutesFromNow := time.Now().Add(10 * time.Minute)

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		nil,
		&memoString,
		senderPublicKey,
		&tenMinutesFromNow,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)
	sigBytes := sig.Serialize()

	invoice, err := common.EncodeSparkAddressWithSignature(receiverPublicKey, network, invoiceFields, sigBytes)
	require.NoError(t, err)

	// Should succeed on first attempt.
	testTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)

	// Single Use Invoice.
	// Should fail on second attempt.
	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestValidSparkInvoiceTransferEmptySignature(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	memoString := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	network := common.Regtest
	tenMinutesFromNow := time.Now().Add(10 * time.Minute)

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		nil,
		&memoString,
		senderPublicKey,
		&tenMinutesFromNow,
	)

	invoice, err := common.EncodeSparkAddressWithSignature(receiverPublicKey, network, invoiceFields, nil)
	require.NoError(t, err)

	// Should succeed on first attempt.
	testTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)

	// Single Use Invoice.
	// Should fail on second attempt.
	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestNonCanonicalInvoiceShouldError(t *testing.T) {
	nonCanonicalInvoice := "sprt1pgssx2ndesmr2cm86s6ylgsx7rqed58p5l4skcw69e2kzqqxgg79j2fszgsqsqgjzqqe364u4mehdy9wur7lc64al4sjypqg5zxsv2syw3jhxaq6gpanrus3aq8sy6c27zj008mjas6x7akw2pt7expuhmsnpmxrakjmrjeep56gqehrh6gwvq9g9nlcy2587n2m9kehdq446t483nnyar5rgasyvl"
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	decoded, err := common.DecodeSparkAddress(nonCanonicalInvoice)
	require.NoError(t, err)
	identityPublicKey, err := keys.ParsePublicKey(decoded.SparkAddress.IdentityPublicKey)
	require.NoError(t, err)

	reEncoded, err := common.EncodeSparkAddressWithSignature(
		identityPublicKey,
		decoded.Network,
		decoded.SparkAddress.SparkInvoiceFields,
		decoded.SparkAddress.Signature,
	)
	require.NoError(t, err)
	require.NotEqual(t, nonCanonicalInvoice, reEncoded)
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	_, _, _, err = sendTransferWithInvoice(t, nonCanonicalInvoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithMismatchedSender(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	amountSats := &amountToSend
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	expiryTime := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	mismatchedSender := keys.MustGeneratePrivateKeyFromRand(rng)
	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		amountSats,
		&memo,
		mismatchedSender.Public(),
		&expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	invoice, err := common.EncodeSparkAddressWithSignature(receiverPublicKey, network, invoiceFields, sig.Serialize())
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithMismatchedReceiver(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	amountSats := &amountToSend
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	expiryTime := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		amountSats,
		&memo,
		senderPublicKey,
		&expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	mismatchedReceiver := keys.MustGeneratePrivateKeyFromRand(rng)
	invoice, err := common.EncodeSparkAddressWithSignature(
		mismatchedReceiver.Public(),
		network,
		invoiceFields,
		sig.Serialize(),
	)
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithInvoiceAmountLessThanSentAmount(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	expiryTime := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	lessThanSentAmount := uint64(amountSatsToSend - 1)

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		&lessThanSentAmount,
		&memo,
		senderPublicKey,
		&expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	invoice, err := common.EncodeSparkAddressWithSignature(receiverPublicKey, network, invoiceFields, sig.Serialize())
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithInvoiceAmountGreaterThanSentAmount(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	expiryTime := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	greaterThanSentAmount := uint64(amountSatsToSend + 1)

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		&greaterThanSentAmount,
		&memo,
		senderPublicKey,
		&expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	invoice, err := common.EncodeSparkAddressWithSignature(receiverPublicKey, network, invoiceFields, sig.Serialize())
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithExpiredInvoice(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	amountSats := &amountToSend
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	network := common.Regtest

	expiryInThePast := time.Now().Add(-10 * time.Minute)
	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		amountSats,
		&memo,
		senderPublicKey,
		&expiryInThePast,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	invoice, err := common.EncodeSparkAddressWithSignature(receiverPublicKey, network, invoiceFields, sig.Serialize())
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithInvalidSignature(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	amountSats := &amountToSend
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	expiryTime := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		amountSats,
		&memo,
		senderPublicKey,
		&expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	// Sign with sender instead of receiver private key.
	sig, err := schnorr.Sign(senderPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	invoice, err := common.EncodeSparkAddressWithSignature(receiverPublicKey, network, invoiceFields, sig.Serialize())
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithMismatchedNetwork(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	amountSats := &amountToSend
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	expiryTime := time.Now().Add(10 * time.Minute)
	mismatchedNetwork := common.Mainnet

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		amountSats,
		&memo,
		senderPublicKey,
		&expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, mismatchedNetwork, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	invoice, err := common.EncodeSparkAddressWithSignature(receiverPublicKey, mismatchedNetwork, invoiceFields, sig.Serialize())
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func TestInvalidSparkInvoiceTransferShouldErrorWithTokensInvoice(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	amountSats := &amountToSend
	memo := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	expiryTime := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	amountBytes := new(big.Int).SetUint64(*amountSats).Bytes()
	invoiceFields := common.CreateTokenSparkInvoiceFields(
		invoiceUUID[:],
		[]byte{},
		amountBytes,
		&memo,
		senderPublicKey,
		&expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)

	invoice, err := common.EncodeSparkAddressWithSignature(receiverPublicKey, network, invoiceFields, sig.Serialize())
	require.NoError(t, err)

	_, _, _, err = sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.Error(t, err)
}

func testTransferWithInvoice(t *testing.T, invoice string, senderPrivKey keys.Private, receiverPrivKey keys.Private) {
	senderTransfer, rootNode, newLeafPrivKey, err := sendTransferWithInvoice(t, invoice, senderPrivKey, receiverPrivKey)
	require.NoError(t, err, "failed to send transfer with invoice")

	senderConfig := wallet.NewTestWalletConfigWithIdentityKey(t, senderPrivKey)
	authToken, err := wallet.AuthenticateWithServer(t.Context(), senderConfig)
	require.NoError(t, err, "failed to authenticate sender")
	senderCtx := wallet.ContextWithToken(t.Context(), authToken)
	invoiceResponse, err := wallet.QuerySparkInvoicesByRawString(
		senderCtx,
		senderConfig,
		[]string{invoice},
	)
	require.NoError(t, err, "failed to query spark invoices")
	transferID, err := uuid.Parse(senderTransfer.Id)
	require.NoError(t, err, "failed to parse transfer ID")

	require.Len(t, invoiceResponse.InvoiceStatuses, 1)
	require.Equal(t, invoice, invoiceResponse.InvoiceStatuses[0].Invoice)
	require.Equal(t, sparkpb.InvoiceStatus_FINALIZED, invoiceResponse.InvoiceStatuses[0].Status)
	require.Equal(t, &sparkpb.InvoiceResponse_SatsTransfer{
		SatsTransfer: &sparkpb.SatsTransfer{
			TransferId: transferID[:],
		},
	}, invoiceResponse.InvoiceStatuses[0].TransferType)

	// Receiver queries pending transfer
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.NotEmpty(t, pendingTransfer.Transfers)
	// With deterministic private key generation, when the test is retried on failure,
	// transfers from the previous failed run will come back as a pending transfer.
	// Find the one that matches this run so we can pass retry.
	var receiverTransfer *pb.Transfer
	for _, t := range pendingTransfer.Transfers {
		if t.Id == senderTransfer.Id {
			receiverTransfer = t
			break
		}
	}
	require.NotNil(t, receiverTransfer)
	require.Equal(t, pb.TransferType_TRANSFER, receiverTransfer.Type)
	require.Equal(t, invoice, receiverTransfer.GetSparkInvoice())

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{rootNode.Id: newLeafPrivKey}, leafPrivKeyMap)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	res, err := wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		receiverConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to ClaimTransfer")
	require.Equal(t, res[0].Id, claimingNode.Leaf.Id)
}

func sendTransferWithInvoice(
	t *testing.T,
	invoice string,
	senderPrivKey keys.Private,
	receiverPrivKey keys.Private,
) (senderTransfer *pb.Transfer, rootNode *pb.TreeNode, newLeafPrivKey keys.Private, err error) {
	senderConfig := wallet.NewTestWalletConfigWithIdentityKey(t, senderPrivKey)

	// Sender initiates transfer
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err = wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey = keys.GeneratePrivateKey()
	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(senderConfig.CoordinatorAddress(), nil)
	require.NoError(t, err, "failed to create grpc connection")
	defer conn.Close()
	authToken, err := wallet.AuthenticateWithServer(t.Context(), senderConfig)
	require.NoError(t, err, "failed to authenticate sender")
	senderCtx := wallet.ContextWithToken(t.Context(), authToken)

	senderTransfer, err = wallet.SendTransferWithKeyTweaksAndInvoice(
		senderCtx,
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
		invoice,
	)
	return senderTransfer, rootNode, newLeafPrivKey, err
}

func TestQuerySparkInvoicesForUnknownInvoiceReturnsNotFound(t *testing.T) {
	rng := rand.NewChaCha8(deterministicSeedFromTestName(t.Name()))
	invoiceUUID, err := uuid.NewV7FromReader(rng)
	require.NoError(t, err)
	amountToSend := uint64(amountSatsToSend)
	memoString := "test memo"
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPublicKey := senderPrivKey.Public()
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPublicKey := receiverPrivKey.Public()
	tenMinutesFromNow := time.Now().Add(10 * time.Minute)
	network := common.Regtest

	amountSats := &amountToSend
	expiryTime := &tenMinutesFromNow
	memo := &memoString

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceUUID[:],
		amountSats,
		memo,
		senderPublicKey,
		expiryTime,
	)

	invoiceHash, err := common.HashSparkInvoiceFields(invoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	sig, err := schnorr.Sign(receiverPrivKey.ToBTCEC(), invoiceHash)
	require.NoError(t, err)
	sigBytes := sig.Serialize()

	invoice, err := common.EncodeSparkAddressWithSignature(receiverPublicKey, network, invoiceFields, sigBytes)
	require.NoError(t, err)

	senderConfig := wallet.NewTestWalletConfig(t)
	authToken, err := wallet.AuthenticateWithServer(t.Context(), senderConfig)
	require.NoError(t, err, "failed to authenticate sender")
	senderCtx := wallet.ContextWithToken(t.Context(), authToken)
	invoiceResponse, err := wallet.QuerySparkInvoicesByRawString(
		senderCtx,
		senderConfig,
		[]string{invoice},
	)
	require.NoError(t, err, "failed to query spark invoices")
	require.Len(t, invoiceResponse.InvoiceStatuses, 1)
	require.Equal(t, sparkpb.InvoiceStatus_NOT_FOUND, invoiceResponse.InvoiceStatuses[0].Status)
}

func TestQueryTransfersRequiresParticipantOrTransferIds(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	sparkClient := pb.NewSparkServiceClient(conn)

	// Test that QueryPendingTransfers fails when both Participant and TransferIds are missing
	network, err := common.ProtoNetworkFromNetwork(config.Network)
	require.NoError(t, err)
	_, err = sparkClient.QueryPendingTransfers(ctx, &pb.TransferFilter{
		Network: network,
	})
	require.ErrorContains(t, err, "must specify either filter.Participant or filter.TransferIds")

	// Test that QueryAllTransfers fails when both Participant and TransferIds are missing
	network, err = common.ProtoNetworkFromNetwork(config.Network)
	require.NoError(t, err)
	_, err = sparkClient.QueryAllTransfers(ctx, &pb.TransferFilter{
		Network: network,
		Limit:   10,
		Offset:  0,
	})
	require.ErrorContains(t, err, "must specify either filter.Participant or filter.TransferIds")

	// Test that providing Participant makes the query succeed (even if no transfers exist)
	network, err = common.ProtoNetworkFromNetwork(config.Network)
	require.NoError(t, err)
	_, err = sparkClient.QueryPendingTransfers(ctx, &pb.TransferFilter{
		Participant: &pb.TransferFilter_ReceiverIdentityPublicKey{
			ReceiverIdentityPublicKey: config.IdentityPublicKey().Serialize(),
		},
		Network: network,
	})
	require.NoError(t, err, "Expected success when Participant is specified")

	// Test that providing TransferIds makes the query succeed (even if no transfers exist)
	network, err = common.ProtoNetworkFromNetwork(config.Network)
	require.NoError(t, err)
	_, err = sparkClient.QueryPendingTransfers(ctx, &pb.TransferFilter{
		TransferIds: []string{uuid.NewString()},
		Network:     network,
	})
	require.NoError(t, err, "Expected success when TransferIds are specified")
}

func deterministicSeedFromTestName(testName string) [32]byte {
	return sha256.Sum256([]byte(testName))
}
