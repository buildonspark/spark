package ent

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

func TestMarshalProto_V3_SortsOperatorKeysAndInvoices(t *testing.T) {
	// Create two deterministic public keys
	k1, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to generate private key 1: %v", err)
	}
	k2, err := keys.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to generate private key 2: %v", err)
	}

	// Build operator map in non-deterministic order
	cfg := &so.Config{
		SigningOperatorMap: map[string]*so.SigningOperator{
			"02": {ID: 2, Identifier: "02", IdentityPublicKey: k2.Public()},
			"01": {ID: 1, Identifier: "01", IdentityPublicKey: k1.Public()},
		},
	}

	// Construct a minimal v3 ent transaction with outputs and invoices in unsorted order
	tx := &TokenTransaction{
		Version:    3,
		ExpiryTime: time.Now(),
		Edges: TokenTransactionEdges{
			CreatedOutput: []*TokenOutput{
				{
					ID:                           uuid.New(),
					CreatedTransactionOutputVout: 0,
					Network:                      st.NetworkMainnet,
				},
			},
			SparkInvoice: []*SparkInvoice{
				{SparkInvoice: "inv-b"},
				{SparkInvoice: "inv-a"},
				{SparkInvoice: "inv-c"},
			},
		},
	}
	// Set status to Started so MarshalProto doesn't require mapping inputs
	tx.Status = st.TokenTransactionStatusStarted

	protoTx, err := tx.MarshalProto(t.Context(), cfg)
	if err != nil {
		t.Fatalf("MarshalProto failed: %v", err)
	}

	// Verify operator keys are sorted byte-wise ascending
	gotOps := protoTx.GetSparkOperatorIdentityPublicKeys()
	if len(gotOps) != 2 {
		t.Fatalf("unexpected operator keys len %d", len(gotOps))
	}
	// Compute expected sorted order from serialized keys
	k1b := k1.Public().Serialize()
	k2b := k2.Public().Serialize()
	expectedOps := [][]byte{k1b, k2b}
	if bytes.Compare(expectedOps[0], expectedOps[1]) > 0 {
		expectedOps[0], expectedOps[1] = expectedOps[1], expectedOps[0]
	}
	if !reflect.DeepEqual(gotOps, expectedOps) {
		t.Fatalf("operator keys not sorted as expected\n got: %x\nwant: %x", gotOps, expectedOps)
	}

	// Verify invoices sorted lexicographically by string
	gotInv := protoTx.GetInvoiceAttachments()
	if len(gotInv) != 3 {
		t.Fatalf("unexpected invoice attachments len %d", len(gotInv))
	}
	wantInv := []string{"inv-a", "inv-b", "inv-c"}
	for i, s := range wantInv {
		if gotInv[i].GetSparkInvoice() != s {
			t.Fatalf("invoice order mismatch at %d: got %s want %s", i, gotInv[i].GetSparkInvoice(), s)
		}
	}
}
