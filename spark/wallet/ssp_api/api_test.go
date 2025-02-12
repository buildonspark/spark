package sspapi

import (
	"crypto/sha256"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/lightsparkdev/spark-go/common"
)

func TestCreateInvoice(t *testing.T) {
	identityPublicKeyString := "03bead4a092468d96dee7723cc8f18c52b194a14a3a3cf722ef99d7b518c4cf236"
	requester, err := NewRequesterWithBaseURL(identityPublicKeyString, nil)
	if err != nil {
		t.Fatal(err)
	}

	preimage, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	paymentHash := sha256.Sum256(preimage.Serialize())

	api := NewSparkServiceAPI(requester)

	invoice, fees, err := api.CreateInvoice(common.Regtest, 1000, paymentHash[:], "test", 600)
	if err != nil {
		t.Fatal(err)
	}

	println(*invoice)
	println(fees)
}
