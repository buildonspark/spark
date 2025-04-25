package grpctest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark/common"
	pbmock "github.com/lightsparkdev/spark/proto/mock"
	"github.com/lightsparkdev/spark/proto/spark"
	testutil "github.com/lightsparkdev/spark/test_util"
	"github.com/lightsparkdev/spark/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FakeLightningInvoiceCreator is a fake implementation of the LightningInvoiceCreator that always returns
// the invoice with which it is initialized.
type FakeLightningInvoiceCreator struct {
	invoice string
}

const testInvoice string = "lnbcrt123450n1pnj6uf4pp5l26hsdxssmr52vd4xmn5xran7puzx34hpr6uevaq7ta0ayzrp8esdqqcqzpgxqyz5vqrzjqtr2vd60g57hu63rdqk87u3clac6jlfhej4kldrrjvfcw3mphcw8sqqqqzp3jlj6zyqqqqqqqqqqqqqq9qsp5w22fd8aqn7sdum7hxdf59ptgk322fkv589ejxjltngvgehlcqcyq9qxpqysgqvykwsxdx64qrj0s5pgcgygmrpj8w25jsjgltwn09yp24l9nvghe3dl3y0ycy70ksrlqmcn42hxn24e0ucuy3g9fjltudvhv4lrhhamgq3stqgp"

func NewFakeLightningInvoiceCreator() *FakeLightningInvoiceCreator {
	return &FakeLightningInvoiceCreator{
		invoice: testInvoice,
	}
}

func NewFakeLightningInvoiceCreatorWithInvoice(invoice string) *FakeLightningInvoiceCreator {
	return &FakeLightningInvoiceCreator{
		invoice: invoice,
	}
}

func testPreimageHash(t *testing.T) ([32]byte, [32]byte) {
	preimage, err := hex.DecodeString("2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c")
	require.NoError(t, err)
	paymentHash := sha256.Sum256(preimage)
	return [32]byte(preimage), paymentHash
}

// CreateInvoice is a fake implementation of the LightningInvoiceCreator interface.
// It returns a fake invoice string.
func (f *FakeLightningInvoiceCreator) CreateInvoice(_ common.Network, _ uint64, _ []byte, _ string, _ int) (*string, int64, error) {
	return &f.invoice, 100, nil
}

func cleanUp(t *testing.T, config *wallet.Config, paymentHash [32]byte) {
	for _, operator := range config.SigningOperators {
		conn, err := common.NewGRPCConnectionWithTestTLS(operator.Address, nil)
		require.NoError(t, err)
		mockClient := pbmock.NewMockServiceClient(conn)
		_, err = mockClient.CleanUpPreimageShare(context.Background(), &pbmock.CleanUpPreimageShareRequest{
			PaymentHash: paymentHash[:],
		})
		require.NoError(t, err)
		conn.Close()
	}
}

func assertVerifiedPendingTransfer(t *testing.T, err error, leafPrivKeyMap *map[string][]byte, nodeToSend *spark.TreeNode, newLeafPrivKey *secp256k1.PrivateKey) {
	require.NoError(t, err, "unable to verify pending transfer")
	require.Equal(t, 1, len(*leafPrivKeyMap))
	require.Equal(t, (*leafPrivKeyMap)[nodeToSend.Id], newLeafPrivKey.Serialize(), "wrong leaf signing private key")
}

func TestCreateLightningInvoice(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	fakeInvoiceCreator := NewFakeLightningInvoiceCreator()

	preimage, paymentHash := testPreimageHash(t)

	invoice, _, err := wallet.CreateLightningInvoiceWithPreimage(context.Background(), config, fakeInvoiceCreator, 100, "test", preimage)
	require.NoError(t, err)
	assert.NotNil(t, invoice)

	cleanUp(t, config, paymentHash)
}

func TestReceiveLightningPayment(t *testing.T) {
	// Create user and ssp configs
	userConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	sspConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	// User creates an invoice
	preimage, paymentHash := testPreimageHash(t)
	fakeInvoiceCreator := NewFakeLightningInvoiceCreator()

	defer cleanUp(t, userConfig, paymentHash)

	invoice, _, err := wallet.CreateLightningInvoiceWithPreimage(context.Background(), userConfig, fakeInvoiceCreator, 100, "test", preimage)
	require.NoError(t, err)
	assert.NotNil(t, invoice)

	// SSP creates a node of 12345 sats
	sspLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	feeSats := uint64(2)
	nodeToSend, err := testutil.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 12343)
	require.NoError(t, err)

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)

	leaves := []wallet.LeafKeyTweak{}
	leaves = append(leaves, wallet.LeafKeyTweak{
		Leaf:              nodeToSend,
		SigningPrivKey:    sspLeafPrivKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	})

	response, err := wallet.SwapNodesForPreimage(
		context.Background(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		feeSats,
		true,
	)
	require.NoError(t, err)
	assert.Equal(t, response.Preimage, preimage[:])
	senderTransfer := response.Transfer

	transfer, err := wallet.SendTransferTweakKey(context.Background(), sspConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	assert.Equal(t, transfer.Status, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED)

	_, err = wallet.SwapNodesForPreimage(
		context.Background(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		feeSats,
		true,
	)
	require.Error(t, err, "should not be able to swap the same leaves twice")

	receiverToken, err := wallet.AuthenticateWithServer(context.Background(), userConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(context.Background(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, userConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Equal(t, 1, len(pendingTransfer.Transfers))
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, receiverTransfer.Id, senderTransfer.Id)
	require.Equal(t, receiverTransfer.Type, spark.TransferType_PREIMAGE_SWAP)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), userConfig, receiverTransfer)
	assertVerifiedPendingTransfer(t, err, leafPrivKeyMap, nodeToSend, newLeafPrivKey)

	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err, "failed to create new node signing private key")
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		userConfig,
		leavesToClaim[:],
	)
	require.NoError(t, err, "failed to ClaimTransfer")
}

func TestSendLightningPayment(t *testing.T) {
	// Create user and ssp configs
	userConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	sspConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	// User creates an invoice
	preimage, paymentHash := testPreimageHash(t)
	invoice := testInvoice

	defer cleanUp(t, userConfig, paymentHash)

	// User creates a node of 12345 sats
	userLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	feeSats := uint64(2)
	nodeToSend, err := testutil.CreateNewTree(userConfig, faucet, userLeafPrivKey, 12347)
	require.NoError(t, err)

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)

	leaves := []wallet.LeafKeyTweak{}
	leaves = append(leaves, wallet.LeafKeyTweak{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	})

	response, err := wallet.SwapNodesForPreimage(
		context.Background(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		feeSats,
		false,
	)
	require.NoError(t, err)

	transfer, err := wallet.SendTransferTweakKey(context.Background(), userConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	assert.Equal(t, transfer.Status, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING)

	refunds, err := wallet.QueryUserSignedRefunds(context.Background(), sspConfig, paymentHash[:])
	require.NoError(t, err)

	var totalValue int64
	for _, refund := range refunds {
		value, err := wallet.ValidateUserSignedRefund(refund)
		require.NoError(t, err)
		totalValue += value
	}
	assert.Equal(t, totalValue, int64(12345+feeSats))

	receiverTransfer, err := wallet.ProvidePreimage(context.Background(), sspConfig, preimage[:])
	require.NoError(t, err)
	assert.Equal(t, receiverTransfer.Status, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED)

	receiverToken, err := wallet.AuthenticateWithServer(context.Background(), sspConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(context.Background(), receiverToken)
	require.Equal(t, receiverTransfer.Id, transfer.Id)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), sspConfig, receiverTransfer)
	assertVerifiedPendingTransfer(t, err, leafPrivKeyMap, nodeToSend, newLeafPrivKey)

	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err, "failed to create new node signing private key")
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		sspConfig,
		leavesToClaim[:],
	)
	require.NoError(t, err, "failed to ClaimTransfer")
}

func TestSendLightningPaymentWithRejection(t *testing.T) {
	// Create user and ssp configs
	userConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	sspConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	// User creates an invoice
	_, paymentHash := testPreimageHash(t)
	invoice := testInvoice

	defer cleanUp(t, userConfig, paymentHash)

	// User creates a node of 12345 sats
	userLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	feeSats := uint64(2)
	nodeToSend, err := testutil.CreateNewTree(userConfig, faucet, userLeafPrivKey, 12347)
	require.NoError(t, err)

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)

	leaves := []wallet.LeafKeyTweak{}
	leaves = append(leaves, wallet.LeafKeyTweak{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	})

	response, err := wallet.SwapNodesForPreimage(
		context.Background(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		feeSats,
		false,
	)
	require.NoError(t, err)

	transfer, err := wallet.SendTransferTweakKey(context.Background(), userConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	assert.Equal(t, transfer.Status, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING)

	refunds, err := wallet.QueryUserSignedRefunds(context.Background(), sspConfig, paymentHash[:])
	require.NoError(t, err)

	var totalValue int64
	for _, refund := range refunds {
		value, err := wallet.ValidateUserSignedRefund(refund)
		require.NoError(t, err)
		totalValue += value
	}
	assert.Equal(t, totalValue, int64(12345+feeSats))

	err = wallet.ReturnLightningPayment(context.Background(), sspConfig, paymentHash[:])
	require.NoError(t, err)

	userTransfers, _, err := wallet.QueryAllTransfers(context.Background(), userConfig, 2, 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(userTransfers))
	require.Equal(t, userTransfers[0].Status, spark.TransferStatus_TRANSFER_STATUS_RETURNED)

	sspTransfers, _, err := wallet.QueryAllTransfers(context.Background(), sspConfig, 2, 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(sspTransfers))
	require.Equal(t, sspTransfers[0].Status, spark.TransferStatus_TRANSFER_STATUS_RETURNED)

	// Test the invoice can be paid again
	_, err = wallet.SwapNodesForPreimage(
		context.Background(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		feeSats,
		false,
	)
	require.NoError(t, err)
}

func TestReceiveLightningPaymentWithWrongPreimage(t *testing.T) {
	// Create user and ssp configs
	userConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	sspConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	// User creates an invoice
	preimage, wrongPaymentHash := testPreimageHash(t)
	wrongPaymentHash[0] = ^wrongPaymentHash[0]
	invoiceWithWrongHash := "lnbc123450n1pn7kvvldqsgdhkjmnnypcxcueppp5qk6hsdxssmr52vd4xmn5xran7puzx34hpr6uevaq7ta0ayzrp8essp5qyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqs9q2sqqqqqqsgqcqzysxqpymqqvpm3mvf87eqjtr7r4zj5jsxvlycq33qxsryhaefwxplhh6j6k5zjymcta3262rs3a0xntfrvawu83xlyx78epmywg4yek0anhh9tu9gp27zpuh"
	fakeInvoiceCreator := NewFakeLightningInvoiceCreatorWithInvoice(invoiceWithWrongHash)

	defer cleanUp(t, userConfig, wrongPaymentHash)

	invoice, _, err := wallet.CreateLightningInvoiceWithPreimageAndHash(context.Background(), userConfig, fakeInvoiceCreator, 100, "test", preimage, wrongPaymentHash)
	require.NoError(t, err)
	assert.NotNil(t, invoice)

	// SSP creates a node of 12345 sats
	sspLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	feeSats := uint64(2)
	nodeToSend, err := testutil.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 12343)
	require.NoError(t, err)

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)

	leaves := []wallet.LeafKeyTweak{}
	leaves = append(leaves, wallet.LeafKeyTweak{
		Leaf:              nodeToSend,
		SigningPrivKey:    sspLeafPrivKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	})

	_, err = wallet.SwapNodesForPreimage(
		context.Background(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		wrongPaymentHash[:],
		nil,
		feeSats,
		true,
	)
	require.Error(t, err, "should not be able to swap nodes with wrong payment hash")

	transfers, _, err := wallet.QueryAllTransfers(context.Background(), sspConfig, 1, 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(transfers))
	require.Equal(t, transfers[0].Status, spark.TransferStatus_TRANSFER_STATUS_RETURNED)

	transfer, err := wallet.SendTransfer(context.Background(), sspConfig, leaves, userConfig.IdentityPublicKey(), time.Unix(0, 0))
	require.NoError(t, err)
	assert.Equal(t, transfer.Status, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED)
}

func TestSendLightningPaymentTwice(t *testing.T) {
	// Create user and ssp configs
	userConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	sspConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	// User creates an invoice
	preimage, paymentHash := testPreimageHash(t)
	invoice := testInvoice

	defer cleanUp(t, userConfig, paymentHash)

	// User creates a node of 12345 sats
	userLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	feeSats := uint64(2)
	nodeToSend, err := testutil.CreateNewTree(userConfig, faucet, userLeafPrivKey, 12347)
	require.NoError(t, err)

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)

	leaves := []wallet.LeafKeyTweak{}
	leaves = append(leaves, wallet.LeafKeyTweak{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	})

	response, err := wallet.SwapNodesForPreimage(
		context.Background(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		feeSats,
		false,
	)
	require.NoError(t, err)

	_, err = wallet.SwapNodesForPreimage(
		context.Background(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		feeSats,
		false,
	)
	require.Error(t, err, "should not be able to swap the same leaves twice")

	transfer, err := wallet.SendTransferTweakKey(context.Background(), userConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	assert.Equal(t, transfer.Status, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING)

	refunds, err := wallet.QueryUserSignedRefunds(context.Background(), sspConfig, paymentHash[:])
	require.NoError(t, err)

	var totalValue int64
	for _, refund := range refunds {
		value, err := wallet.ValidateUserSignedRefund(refund)
		require.NoError(t, err)
		totalValue += value
	}
	assert.Equal(t, totalValue, int64(12345+feeSats))

	receiverTransfer, err := wallet.ProvidePreimage(context.Background(), sspConfig, preimage[:])
	require.NoError(t, err)
	assert.Equal(t, receiverTransfer.Status, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED)

	receiverToken, err := wallet.AuthenticateWithServer(context.Background(), sspConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(context.Background(), receiverToken)
	require.Equal(t, receiverTransfer.Id, transfer.Id)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), sspConfig, receiverTransfer)
	assertVerifiedPendingTransfer(t, err, leafPrivKeyMap, nodeToSend, newLeafPrivKey)

	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err, "failed to create new node signing private key")
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		sspConfig,
		leavesToClaim[:],
	)
	require.NoError(t, err, "failed to ClaimTransfer")
}
