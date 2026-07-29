package ent

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

func TestMarshalProto_V3_SortsOperatorKeysAndInvoices(t *testing.T) {
	// Create two deterministic public keys
	k1 := keys.GeneratePrivateKey()
	k2 := keys.GeneratePrivateKey()

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
					Network:                      btcnetwork.Mainnet,
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
	actualOps := protoTx.GetSparkOperatorIdentityPublicKeys()
	if len(actualOps) != 2 {
		t.Fatalf("unexpected operator keys len %d", len(actualOps))
	}
	// Compute expected sorted order from serialized keys
	k1b := k1.Public().Serialize()
	k2b := k2.Public().Serialize()
	expectedOps := [][]byte{k1b, k2b}
	if bytes.Compare(expectedOps[0], expectedOps[1]) > 0 {
		expectedOps[0], expectedOps[1] = expectedOps[1], expectedOps[0]
	}
	if !reflect.DeepEqual(actualOps, expectedOps) {
		t.Fatalf("operator keys not sorted as expected\n got: %x\nwant: %x", actualOps, expectedOps)
	}

	// Verify invoices sorted lexicographically by string
	actualInv := protoTx.GetInvoiceAttachments()
	if len(actualInv) != 3 {
		t.Fatalf("unexpected invoice attachments len %d", len(actualInv))
	}
	expectedInv := []string{"inv-a", "inv-b", "inv-c"}
	for i, s := range expectedInv {
		if actualInv[i].GetSparkInvoice() != s {
			t.Fatalf("invoice order mismatch at %d: got %s want %s", i, actualInv[i].GetSparkInvoice(), s)
		}
	}
}

func TestSelectPartialTokenTransactionForHashPrefersFinalizedOverRevealed(t *testing.T) {
	now := time.Now()
	revealed := &TokenTransaction{
		ID:         uuid.New(),
		CreateTime: now,
		Status:     st.TokenTransactionStatusRevealed,
		Edges: TokenTransactionEdges{
			SpentOutput: []*TokenOutput{{ID: uuid.New()}},
		},
	}
	finalized := &TokenTransaction{
		ID:         uuid.New(),
		CreateTime: now.Add(-time.Minute),
		Status:     st.TokenTransactionStatusFinalized,
		Edges: TokenTransactionEdges{
			SpentOutput: []*TokenOutput{{ID: uuid.New()}},
		},
	}

	selected := selectPartialTokenTransactionForHash([]*TokenTransaction{revealed, finalized}, now)
	if selected != finalized {
		t.Fatalf("expected finalized transaction, got status %s", selected.Status)
	}
}

func TestSelectPartialTokenTransactionForHashPrefersTerminalWithTypeEdge(t *testing.T) {
	now := time.Now()
	withoutTypeEdge := &TokenTransaction{
		ID:         uuid.New(),
		CreateTime: now,
		Status:     st.TokenTransactionStatusFinalized,
	}
	withTypeEdge := &TokenTransaction{
		ID:         uuid.New(),
		CreateTime: now.Add(-time.Minute),
		Status:     st.TokenTransactionStatusFinalized,
		Edges: TokenTransactionEdges{
			SpentOutput: []*TokenOutput{{ID: uuid.New()}},
		},
	}

	selected := selectPartialTokenTransactionForHash([]*TokenTransaction{withoutTypeEdge, withTypeEdge}, now)
	if selected != withTypeEdge {
		t.Fatalf("expected terminal transaction with type edge, got %s", selected.ID)
	}
}

func TestSelectPartialTokenTransactionForHashPrefersAnchoredTerminalBeforeUnanchoredFinalized(t *testing.T) {
	now := time.Now()
	unanchoredFinalized := &TokenTransaction{
		ID:         uuid.New(),
		CreateTime: now,
		Status:     st.TokenTransactionStatusFinalized,
	}
	anchoredRevealed := &TokenTransaction{
		ID:         uuid.New(),
		CreateTime: now.Add(-time.Minute),
		Status:     st.TokenTransactionStatusRevealed,
		Edges: TokenTransactionEdges{
			SpentOutput: []*TokenOutput{{ID: uuid.New()}},
		},
	}

	selected := selectPartialTokenTransactionForHash([]*TokenTransaction{unanchoredFinalized, anchoredRevealed}, now)
	if selected != anchoredRevealed {
		t.Fatalf("expected anchored terminal transaction, got status %s", selected.Status)
	}
}
