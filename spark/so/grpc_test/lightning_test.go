package grpctest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	pbmock "github.com/lightsparkdev/spark-go/proto/mock"
	"github.com/lightsparkdev/spark-go/proto/spark"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
	"github.com/stretchr/testify/assert"
)

// FakeLightningInvoiceCreator is a fake implementation of the LightningInvoiceCreator interface.
type FakeLightningInvoiceCreator struct{}

// CreateInvoice is a fake implementation of the LightningInvoiceCreator interface.
// It returns a fake invoice string.
func (f *FakeLightningInvoiceCreator) CreateInvoice(_ common.Network, _ uint64, _ []byte, _ string, _ int) (*string, int64, error) {
	invoice := "lnbcrt123450n1pnj6uf4pp5l26hsdxssmr52vd4xmn5xran7puzx34hpr6uevaq7ta0ayzrp8esdqqcqzpgxqyz5vqrzjqtr2vd60g57hu63rdqk87u3clac6jlfhej4kldrrjvfcw3mphcw8sqqqqzp3jlj6zyqqqqqqqqqqqqqq9qsp5w22fd8aqn7sdum7hxdf59ptgk322fkv589ejxjltngvgehlcqcyq9qxpqysgqvykwsxdx64qrj0s5pgcgygmrpj8w25jsjgltwn09yp24l9nvghe3dl3y0ycy70ksrlqmcn42hxn24e0ucuy3g9fjltudvhv4lrhhamgq3stqgp"
	return &invoice, 100, nil
}

func cleanUp(t *testing.T, config *wallet.Config, paymentHash []byte) {
	for _, operator := range config.SigningOperators {
		conn, err := common.NewGRPCConnectionWithTestTLS(operator.Address)
		if err != nil {
			t.Fatal(err)
		}
		mockClient := pbmock.NewMockServiceClient(conn)
		_, err = mockClient.CleanUpPreimageShare(context.Background(), &pbmock.CleanUpPreimageShareRequest{
			PaymentHash: paymentHash,
		})
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}
}

func TestCreateLightningInvoice(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatal(err)
	}

	fakeInvoiceCreator := &FakeLightningInvoiceCreator{}

	preimage, err := hex.DecodeString("2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c")
	if err != nil {
		t.Fatal(err)
	}
	paymentHash := sha256.Sum256(preimage)

	invoice, _, err := wallet.CreateLightningInvoiceWithPreimage(context.Background(), config, fakeInvoiceCreator, 100, "test", preimage)
	if err != nil {
		t.Fatal(err)
	}
	assert.NotNil(t, invoice)

	cleanUp(t, config, paymentHash[:])
}

func TestReceiveLightningPayment(t *testing.T) {
	// Create user and ssp configs
	userConfig, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatal(err)
	}

	sspConfig, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatal(err)
	}

	// User creates an invoice
	preimage, err := hex.DecodeString("2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c")
	if err != nil {
		t.Fatal(err)
	}
	paymentHash := sha256.Sum256(preimage)
	fakeInvoiceCreator := &FakeLightningInvoiceCreator{}

	defer cleanUp(t, userConfig, paymentHash[:])

	invoice, _, err := wallet.CreateLightningInvoiceWithPreimage(context.Background(), userConfig, fakeInvoiceCreator, 100, "test", preimage)
	if err != nil {
		t.Fatal(err)
	}
	assert.NotNil(t, invoice)

	// SSP creates a node of 12345 sats
	sspLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	feeSats := uint64(2)
	nodeToSend, err := testutil.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 12343)
	if err != nil {
		t.Fatal(err)
	}

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}

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
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, response.Preimage, preimage)
	senderTransfer := response.Transfer

	transfer, err := wallet.SendTransferTweakKey(context.Background(), sspConfig, response.Transfer, leaves, nil)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, transfer.Status, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED)

	receiverToken, err := wallet.AuthenticateWithServer(context.Background(), userConfig)
	if err != nil {
		t.Fatalf("failed to authenticate receiver: %v", err)
	}
	receiverCtx := wallet.ContextWithToken(context.Background(), receiverToken)
	pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, userConfig)
	if err != nil {
		t.Fatalf("failed to query pending transfers: %v", err)
	}
	if len(pendingTransfer.Transfers) != 1 {
		t.Fatalf("expected 1 pending transfer, got %d", len(pendingTransfer.Transfers))
	}
	receiverTransfer := pendingTransfer.Transfers[0]
	if receiverTransfer.Id != senderTransfer.Id {
		t.Fatalf("expected transfer id %s, got %s", senderTransfer.Id, receiverTransfer.Id)
	}

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), userConfig, receiverTransfer)
	if err != nil {
		t.Fatalf("unable to verify pending transfer: %v", err)
	}
	if len(*leafPrivKeyMap) != 1 {
		t.Fatalf("Expected 1 leaf to transfer, got %d", len(*leafPrivKeyMap))
	}
	if !bytes.Equal((*leafPrivKeyMap)[nodeToSend.Id], newLeafPrivKey.Serialize()) {
		t.Fatalf("wrong leaf signing private key")
	}

	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}
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
	if err != nil {
		t.Fatalf("failed to ClaimTransfer: %v", err)
	}
}

func TestSendLightningPayment(t *testing.T) {
	// Create user and ssp configs
	userConfig, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatal(err)
	}

	sspConfig, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatal(err)
	}

	// User creates an invoice
	preimage, err := hex.DecodeString("2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c")
	if err != nil {
		t.Fatal(err)
	}
	paymentHash := sha256.Sum256(preimage)
	invoice := "lnbcrt123450n1pnj6uf4pp5l26hsdxssmr52vd4xmn5xran7puzx34hpr6uevaq7ta0ayzrp8esdqqcqzpgxqyz5vqrzjqtr2vd60g57hu63rdqk87u3clac6jlfhej4kldrrjvfcw3mphcw8sqqqqzp3jlj6zyqqqqqqqqqqqqqq9qsp5w22fd8aqn7sdum7hxdf59ptgk322fkv589ejxjltngvgehlcqcyq9qxpqysgqvykwsxdx64qrj0s5pgcgygmrpj8w25jsjgltwn09yp24l9nvghe3dl3y0ycy70ksrlqmcn42hxn24e0ucuy3g9fjltudvhv4lrhhamgq3stqgp"

	defer cleanUp(t, userConfig, paymentHash[:])

	// User creates a node of 12345 sats
	userLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	feeSats := uint64(2)
	nodeToSend, err := testutil.CreateNewTree(userConfig, faucet, userLeafPrivKey, 12347)
	if err != nil {
		t.Fatal(err)
	}

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}

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
	if err != nil {
		t.Fatal(err)
	}

	transfer, err := wallet.SendTransferTweakKey(context.Background(), userConfig, response.Transfer, leaves, nil)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, transfer.Status, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING)

	refunds, err := wallet.QueryUserSignedRefunds(context.Background(), sspConfig, paymentHash[:])
	if err != nil {
		t.Fatal(err)
	}

	var totalValue int64
	for _, refund := range refunds {
		value, err := wallet.ValidateUserSignedRefund(refund)
		if err != nil {
			t.Fatal(err)
		}
		totalValue += value
	}
	assert.Equal(t, totalValue, int64(12345+feeSats))

	receiverTransfer, err := wallet.ProvidePreimage(context.Background(), sspConfig, preimage)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, receiverTransfer.Status, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED)

	receiverToken, err := wallet.AuthenticateWithServer(context.Background(), sspConfig)
	if err != nil {
		t.Fatalf("failed to authenticate receiver: %v", err)
	}
	receiverCtx := wallet.ContextWithToken(context.Background(), receiverToken)
	if receiverTransfer.Id != transfer.Id {
		t.Fatalf("expected transfer id %s, got %s", transfer.Id, receiverTransfer.Id)
	}

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), sspConfig, receiverTransfer)
	if err != nil {
		t.Fatalf("unable to verify pending transfer: %v", err)
	}
	if len(*leafPrivKeyMap) != 1 {
		t.Fatalf("Expected 1 leaf to transfer, got %d", len(*leafPrivKeyMap))
	}
	if !bytes.Equal((*leafPrivKeyMap)[nodeToSend.Id], newLeafPrivKey.Serialize()) {
		t.Fatalf("wrong leaf signing private key")
	}

	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}
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
	if err != nil {
		t.Fatalf("failed to ClaimTransfer: %v", err)
	}
}

func TestSendLightningPaymentWithRejection(t *testing.T) {
	// Create user and ssp configs
	userConfig, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatal(err)
	}

	sspConfig, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatal(err)
	}

	// User creates an invoice
	preimage, err := hex.DecodeString("2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c")
	if err != nil {
		t.Fatal(err)
	}
	paymentHash := sha256.Sum256(preimage)
	invoice := "lnbcrt123450n1pnj6uf4pp5l26hsdxssmr52vd4xmn5xran7puzx34hpr6uevaq7ta0ayzrp8esdqqcqzpgxqyz5vqrzjqtr2vd60g57hu63rdqk87u3clac6jlfhej4kldrrjvfcw3mphcw8sqqqqzp3jlj6zyqqqqqqqqqqqqqq9qsp5w22fd8aqn7sdum7hxdf59ptgk322fkv589ejxjltngvgehlcqcyq9qxpqysgqvykwsxdx64qrj0s5pgcgygmrpj8w25jsjgltwn09yp24l9nvghe3dl3y0ycy70ksrlqmcn42hxn24e0ucuy3g9fjltudvhv4lrhhamgq3stqgp"

	defer cleanUp(t, userConfig, paymentHash[:])

	// User creates a node of 12345 sats
	userLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	feeSats := uint64(2)
	nodeToSend, err := testutil.CreateNewTree(userConfig, faucet, userLeafPrivKey, 12347)
	if err != nil {
		t.Fatal(err)
	}

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}

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
	if err != nil {
		t.Fatal(err)
	}

	transfer, err := wallet.SendTransferTweakKey(context.Background(), userConfig, response.Transfer, leaves, nil)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, transfer.Status, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING)

	refunds, err := wallet.QueryUserSignedRefunds(context.Background(), sspConfig, paymentHash[:])
	if err != nil {
		t.Fatal(err)
	}

	var totalValue int64
	for _, refund := range refunds {
		value, err := wallet.ValidateUserSignedRefund(refund)
		if err != nil {
			t.Fatal(err)
		}
		totalValue += value
	}
	assert.Equal(t, totalValue, int64(12345+feeSats))

	err = wallet.ReturnLightningPayment(context.Background(), sspConfig, paymentHash[:])
	if err != nil {
		t.Fatal(err)
	}
}
