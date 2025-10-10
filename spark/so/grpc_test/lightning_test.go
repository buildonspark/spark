package grpctest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	sparktesting "github.com/lightsparkdev/spark/testing"
	decodepay "github.com/nbd-wtf/ln-decodepay"

	"github.com/lightsparkdev/spark/common"
	pbmock "github.com/lightsparkdev/spark/proto/mock"
	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FakeLightningInvoiceCreator is a fake implementation of the LightningInvoiceCreator that always returns
// the invoice with which it is initialized.
type FakeLightningInvoiceCreator struct {
	invoice     string
	zeroInvoice string
}

const (
	testInvoice     string = "lnbcrt123450n1pnj6uf4pp5l26hsdxssmr52vd4xmn5xran7puzx34hpr6uevaq7ta0ayzrp8esdqqcqzpgxqyz5vqrzjqtr2vd60g57hu63rdqk87u3clac6jlfhej4kldrrjvfcw3mphcw8sqqqqzp3jlj6zyqqqqqqqqqqqqqq9qsp5w22fd8aqn7sdum7hxdf59ptgk322fkv589ejxjltngvgehlcqcyq9qxpqysgqvykwsxdx64qrj0s5pgcgygmrpj8w25jsjgltwn09yp24l9nvghe3dl3y0ycy70ksrlqmcn42hxn24e0ucuy3g9fjltudvhv4lrhhamgq3stqgp"
	testZeroInvoice string = "lnbc1pjkkc4qpp506g22474pc5lle9nkwd2sgp2uk8muyxa79fga5dc9xfxwst0dwjqdz9235xjueqd9ejqcfqwd5k6urvv5sxjmnkda5kxefqveex7mfq2dkx7apqf4skx6rfdejjucqzzsxqyz5vqrzjqtqd37k2ya0pv8pqeyjs4lklcexjyw600g9qqp62r4j0ph8fcmlfwqqqqqysrpfykyqqqqqqqqqqqqqq9qsp5x88g0rk9e4qnsc6hgf4mrllrhu2f94psqkun9j4007pd0ts9ktcs9qyyssqdrq33g2nze886y98p0jsrezyva2jqqe3kgxaexrz0p470d7hpxrnxy5z3x9sdk0x3s23v0g78f2vgq7lckkp0gk7as5kxaygjzec0acpm7nz5l"
)

func NewFakeLightningInvoiceCreator() *FakeLightningInvoiceCreator {
	return &FakeLightningInvoiceCreator{
		invoice:     testInvoice,
		zeroInvoice: testZeroInvoice,
	}
}

func NewFakeLightningInvoiceCreatorWithInvoice(invoice string) *FakeLightningInvoiceCreator {
	return &FakeLightningInvoiceCreator{
		invoice: invoice,
	}
}

func testPreimageHash(t *testing.T, amountSats uint64) ([32]byte, [32]byte) {
	var preimageHex string
	if amountSats == 0 {
		preimageHex = "b27cabd004b2194aca8022a0f311a25db939771e11adf2ed226033917d39ce0d"
	} else {
		preimageHex = "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c"
	}
	preimage, err := hex.DecodeString(preimageHex)
	require.NoError(t, err)
	paymentHash := sha256.Sum256(preimage)
	return [32]byte(preimage), paymentHash
}

// CreateInvoice is a fake implementation of the LightningInvoiceCreator interface.
// It returns a fake invoice string.
func (f *FakeLightningInvoiceCreator) CreateInvoice(_ context.Context, _ common.Network, amountSats int64, _ []byte, _ string, _ time.Duration) (string, error) {
	var invoice string
	if amountSats == 0 {
		invoice = f.zeroInvoice
	} else {
		invoice = f.invoice
	}
	return invoice, nil
}

func cleanUp(t *testing.T, config *wallet.TestWalletConfig, paymentHash [32]byte) {
	for _, operator := range config.SigningOperators {
		conn, err := operator.NewOperatorGRPCConnection()
		require.NoError(t, err)
		mockClient := pbmock.NewMockServiceClient(conn)
		_, err = mockClient.CleanUpPreimageShare(t.Context(), &pbmock.CleanUpPreimageShareRequest{
			PaymentHash: paymentHash[:],
		})
		require.NoError(t, err)
		conn.Close()
	}
}

func TestCreateLightningInvoice(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	fakeInvoiceCreator := NewFakeLightningInvoiceCreator()

	amountSats := uint64(100)
	preimage, paymentHash := testPreimageHash(t, amountSats)

	invoice, err := wallet.CreateLightningInvoiceWithPreimage(t.Context(), config, fakeInvoiceCreator, amountSats, "test", preimage)
	require.NoError(t, err)
	require.Equal(t, testInvoice, invoice)

	cleanUp(t, config, paymentHash)
}

func TestCreateZeroAmountLightningInvoice(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	fakeInvoiceCreator := NewFakeLightningInvoiceCreator()

	amountSats := uint64(0)
	preimage, paymentHash := testPreimageHash(t, amountSats)

	invoice, err := wallet.CreateLightningInvoiceWithPreimage(t.Context(), config, fakeInvoiceCreator, amountSats, "test", preimage)
	require.NoError(t, err)
	require.Equal(t, testZeroInvoice, invoice)

	cleanUp(t, config, paymentHash)
}

func TestReceiveLightningPayment(t *testing.T) {
	// Create user and ssp configs
	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)
	// User creates an invoice
	amountSats := uint64(100)
	preimage, paymentHash := testPreimageHash(t, amountSats)
	fakeInvoiceCreator := NewFakeLightningInvoiceCreator()

	defer cleanUp(t, userConfig, paymentHash)

	invoice, err := wallet.CreateLightningInvoiceWithPreimage(t.Context(), userConfig, fakeInvoiceCreator, amountSats, "test", preimage)
	require.NoError(t, err)
	assert.NotNil(t, invoice)

	// SSP creates a node of 12345 sats
	sspLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(0)
	nodeToSend, err := wallet.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 12345)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()

	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    sspLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		feeSats,
		true,
		amountSats,
	)
	require.NoError(t, err)
	assert.Equal(t, response.Preimage, preimage[:])
	senderTransfer := response.Transfer

	transfer, err := wallet.DeliverTransferPackage(t.Context(), sspConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, transfer.Status)

	_, err = wallet.SwapNodesForPreimage(
		t.Context(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		feeSats,
		true,
		amountSats,
	)
	require.Error(t, err, "should not be able to swap the same leaves twice")

	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), userConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, userConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, receiverTransfer.Id, senderTransfer.Id)
	require.Equal(t, spark.TransferType_PREIMAGE_SWAP, receiverTransfer.Type)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), userConfig, receiverTransfer)
	sparktesting.AssertVerifiedPendingTransfer(t, err, leafPrivKeyMap, nodeToSend, newLeafPrivKey)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(receiverCtx, receiverTransfer, userConfig, leavesToClaim)
	require.NoError(t, err, "failed to ClaimTransfer")
}

func TestReceiveZeroAmountLightningInvoicePayment(t *testing.T) {
	// Create user and ssp configs
	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)
	// User creates a 0-amount invoice
	invoiceSats := uint64(0)
	preimage, paymentHash := testPreimageHash(t, invoiceSats)
	fakeInvoiceCreator := NewFakeLightningInvoiceCreator()

	defer cleanUp(t, userConfig, paymentHash)

	invoice, err := wallet.CreateLightningInvoiceWithPreimage(t.Context(), userConfig, fakeInvoiceCreator, invoiceSats, "test", preimage)
	require.NoError(t, err)
	require.NotNil(t, invoice)
	bolt11, err := decodepay.Decodepay(invoice)
	require.NoError(t, err)
	require.Equal(t, int64(0), bolt11.MSatoshi, "invoice amount should be 0")

	paymentAmountSats := uint64(15000)
	// SSP creates a node of sats equals to the payment amount
	sspLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(0)
	nodeToSend, err := wallet.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, int64(paymentAmountSats))
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()

	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    sspLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		feeSats,
		true,
		paymentAmountSats,
	)
	require.NoError(t, err)
	require.Equal(t, response.Preimage, preimage[:])
	senderTransfer := response.Transfer

	transfer, err := wallet.DeliverTransferPackage(t.Context(), sspConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	require.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, transfer.Status)
	require.Equal(t, transfer.TotalValue, paymentAmountSats)

	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), userConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, userConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pendingTransfer.Transfers, 1)
	receiverTransfer := pendingTransfer.Transfers[0]
	require.Equal(t, receiverTransfer.Id, senderTransfer.Id)
	require.Equal(t, spark.TransferType_PREIMAGE_SWAP, receiverTransfer.Type)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), userConfig, receiverTransfer)
	sparktesting.AssertVerifiedPendingTransfer(t, err, leafPrivKeyMap, nodeToSend, newLeafPrivKey)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(receiverCtx, receiverTransfer, userConfig, leavesToClaim)
	require.NoError(t, err, "failed to ClaimTransfer")
}

func TestReceiveLightningPaymentCannotCancelAfterPreimageReveal(t *testing.T) {
	// Create user and ssp configs
	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)
	// User creates an invoice
	amountSats := uint64(100)
	preimage, paymentHash := testPreimageHash(t, amountSats)
	fakeInvoiceCreator := NewFakeLightningInvoiceCreator()

	defer cleanUp(t, userConfig, paymentHash)

	invoice, err := wallet.CreateLightningInvoiceWithPreimage(t.Context(), userConfig, fakeInvoiceCreator, amountSats, "test", preimage)
	require.NoError(t, err)
	assert.NotNil(t, invoice)

	// SSP creates a node of 12345 sats
	sspLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(0)
	nodeToSend, err := wallet.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 12345)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()

	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    sspLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		feeSats,
		true,
		amountSats,
	)
	require.NoError(t, err)
	assert.Equal(t, response.Preimage, preimage[:])

	_, err = wallet.CancelTransfer(t.Context(), sspConfig, response.Transfer)
	require.ErrorContains(t, err, "FailedPrecondition")
	require.ErrorContains(t, err, "Cannot cancel an invoice whose preimage has already been revealed")
}

func TestSendLightningPayment(t *testing.T) {
	// Create user and ssp configs
	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)
	// User creates an invoice
	amountSats := uint64(100)
	preimage, paymentHash := testPreimageHash(t, amountSats)
	invoice := testInvoice

	defer cleanUp(t, userConfig, paymentHash)

	// User creates a node of 12345 sats
	userLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(2)
	nodeToSend, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey, 12347)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()

	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		feeSats,
		false,
		amountSats,
	)
	require.NoError(t, err)

	transfer, err := wallet.DeliverTransferPackage(t.Context(), userConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, transfer.Status)

	refunds, err := wallet.QueryUserSignedRefunds(t.Context(), sspConfig, paymentHash[:])
	require.NoError(t, err)

	var totalValue int64
	for _, refund := range refunds {
		value, err := wallet.ValidateUserSignedRefund(refund)
		require.NoError(t, err)
		totalValue += value
	}
	assert.Equal(t, totalValue, int64(12345+feeSats))

	receiverTransfer, err := wallet.ProvidePreimage(t.Context(), sspConfig, preimage[:])
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, receiverTransfer.Status)

	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), sspConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	require.Equal(t, receiverTransfer.Id, transfer.Id)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), sspConfig, receiverTransfer)
	sparktesting.AssertVerifiedPendingTransfer(t, err, leafPrivKeyMap, nodeToSend, newLeafPrivKey)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		sspConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to ClaimTransfer")
}

func TestSendLightningPaymentV2(t *testing.T) {
	// Create user and ssp configs
	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)
	// User creates an invoice
	amountSats := uint64(100)
	preimage, paymentHash := testPreimageHash(t, amountSats)
	invoice := testInvoice

	defer cleanUp(t, userConfig, paymentHash)

	// User creates a node of 12345 sats
	userLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(2)
	nodeToSend, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey, 12347)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()

	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		feeSats,
		false,
		amountSats,
	)
	require.NoError(t, err)

	transfer, err := wallet.DeliverTransferPackage(t.Context(), userConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, transfer.Status)

	refunds, err := wallet.QueryUserSignedRefunds(t.Context(), sspConfig, paymentHash[:])
	require.NoError(t, err)

	var totalValue int64
	for _, refund := range refunds {
		value, err := wallet.ValidateUserSignedRefund(refund)
		require.NoError(t, err)
		totalValue += value
	}
	assert.Equal(t, int64(12345+feeSats), totalValue)

	// Check that the expiry time is at least 15 days from now
	htlcs, err := wallet.QueryHTLC(t.Context(), sspConfig, 5, 0, nil, nil)
	require.NoError(t, err)
	expiryTime := htlcs.PreimageRequests[0].Transfer.ExpiryTime.AsTime()
	require.Greater(t, expiryTime, time.Now().Add(15*24*time.Hour))

	receiverTransfer, err := wallet.ProvidePreimage(t.Context(), sspConfig, preimage[:])
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, receiverTransfer.Status)

	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), sspConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	require.Equal(t, receiverTransfer.Id, transfer.Id)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), sspConfig, receiverTransfer)
	sparktesting.AssertVerifiedPendingTransfer(t, err, leafPrivKeyMap, nodeToSend, newLeafPrivKey)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(
		receiverCtx,
		receiverTransfer,
		sspConfig,
		leavesToClaim,
	)
	require.NoError(t, err, "failed to ClaimTransfer")
}

func TestSendLightningPaymentWithRejection(t *testing.T) {
	// Create user and ssp configs
	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)
	// User creates an invoice
	amountSats := uint64(100)
	_, paymentHash := testPreimageHash(t, amountSats)
	invoice := testInvoice

	defer cleanUp(t, userConfig, paymentHash)

	// User creates a node of 12345 sats
	userLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(2)
	nodeToSend, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey, 12347)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()

	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		feeSats,
		false,
		amountSats,
	)
	require.NoError(t, err)

	transfer, err := wallet.DeliverTransferPackage(t.Context(), userConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, transfer.Status)

	refunds, err := wallet.QueryUserSignedRefunds(t.Context(), sspConfig, paymentHash[:])
	require.NoError(t, err)

	var totalValue int64
	for _, refund := range refunds {
		value, err := wallet.ValidateUserSignedRefund(refund)
		require.NoError(t, err)
		totalValue += value
	}
	assert.Equal(t, totalValue, int64(12345+feeSats))

	err = wallet.ReturnLightningPayment(t.Context(), sspConfig, paymentHash[:])
	require.NoError(t, err)

	userTransfers, _, err := wallet.QueryAllTransfers(t.Context(), userConfig, 2, 0)
	require.NoError(t, err)
	require.Len(t, userTransfers, 1)
	require.Equal(t, spark.TransferStatus_TRANSFER_STATUS_RETURNED, userTransfers[0].Status)

	sspTransfers, _, err := wallet.QueryAllTransfers(t.Context(), sspConfig, 2, 0)
	require.NoError(t, err)
	require.Len(t, sspTransfers, 1)
	require.Equal(t, spark.TransferStatus_TRANSFER_STATUS_RETURNED, sspTransfers[0].Status)

	// Test the invoice can be paid again
	_, err = wallet.SwapNodesForPreimage(
		t.Context(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		feeSats,
		false,
		amountSats,
	)
	require.NoError(t, err)
}

func TestReceiveLightningPaymentWithWrongPreimage(t *testing.T) {
	// Create user and ssp configs
	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)
	// User creates an invoice
	amountSats := uint64(100)
	preimage, wrongPaymentHash := testPreimageHash(t, amountSats)
	wrongPaymentHash[0] = ^wrongPaymentHash[0]
	invoiceWithWrongHash := "lnbc123450n1pn7kvvldqsgdhkjmnnypcxcueppp5qk6hsdxssmr52vd4xmn5xran7puzx34hpr6uevaq7ta0ayzrp8essp5qyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqs9q2sqqqqqqsgqcqzysxqpymqqvpm3mvf87eqjtr7r4zj5jsxvlycq33qxsryhaefwxplhh6j6k5zjymcta3262rs3a0xntfrvawu83xlyx78epmywg4yek0anhh9tu9gp27zpuh"
	fakeInvoiceCreator := NewFakeLightningInvoiceCreatorWithInvoice(invoiceWithWrongHash)

	defer cleanUp(t, userConfig, wrongPaymentHash)

	invoice, err := wallet.CreateLightningInvoiceWithPreimageAndHash(t.Context(), userConfig, fakeInvoiceCreator, amountSats, "test", preimage, wrongPaymentHash)
	require.NoError(t, err)
	assert.NotNil(t, invoice)

	// SSP creates a node of 12345 sats
	sspLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(0)
	nodeToSend, err := wallet.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 12345)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()

	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    sspLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	_, err = wallet.SwapNodesForPreimage(
		t.Context(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		wrongPaymentHash[:],
		nil,
		feeSats,
		true,
		amountSats,
	)
	require.Error(t, err, "should not be able to swap nodes with wrong payment hash")

	transfers, _, err := wallet.QueryAllTransfers(t.Context(), sspConfig, 1, 0)
	require.NoError(t, err)
	require.Len(t, transfers, 1)
	require.Equal(t, spark.TransferStatus_TRANSFER_STATUS_RETURNED, transfers[0].Status)

	transfer, err := wallet.SendTransferWithKeyTweaks(t.Context(), sspConfig, leaves, userConfig.IdentityPublicKey(), time.Unix(0, 0))
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, transfer.Status)
}

func TestSendLightningPaymentTwice(t *testing.T) {
	// Create user and ssp configs
	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)
	// User creates an invoice
	amountSats := uint64(100)
	preimage, paymentHash := testPreimageHash(t, amountSats)
	invoice := testInvoice

	defer cleanUp(t, userConfig, paymentHash)

	// User creates a node of 12345 sats
	userLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(2)
	nodeToSend, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey, 12347)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()

	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		feeSats,
		false,
		amountSats,
	)
	require.NoError(t, err)

	_, err = wallet.SwapNodesForPreimage(
		t.Context(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		feeSats,
		false,
		amountSats,
	)
	require.Error(t, err, "should not be able to swap the same leaves twice")

	transfer, err := wallet.DeliverTransferPackage(t.Context(), userConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, transfer.Status)

	refunds, err := wallet.QueryUserSignedRefunds(t.Context(), sspConfig, paymentHash[:])
	require.NoError(t, err)

	var totalValue int64
	for _, refund := range refunds {
		value, err := wallet.ValidateUserSignedRefund(refund)
		require.NoError(t, err)
		totalValue += value
	}
	assert.Equal(t, int64(12345+feeSats), totalValue)

	receiverTransfer, err := wallet.ProvidePreimage(t.Context(), sspConfig, preimage[:])
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, receiverTransfer.Status)

	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), sspConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	require.Equal(t, receiverTransfer.Id, transfer.Id)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), sspConfig, receiverTransfer)
	sparktesting.AssertVerifiedPendingTransfer(t, err, leafPrivKeyMap, nodeToSend, newLeafPrivKey)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(receiverCtx, receiverTransfer, sspConfig, leavesToClaim)
	require.NoError(t, err, "failed to ClaimTransfer")
}

func TestSendLightningPaymentWithHTLC(t *testing.T) {
	// Create user and ssp configs
	userConfig := wallet.NewTestWalletConfig(t)

	sspConfig := wallet.NewTestWalletConfig(t)

	// User creates an invoice
	amountSats := uint64(100)
	preimage, paymentHash := testPreimageHash(t, amountSats)
	invoice := testInvoice

	defer cleanUp(t, userConfig, paymentHash)

	// User creates a node of 12345 sats
	userLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(2)
	nodeToSend, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey, 12347)
	require.NoError(t, err)
	newLeafPrivKey := keys.GeneratePrivateKey()

	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimageWithHTLC(
		t.Context(),
		userConfig,
		leaves,
		sspConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		feeSats,
		false,
		amountSats,
	)
	require.NoError(t, err)

	transfer := response.Transfer
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, transfer.Status)

	refunds, err := wallet.QueryUserSignedRefunds(t.Context(), sspConfig, paymentHash[:])
	require.NoError(t, err)

	var totalValue int64
	for _, refund := range refunds {
		value, err := wallet.ValidateUserSignedRefund(refund)
		require.NoError(t, err)
		totalValue += value
	}
	assert.Equal(t, int64(12345+feeSats), totalValue)

	receiverTransfer, err := wallet.ProvidePreimage(t.Context(), sspConfig, preimage[:])
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, receiverTransfer.Status)

	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), sspConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
	require.Equal(t, receiverTransfer.Id, transfer.Id)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(t.Context(), sspConfig, receiverTransfer)
	sparktesting.AssertVerifiedPendingTransfer(t, err, leafPrivKeyMap, nodeToSend, newLeafPrivKey)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}
	leavesToClaim := []wallet.LeafKeyTweak{claimingNode}
	_, err = wallet.ClaimTransfer(receiverCtx, receiverTransfer, sspConfig, leavesToClaim)
	require.NoError(t, err, "failed to ClaimTransfer")
}

func TestQueryHTLCWithNoFilters(t *testing.T) {
	// Create user and ssp configs
	userConfig := wallet.NewTestWalletConfig(t)

	// User creates an invoice
	amountSats := uint64(100)
	_, paymentHash := testPreimageHash(t, amountSats)
	invoice := testInvoice

	defer cleanUp(t, userConfig, paymentHash)

	// User creates a node of 12345 sats
	userLeafPrivKey := keys.GeneratePrivateKey()

	feeSats := uint64(2)
	nodeToSend, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey, 12347)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()

	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
		userConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		feeSats,
		false,
		amountSats,
	)
	require.NoError(t, err)

	transfer, err := wallet.DeliverTransferPackage(t.Context(), userConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, transfer.Status)

	htlcs, err := wallet.QueryHTLC(t.Context(), userConfig, 100, 0, nil, nil)
	require.NoError(t, err, "failed to query htlcs")
	require.Len(t, htlcs.PreimageRequests, 1)
	require.Equal(t, paymentHash[:], htlcs.PreimageRequests[0].PaymentHash)
	require.Equal(t, userConfig.IdentityPublicKey().Serialize(), htlcs.PreimageRequests[0].ReceiverIdentityPubkey)
	require.Equal(t, spark.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_WAITING_FOR_PREIMAGE, htlcs.PreimageRequests[0].Status)
	require.Equal(t, int64(-1), htlcs.Offset)
}

func TestQueryHTLCMultipleHTLCs(t *testing.T) {
	// Create user and ssp configs
	userConfig := wallet.NewTestWalletConfig(t)

	// User creates an invoice
	amountSats := uint64(1000)
	preimage, err := hex.DecodeString("01")
	require.NoError(t, err)
	paymentHash := sha256.Sum256(preimage)

	defer cleanUp(t, userConfig, paymentHash)

	// User creates a node of 12345 sats
	userLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(0)
	nodeToSend, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey, 1000)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()

	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
		userConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		feeSats,
		false,
		amountSats,
	)
	require.NoError(t, err)

	transfer, err := wallet.DeliverTransferPackage(t.Context(), userConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, transfer.Status)

	// User creates a second invoice
	amountSats2 := uint64(2000)
	preimage2, err := hex.DecodeString("02")
	require.NoError(t, err)
	paymentHash2 := sha256.Sum256(preimage2)

	defer cleanUp(t, userConfig, paymentHash2)

	// User creates a second node of 1000 sats
	userLeafPrivKey2 := keys.GeneratePrivateKey()

	nodeToSend2, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey2, 2000)
	require.NoError(t, err)

	newLeafPrivKey2 := keys.GeneratePrivateKey()
	require.NoError(t, err)

	leaves2 := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend2,
		SigningPrivKey:    userLeafPrivKey2,
		NewSigningPrivKey: newLeafPrivKey2,
	}}
	response2, err := wallet.SwapNodesForPreimage(
		t.Context(),
		userConfig,
		leaves2,
		userConfig.IdentityPublicKey(),
		paymentHash2[:],
		nil,
		feeSats,
		false,
		amountSats2,
	)
	require.NoError(t, err)

	transfer2, err := wallet.DeliverTransferPackage(t.Context(), userConfig, response2.Transfer, leaves2, nil)
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, transfer2.Status)

	htlcs, err := wallet.QueryHTLC(t.Context(), userConfig, 5, 0, nil, nil)
	require.NoError(t, err, "failed to query htlcs")
	require.Len(t, htlcs.PreimageRequests, 2)
	require.Equal(t, paymentHash[:], htlcs.PreimageRequests[0].PaymentHash)
	require.Equal(t, userConfig.IdentityPublicKey().Serialize(), htlcs.PreimageRequests[0].ReceiverIdentityPubkey)
	require.Equal(t, spark.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_WAITING_FOR_PREIMAGE, htlcs.PreimageRequests[0].Status)
	require.Equal(t, int64(-1), htlcs.Offset)

	require.Equal(t, paymentHash2[:], htlcs.PreimageRequests[1].PaymentHash)
	require.Equal(t, userConfig.IdentityPublicKey().Serialize(), htlcs.PreimageRequests[1].ReceiverIdentityPubkey)
	require.Equal(t, spark.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_WAITING_FOR_PREIMAGE, htlcs.PreimageRequests[1].Status)
	require.Equal(t, int64(-1), htlcs.Offset)
}

func TestQueryHTLCWithPaymentHashFilter(t *testing.T) {
	// Create user and ssp configs
	userConfig := wallet.NewTestWalletConfig(t)

	// User creates an invoice
	amountSats := uint64(1000)
	preimage, err := hex.DecodeString("01")
	require.NoError(t, err)
	paymentHash := sha256.Sum256(preimage)

	defer cleanUp(t, userConfig, paymentHash)

	// User creates a node of 12345 sats
	userLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(0)
	nodeToSend, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey, 1000)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()

	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
		userConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		feeSats,
		false,
		amountSats,
	)
	require.NoError(t, err)

	transfer, err := wallet.DeliverTransferPackage(t.Context(), userConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, transfer.Status)

	// User creates a second invoice
	amountSats2 := uint64(2000)
	preimage2, err := hex.DecodeString("02")
	require.NoError(t, err)
	paymentHash2 := sha256.Sum256(preimage2)

	defer cleanUp(t, userConfig, paymentHash2)

	// User creates a second node of 1000 sats
	userLeafPrivKey2 := keys.GeneratePrivateKey()
	nodeToSend2, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey2, 2000)
	require.NoError(t, err)

	newLeafPrivKey2 := keys.GeneratePrivateKey()
	require.NoError(t, err)

	leaves2 := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend2,
		SigningPrivKey:    userLeafPrivKey2,
		NewSigningPrivKey: newLeafPrivKey2,
	}}
	response2, err := wallet.SwapNodesForPreimage(
		t.Context(),
		userConfig,
		leaves2,
		userConfig.IdentityPublicKey(),
		paymentHash2[:],
		nil,
		feeSats,
		false,
		amountSats2,
	)
	require.NoError(t, err)

	transfer2, err := wallet.DeliverTransferPackage(t.Context(), userConfig, response2.Transfer, leaves2, nil)
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, transfer2.Status)

	htlcs, err := wallet.QueryHTLC(t.Context(), userConfig, 5, 0, [][]byte{paymentHash[:]}, nil)
	require.NoError(t, err, "failed to query htlcs")
	require.Len(t, htlcs.PreimageRequests, 1)
	require.Equal(t, paymentHash[:], htlcs.PreimageRequests[0].PaymentHash)
	require.Equal(t, userConfig.IdentityPublicKey().Serialize(), htlcs.PreimageRequests[0].ReceiverIdentityPubkey)
	require.Equal(t, spark.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_WAITING_FOR_PREIMAGE, htlcs.PreimageRequests[0].Status)
	require.Equal(t, int64(-1), htlcs.Offset)
}

func TestQueryHTLCWithStaatusFilter(t *testing.T) {
	// Create user and ssp configs
	userConfig := wallet.NewTestWalletConfig(t)

	// User creates an invoice
	amountSats := uint64(1000)
	preimage, err := hex.DecodeString("01")
	require.NoError(t, err)
	paymentHash := sha256.Sum256(preimage)

	defer cleanUp(t, userConfig, paymentHash)

	// User creates a node of 12345 sats
	userLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(0)
	nodeToSend, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey, 1000)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()

	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
		userConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		feeSats,
		false,
		amountSats,
	)
	require.NoError(t, err)

	transfer, err := wallet.DeliverTransferPackage(t.Context(), userConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, transfer.Status)

	status := spark.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_WAITING_FOR_PREIMAGE
	htlcs, err := wallet.QueryHTLC(t.Context(), userConfig, 5, 0, nil, &status)
	require.NoError(t, err, "failed to query htlcs")
	require.Len(t, htlcs.PreimageRequests, 1)
	require.Equal(t, paymentHash[:], htlcs.PreimageRequests[0].PaymentHash)
	require.Equal(t, userConfig.IdentityPublicKey().Serialize(), htlcs.PreimageRequests[0].ReceiverIdentityPubkey)
	require.Equal(t, spark.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_WAITING_FOR_PREIMAGE, htlcs.PreimageRequests[0].Status)
	require.Equal(t, int64(-1), htlcs.Offset)

	status2 := spark.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_PREIMAGE_SHARED
	htlcs2, err := wallet.QueryHTLC(t.Context(), userConfig, 5, 0, nil, &status2)
	require.NoError(t, err, "failed to query htlcs")
	require.Empty(t, htlcs2.PreimageRequests)
	require.Equal(t, int64(-1), htlcs2.Offset)
}
