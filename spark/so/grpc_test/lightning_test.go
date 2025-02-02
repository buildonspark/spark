package grpctest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	pbmock "github.com/lightsparkdev/spark-go/proto/mock"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
	"github.com/stretchr/testify/assert"
)

// FakeLightningInvoiceCreator is a fake implementation of the LightningInvoiceCreator interface.
type FakeLightningInvoiceCreator struct{}

// CreateInvoice is a fake implementation of the LightningInvoiceCreator interface.
// It returns a fake invoice string.
func (f *FakeLightningInvoiceCreator) CreateInvoice(_ uint64, _ []byte, _ string) (*string, error) {
	invoice := "lnbcrt123450n1pnj6uf4pp5l26hsdxssmr52vd4xmn5xran7puzx34hpr6uevaq7ta0ayzrp8esdqqcqzpgxqyz5vqrzjqtr2vd60g57hu63rdqk87u3clac6jlfhej4kldrrjvfcw3mphcw8sqqqqzp3jlj6zyqqqqqqqqqqqqqq9qsp5w22fd8aqn7sdum7hxdf59ptgk322fkv589ejxjltngvgehlcqcyq9qxpqysgqvykwsxdx64qrj0s5pgcgygmrpj8w25jsjgltwn09yp24l9nvghe3dl3y0ycy70ksrlqmcn42hxn24e0ucuy3g9fjltudvhv4lrhhamgq3stqgp"
	return &invoice, nil
}

func cleanUp(t *testing.T, config *wallet.Config, paymentHash []byte) {
	for _, operator := range config.SigningOperators {
		conn, err := common.NewGRPCConnection(operator.Address)
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

	invoice, err := wallet.CreateLightningInvoiceWithPreimage(context.Background(), config, fakeInvoiceCreator, 100, "test", preimage)
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

	invoice, err := wallet.CreateLightningInvoiceWithPreimage(context.Background(), userConfig, fakeInvoiceCreator, 100, "test", preimage)
	if err != nil {
		t.Fatal(err)
	}
	assert.NotNil(t, invoice)

	// SSP creates a node of 12345 sats
	sspLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	nodeToSend, err := testutil.CreateNewTree(sspConfig, sspLeafPrivKey, 12345)
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

	receivedPreimage, err := wallet.SwapNodesForPreimage(
		context.Background(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		nil,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, receivedPreimage, preimage)
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
	nodeToSend, err := testutil.CreateNewTree(userConfig, userLeafPrivKey, 12345)
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

	_, err = wallet.SwapNodesForPreimage(
		context.Background(),
		userConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

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
	assert.Equal(t, totalValue, int64(12345))
}
