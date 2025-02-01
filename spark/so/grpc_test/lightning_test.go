package grpctest

import (
	"context"
	"encoding/hex"
	"testing"

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

	invoice, err := wallet.CreateLightningInvoiceWithPreimage(context.Background(), config, fakeInvoiceCreator, 100, "test", preimage)
	if err != nil {
		t.Fatal(err)
	}

	for _, operator := range config.SigningOperators {
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			t.Fatal(err)
		}
		mockClient := pbmock.NewMockServiceClient(conn)
		_, err = mockClient.CleanUpPreimageShare(context.Background(), &pbmock.CleanUpPreimageShareRequest{
			PaymentHash: preimage,
		})
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}

	assert.NotNil(t, invoice)
}
