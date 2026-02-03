package grpctest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	decodepay "github.com/nbd-wtf/ln-decodepay"

	pbmock "github.com/lightsparkdev/spark/proto/mock"
	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
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
func (f *FakeLightningInvoiceCreator) CreateInvoice(_ context.Context, _ btcnetwork.Network, amountSats int64, _ []byte, _ string, _ time.Duration) (string, error) {
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
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{nodeToSend.Id: newLeafPrivKey}, leafPrivKeyMap)

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
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{nodeToSend.Id: newLeafPrivKey}, leafPrivKeyMap)

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
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{nodeToSend.Id: newLeafPrivKey}, leafPrivKeyMap)

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
	htlcs, err := wallet.QueryHTLC(t.Context(), sspConfig, 5, 0, nil, nil, nil, nil)
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
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{nodeToSend.Id: newLeafPrivKey}, leafPrivKeyMap)

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
	require.Empty(t, transfers)

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
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{nodeToSend.Id: newLeafPrivKey}, leafPrivKeyMap)

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
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{nodeToSend.Id: newLeafPrivKey}, leafPrivKeyMap)

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

	htlcs, err := wallet.QueryHTLC(t.Context(), userConfig, 100, 0, nil, nil, nil, nil)
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

	htlcs, err := wallet.QueryHTLC(t.Context(), userConfig, 5, 0, nil, nil, nil, nil)
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

	htlcs, err := wallet.QueryHTLC(t.Context(), userConfig, 5, 0, [][]byte{paymentHash[:]}, nil, nil, nil)
	require.NoError(t, err, "failed to query htlcs")
	require.Len(t, htlcs.PreimageRequests, 1)
	require.Equal(t, paymentHash[:], htlcs.PreimageRequests[0].PaymentHash)
	require.Equal(t, userConfig.IdentityPublicKey().Serialize(), htlcs.PreimageRequests[0].ReceiverIdentityPubkey)
	require.Equal(t, spark.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_WAITING_FOR_PREIMAGE, htlcs.PreimageRequests[0].Status)
	require.Equal(t, int64(-1), htlcs.Offset)
}

func TestQueryHTLCWithStatusFilter(t *testing.T) {
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
	htlcs, err := wallet.QueryHTLC(t.Context(), userConfig, 5, 0, nil, &status, nil, nil)
	require.NoError(t, err, "failed to query htlcs")
	require.Len(t, htlcs.PreimageRequests, 1)
	require.Equal(t, paymentHash[:], htlcs.PreimageRequests[0].PaymentHash)
	require.Equal(t, userConfig.IdentityPublicKey().Serialize(), htlcs.PreimageRequests[0].ReceiverIdentityPubkey)
	require.Equal(t, spark.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_WAITING_FOR_PREIMAGE, htlcs.PreimageRequests[0].Status)
	require.Equal(t, int64(-1), htlcs.Offset)

	status2 := spark.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_PREIMAGE_SHARED
	htlcs2, err := wallet.QueryHTLC(t.Context(), userConfig, 5, 0, nil, &status2, nil, nil)
	require.NoError(t, err, "failed to query htlcs")
	require.Empty(t, htlcs2.PreimageRequests)
	require.Equal(t, int64(-1), htlcs2.Offset)
}

func TestQueryHTLCWithTransferIdFilter(t *testing.T) {
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

	transferId := response.Transfer.Id

	// status := spark.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_WAITING_FOR_PREIMAGE
	htlcs, err := wallet.QueryHTLC(t.Context(), userConfig, 5, 0, nil, nil, []string{transferId}, nil)
	require.NoError(t, err, "failed to query htlcs")
	require.Len(t, htlcs.PreimageRequests, 1)
	require.Equal(t, paymentHash[:], htlcs.PreimageRequests[0].PaymentHash)
	require.Equal(t, userConfig.IdentityPublicKey().Serialize(), htlcs.PreimageRequests[0].ReceiverIdentityPubkey)
	require.Equal(t, spark.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_WAITING_FOR_PREIMAGE, htlcs.PreimageRequests[0].Status)
	require.Equal(t, transferId, htlcs.PreimageRequests[0].Transfer.Id)
	require.Equal(t, int64(-1), htlcs.Offset)

	htlcs2, err := wallet.QueryHTLC(t.Context(), userConfig, 5, 0, nil, nil, []string{}, nil)
	require.NoError(t, err, "failed to query htlcs")
	require.Len(t, htlcs2.PreimageRequests, 1)

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

	transferId2 := response2.Transfer.Id

	htlcs3, err := wallet.QueryHTLC(t.Context(), userConfig, 5, 0, nil, nil, []string{transferId, transferId2}, nil)
	require.NoError(t, err, "failed to query htlcs")
	require.Len(t, htlcs3.PreimageRequests, 2)
}

func TestQueryHTLCWithRoleFilter(t *testing.T) {
	// Create user and ssp configs
	userConfig := wallet.NewTestWalletConfig(t)
	receiverConfig := wallet.NewTestWalletConfig(t)

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
		receiverConfig.IdentityPublicKey(),
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

	transferId := response.Transfer.Id

	role := spark.PreimageRequestRole_PREIMAGE_REQUEST_ROLE_RECEIVER
	htlcs, err := wallet.QueryHTLC(t.Context(), userConfig, 5, 0, nil, nil, nil, &role)
	require.NoError(t, err, "failed to query htlcs")
	require.Empty(t, htlcs.PreimageRequests)

	senderRole := spark.PreimageRequestRole_PREIMAGE_REQUEST_ROLE_SENDER

	htlcs2, err := wallet.QueryHTLC(t.Context(), userConfig, 5, 0, nil, nil, nil, &senderRole)
	require.NoError(t, err, "failed to query htlcs")
	require.Len(t, htlcs2.PreimageRequests, 1)
	require.Equal(t, paymentHash[:], htlcs2.PreimageRequests[0].PaymentHash)
	require.Equal(t, receiverConfig.IdentityPublicKey().Serialize(), htlcs2.PreimageRequests[0].ReceiverIdentityPubkey)
	require.Equal(t, spark.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_WAITING_FOR_PREIMAGE, htlcs2.PreimageRequests[0].Status)
	require.Equal(t, transferId, htlcs2.PreimageRequests[0].Transfer.Id)
	require.Equal(t, int64(-1), htlcs2.Offset)

	// Test a second htlc by swapping the receiver and sender role
	amountSats2 := uint64(2000)
	preimage2, err := hex.DecodeString("02")
	require.NoError(t, err)
	paymentHash2 := sha256.Sum256(preimage2)

	defer cleanUp(t, receiverConfig, paymentHash2)

	userLeafPrivKey2 := keys.GeneratePrivateKey()
	nodeToSend2, err := wallet.CreateNewTree(receiverConfig, faucet, userLeafPrivKey2, 2000)
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
		receiverConfig,
		leaves2,
		userConfig.IdentityPublicKey(),
		paymentHash2[:],
		nil,
		feeSats,
		false,
		amountSats2,
	)
	require.NoError(t, err)

	transfer2, err := wallet.DeliverTransferPackage(t.Context(), receiverConfig, response2.Transfer, leaves2, nil)
	require.NoError(t, err)
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING, transfer2.Status)

	receiverAndSenderRole := spark.PreimageRequestRole_PREIMAGE_REQUEST_ROLE_RECEIVER_AND_SENDER
	htlcsReceiverAndSenderRole, err := wallet.QueryHTLC(t.Context(), userConfig, 5, 0, nil, nil, nil, &receiverAndSenderRole)
	require.NoError(t, err, "failed to query htlcs")
	require.Len(t, htlcsReceiverAndSenderRole.PreimageRequests, 2)
}

// TestReceiveLightningPaymentWithTransferRequest tests the lightning receive flow
// where TransferRequest is provided (non-HODL invoice with preimage available).
// This test verifies that:
// 1. settleSenderKeyTweaks is called to coordinate with other operators
// 2. commitSenderKeyTweaks is called to apply key tweaks locally
// 3. Transfer status becomes SENDER_KEY_TWEAKED
func TestReceiveLightningPaymentWithTransferRequest(t *testing.T) {
	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)

	amountSats := uint64(100)
	preimageHex := "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c"
	preimage, err := hex.DecodeString(preimageHex)
	require.NoError(t, err)
	paymentHash := sha256.Sum256(preimage)

	fakeInvoiceCreator := NewFakeLightningInvoiceCreator()
	defer cleanUp(t, userConfig, paymentHash)

	invoice, err := wallet.CreateLightningInvoiceWithPreimage(
		t.Context(),
		userConfig,
		fakeInvoiceCreator,
		amountSats,
		"test",
		[32]byte(preimage),
	)
	require.NoError(t, err)
	require.NotNil(t, invoice)

	sspLeafPrivKey := keys.GeneratePrivateKey()
	nodeToSend, err := wallet.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 12345)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()
	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    sspLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	conn, err := sspConfig.NewCoordinatorGRPCConnection()
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), sspConfig, conn)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	client := spark.NewSparkServiceClient(conn)

	transferID, err := uuid.NewV7()
	require.NoError(t, err)

	keyTweakInputMap, err := wallet.PrepareSendTransferKeyTweaks(
		sspConfig,
		transferID,
		userConfig.IdentityPublicKey(),
		leaves,
		map[string][]byte{},
	)
	require.NoError(t, err)

	transferPackage, err := wallet.PrepareTransferPackage(
		ctx,
		sspConfig,
		client,
		transferID,
		keyTweakInputMap,
		leaves,
		userConfig.IdentityPublicKey(),
		keys.Public{}, // No adaptor key for non-swap
	)
	require.NoError(t, err)

	userSignedLeavesToSend, err := wallet.PrepareUserSignedLeafSigningJobs(
		ctx,
		sspConfig,
		client,
		leaves,
		userConfig.IdentityPublicKey(),
		keys.Public{}, // No adaptor key for non-swap
	)
	require.NoError(t, err)

	response, err := client.InitiatePreimageSwapV2(ctx, &spark.InitiatePreimageSwapRequest{
		PaymentHash: paymentHash[:],
		Reason:      spark.InitiatePreimageSwapRequest_REASON_RECEIVE,
		InvoiceAmount: &spark.InvoiceAmount{
			InvoiceAmountProof: &spark.InvoiceAmountProof{
				Bolt11Invoice: invoice,
			},
			ValueSats: amountSats,
		},
		Transfer: &spark.StartUserSignedTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    sspConfig.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: userConfig.IdentityPublicKey().Serialize(),
			LeavesToSend:              userSignedLeavesToSend,
			ExpiryTime:                timestamppb.New(time.Now().Add(2 * time.Minute)),
		},
		TransferRequest: &spark.StartTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    sspConfig.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: userConfig.IdentityPublicKey().Serialize(),
			TransferPackage:           transferPackage,
			ExpiryTime:                timestamppb.New(time.Now().Add(2 * time.Minute)),
		},
		ReceiverIdentityPublicKey: userConfig.IdentityPublicKey().Serialize(),
		FeeSats:                   0,
	})

	require.NoError(t, err)
	require.NotNil(t, response)
	require.NotNil(t, response.Transfer)

	require.Equal(t, preimage, response.Preimage, "preimage should be returned for non-HODL invoice")
	assert.Equal(t,
		spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		response.Transfer.Status,
		"transfer status should be SENDER_KEY_TWEAKED after key tweak settlement",
	)
	assert.Equal(t, transferID.String(), response.Transfer.Id)
	assert.Equal(t, spark.TransferType_PREIMAGE_SWAP, response.Transfer.Type)
	require.Len(t, response.Transfer.Leaves, 1)
	assert.Equal(t, nodeToSend.Id, response.Transfer.Leaves[0].Leaf.Id)

	// Verify all operators have the same status (distributed consensus verification)
	for identifier, operator := range sspConfig.SigningOperators {
		operatorConn, err := operator.NewOperatorGRPCConnection()
		require.NoError(t, err, "failed to connect to operator %s", identifier)
		defer operatorConn.Close()

		operatorToken, err := wallet.AuthenticateWithConnection(t.Context(), sspConfig, operatorConn)
		require.NoError(t, err, "failed to authenticate with operator %s", identifier)
		operatorCtx := wallet.ContextWithToken(t.Context(), operatorToken)

		operatorClient := spark.NewSparkServiceClient(operatorConn)
		network, err := sspConfig.Network.ToProtoNetwork()
		require.NoError(t, err)

		response, err := operatorClient.QueryAllTransfers(operatorCtx, &spark.TransferFilter{
			Participant: &spark.TransferFilter_SenderOrReceiverIdentityPublicKey{
				SenderOrReceiverIdentityPublicKey: sspConfig.IdentityPublicKey().Serialize(),
			},
			Limit:   10,
			Offset:  0,
			Network: network,
		})
		require.NoError(t, err, "failed to query transfers from operator %s", identifier)

		var found bool
		for _, transfer := range response.Transfers {
			if transfer.Id == transferID.String() {
				assert.Equal(t,
					spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
					transfer.Status,
					"operator %s should have transfer status SENDER_KEY_TWEAKED",
					identifier,
				)
				found = true
				break
			}
		}
		assert.True(t, found, "operator %s should have the transfer in its database", identifier)
	}
}
