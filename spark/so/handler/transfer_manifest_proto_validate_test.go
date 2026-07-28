package handler

import (
	"math/rand/v2"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// protoc-gen-validate has no cross-field conditional, so any presence rule on
// manifest_hash_signature applies to every sender package and rejects the manifest-less
// requests every live v3 caller sends. Only the binding can enforce it.
func TestStartTransferV3Request_ManifestFieldsAreOptional(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	sender := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	const transferID = "11111111-1111-1111-1111-111111111111"

	senderPackage := func(manifestSig []byte) *pb.SenderTransferPackage {
		return &pb.SenderTransferPackage{
			OwnerIdentityPublicKey:     sender.Serialize(),
			TransferPackage:            &pb.TransferPackage{},
			ReceiverIdentityPublicKeys: map[string][]byte{"leaf": receiver.Serialize()},
			ManifestHashSignature:      manifestSig,
		}
	}

	tests := []struct {
		name string
		req  *pb.StartTransferV3Request
	}{
		{
			"no manifest and no signature",
			&pb.StartTransferV3Request{
				TransferId:     transferID,
				SenderPackages: []*pb.SenderTransferPackage{senderPackage(nil)},
			},
		},
		{
			"manifest with signature",
			&pb.StartTransferV3Request{
				TransferId:     transferID,
				SenderPackages: []*pb.SenderTransferPackage{senderPackage([]byte{0x30, 0x44})},
				TransferManifest: &pb.TransferManifest{
					Version:    1,
					TransferId: transferID,
					Network:    pb.Network_REGTEST,
					Edges: []*pb.ManifestEdge{{
						SenderIdentityPublicKey:   sender.Serialize(),
						ReceiverIdentityPublicKey: receiver.Serialize(),
						Amount:                    &pb.ManifestAmount{Amount: &pb.ManifestAmount_Sats{Sats: 1000}},
					}},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, tc.req.Validate())
		})
	}
}

// Embedding the manifest in a live request promotes TransferManifest's own rules to
// boundary rejections for the first time — the message rode inside no request before.
func TestStartTransferV3Request_RejectsMalformedManifest(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	sender := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	const transferID = "11111111-1111-1111-1111-111111111111"

	validManifest := func() *pb.TransferManifest {
		return &pb.TransferManifest{
			Version:    1,
			TransferId: transferID,
			Network:    pb.Network_REGTEST,
			Edges: []*pb.ManifestEdge{{
				SenderIdentityPublicKey:   sender.Serialize(),
				ReceiverIdentityPublicKey: receiver.Serialize(),
				Amount:                    &pb.ManifestAmount{Amount: &pb.ManifestAmount_Sats{Sats: 1000}},
			}},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*pb.TransferManifest)
		wantErr bool
	}{
		{"well-formed", func(*pb.TransferManifest) {}, false},
		{"zero version", func(m *pb.TransferManifest) { m.Version = 0 }, true},
		{"non-uuid transfer id", func(m *pb.TransferManifest) { m.TransferId = "not-a-uuid" }, true},
		{"unspecified network", func(m *pb.TransferManifest) { m.Network = pb.Network_UNSPECIFIED }, true},
		{"no edges", func(m *pb.TransferManifest) { m.Edges = nil }, true},
		{"uncompressed edge sender key", func(m *pb.TransferManifest) {
			m.Edges[0].SenderIdentityPublicKey = make([]byte, 32)
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manifest := validManifest()
			tc.mutate(manifest)
			req := &pb.StartTransferV3Request{
				TransferId: transferID,
				SenderPackages: []*pb.SenderTransferPackage{{
					OwnerIdentityPublicKey:     sender.Serialize(),
					TransferPackage:            &pb.TransferPackage{},
					ReceiverIdentityPublicKeys: map[string][]byte{"leaf": receiver.Serialize()},
				}},
				TransferManifest: manifest,
			}
			err := req.Validate()
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			// Pin that the parent recursed into the manifest rather than some
			// unrelated rule firing.
			require.ErrorContains(t, err, "TransferManifest")
		})
	}
}

// A sender must never be told a transfer was bound when nothing verified it. The guard runs
// before any DB work, so no database setup is needed.
func TestStartTransferV3_ManifestMaterialIsRefused(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{60})
	sender := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	second := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	// A manifest that survives ValidationInterceptor, so these are shapes the handler
	// can actually be reached with rather than ones rejected an interceptor earlier.
	wellFormed := func(transferID string) *pb.TransferManifest {
		return &pb.TransferManifest{
			Version:    1,
			TransferId: transferID,
			Network:    pb.Network_REGTEST,
			Edges: []*pb.ManifestEdge{{
				SenderIdentityPublicKey:   sender.Serialize(),
				ReceiverIdentityPublicKey: receiver.Serialize(),
				Amount:                    &pb.ManifestAmount{Amount: &pb.ManifestAmount_Sats{Sats: 1000}},
			}},
		}
	}

	receivers := func(n int) map[string][]byte {
		m := map[string][]byte{"leaf-1": receiver.Serialize()}
		if n > 1 {
			m["leaf-2"] = second.Serialize()
		}
		return m
	}

	request := func(withManifest bool, manifestSig []byte, receiverCount int) *pb.StartTransferV3Request {
		transferID := uuid.New().String()
		var manifest *pb.TransferManifest
		if withManifest {
			manifest = wellFormed(transferID)
		}
		return &pb.StartTransferV3Request{
			TransferId: transferID,
			SenderPackages: []*pb.SenderTransferPackage{{
				OwnerIdentityPublicKey:     sender.Serialize(),
				TransferPackage:            &pb.TransferPackage{},
				ReceiverIdentityPublicKeys: receivers(receiverCount),
				ManifestHashSignature:      manifestSig,
			}},
			TransferManifest: manifest,
		}
	}

	tests := []struct {
		name       string
		req        *pb.StartTransferV3Request
		wantRefuse bool
	}{
		{"neither", request(false, nil, 1), false},
		{"manifest only", request(true, nil, 1), true},
		{"signature only", request(false, []byte{0x30, 0x44}, 1), true},
		{"both", request(true, []byte{0x30, 0x44}, 1), true},
		// The knob is off here, so Unimplemented proves the refusal ran before the
		// receiver gate, which would have returned FailedPrecondition.
		{"manifest with multiple receivers", request(true, nil, 2), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := handler.StartTransferV3(t.Context(), tc.req)
			require.Error(t, err, "every case fails eventually; only the reason differs")
			if tc.wantRefuse {
				assert.Equal(t, codes.Unimplemented, status.Code(err))
				assert.Contains(t, err.Error(), "transfer manifest binding is not yet implemented")
				return
			}
			// Passes the guard and fails further in, where real work begins.
			assert.NotEqual(t, codes.Unimplemented, status.Code(err))
			assert.NotContains(t, err.Error(), "transfer manifest binding")
		})
	}
}

func TestInitiatePreimageSwapV4Request_ValidatesInput(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	sender := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	paymentHash := make([]byte, 32)
	const transferID = "11111111-1111-1111-1111-111111111111"

	transferRequest := func(manifestVersion uint32) *pb.StartTransferV3Request {
		return &pb.StartTransferV3Request{
			TransferId: transferID,
			SenderPackages: []*pb.SenderTransferPackage{{
				OwnerIdentityPublicKey:     sender.Serialize(),
				TransferPackage:            &pb.TransferPackage{},
				ReceiverIdentityPublicKeys: map[string][]byte{"leaf": receiver.Serialize()},
			}},
			TransferManifest: &pb.TransferManifest{
				Version:    manifestVersion,
				TransferId: transferID,
				Network:    pb.Network_REGTEST,
				Edges: []*pb.ManifestEdge{{
					SenderIdentityPublicKey:   sender.Serialize(),
					ReceiverIdentityPublicKey: receiver.Serialize(),
					Amount:                    &pb.ManifestAmount{Amount: &pb.ManifestAmount_Sats{Sats: 1000}},
				}},
			},
		}
	}

	tests := []struct {
		name    string
		req     *pb.InitiatePreimageSwapV4Request
		wantErr bool
	}{
		{
			"valid",
			&pb.InitiatePreimageSwapV4Request{PaymentHash: paymentHash, CounterpartyIdentityPublicKey: receiver.Serialize(), TransferV3Request: transferRequest(1)},
			false,
		},
		{
			"missing payment hash",
			&pb.InitiatePreimageSwapV4Request{CounterpartyIdentityPublicKey: receiver.Serialize(), TransferV3Request: transferRequest(1)},
			true,
		},
		{
			"short payment hash",
			&pb.InitiatePreimageSwapV4Request{PaymentHash: make([]byte, 31), CounterpartyIdentityPublicKey: receiver.Serialize(), TransferV3Request: transferRequest(1)},
			true,
		},
		{
			"missing counterparty",
			&pb.InitiatePreimageSwapV4Request{PaymentHash: paymentHash, TransferV3Request: transferRequest(1)},
			true,
		},
		{
			"uncompressed counterparty",
			&pb.InitiatePreimageSwapV4Request{PaymentHash: paymentHash, CounterpartyIdentityPublicKey: make([]byte, 65), TransferV3Request: transferRequest(1)},
			true,
		},
		{
			"missing transfer request",
			&pb.InitiatePreimageSwapV4Request{PaymentHash: paymentHash, CounterpartyIdentityPublicKey: receiver.Serialize()},
			true,
		},
		{
			"embedded transfer request with malformed manifest",
			&pb.InitiatePreimageSwapV4Request{PaymentHash: paymentHash, CounterpartyIdentityPublicKey: receiver.Serialize(), TransferV3Request: transferRequest(0)},
			true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantErr {
				require.Error(t, tc.req.Validate())
			} else {
				require.NoError(t, tc.req.Validate())
			}
		})
	}
}
