package grpctest

import (
	"context"
	"testing"

	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
	"github.com/stretchr/testify/assert"
)

func TestCreateLightningInvoice(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatal(err)
	}

	fakeInvoiceCreator := wallet.FakeLightningInvoiceCreator{}

	invoice, err := wallet.CreateLightningInvoice(context.Background(), config, &fakeInvoiceCreator, 100, "test")
	if err != nil {
		t.Fatal(err)
	}

	assert.NotNil(t, invoice)
}
