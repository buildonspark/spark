package handler

import (
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"math/rand/v2"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent"
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
func TestStartTransferV3_RejectsOnlyAStraySignature(t *testing.T) {
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

	// start_transfer_v3 no longer refuses a manifest — it binds one when supplied and demands
	// nothing. The only manifest shape it rejects outright is a signature with nothing to sign.
	tests := []struct {
		name     string
		req      *pb.StartTransferV3Request
		wantCode codes.Code
		wantMsg  string
	}{
		{"neither", request(false, nil, 1), codes.OK, ""},
		{"manifest only", request(true, nil, 1), codes.OK, ""},
		{"signature only", request(false, []byte{0x30, 0x44}, 1), codes.InvalidArgument, "manifest_hash_signature set without a transfer_manifest"},
		{"both", request(true, []byte{0x30, 0x44}, 1), codes.OK, ""},
		{"manifest with multiple receivers", request(true, nil, 2), codes.OK, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := handler.StartTransferV3(t.Context(), tc.req)
			require.Error(t, err, "every case fails eventually; only the reason differs")
			if tc.wantCode != codes.OK {
				assert.Equal(t, tc.wantCode, status.Code(err))
				assert.Contains(t, err.Error(), tc.wantMsg)
				return
			}
			// Passes the gate and fails further in, where real work begins.
			assert.NotContains(t, err.Error(), "manifest_hash_signature set without")
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

// start_transfer_v3 is the generic transfer path: it binds a manifest when given one, and never
// requires one. Each fee flow requires the manifest on its own endpoint instead.
func TestRejectStrayManifestSignature(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{61})
	sender := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	transferID := uuid.New().String()

	pkg := func(sig []byte) *pb.SenderTransferPackage {
		return &pb.SenderTransferPackage{
			OwnerIdentityPublicKey:     sender.Serialize(),
			TransferPackage:            &pb.TransferPackage{},
			ReceiverIdentityPublicKeys: map[string][]byte{"leaf-1": receiver.Serialize()},
			ManifestHashSignature:      sig,
		}
	}
	manifest := &pb.TransferManifest{
		Version:    1,
		TransferId: transferID,
		Network:    pb.Network_REGTEST,
		Edges: []*pb.ManifestEdge{{
			SenderIdentityPublicKey:   sender.Serialize(),
			ReceiverIdentityPublicKey: receiver.Serialize(),
			Amount:                    &pb.ManifestAmount{Amount: &pb.ManifestAmount_Sats{Sats: 1000}},
		}},
	}

	t.Run("a manifest alone is admitted", func(t *testing.T) {
		require.NoError(t, rejectStrayManifestSignature(&pb.StartTransferV3Request{
			TransferId: transferID, SenderPackages: []*pb.SenderTransferPackage{pkg(nil)}, TransferManifest: manifest,
		}))
	})

	t.Run("a manifest with its signature is admitted", func(t *testing.T) {
		require.NoError(t, rejectStrayManifestSignature(&pb.StartTransferV3Request{
			TransferId: transferID, SenderPackages: []*pb.SenderTransferPackage{pkg([]byte{0x30, 0x44})}, TransferManifest: manifest,
		}))
	})

	t.Run("a signature with no manifest is refused", func(t *testing.T) {
		err := rejectStrayManifestSignature(&pb.StartTransferV3Request{
			TransferId: transferID, SenderPackages: []*pb.SenderTransferPackage{pkg([]byte{0x30, 0x44})},
		})
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("no manifest material at all is admitted", func(t *testing.T) {
		require.NoError(t, rejectStrayManifestSignature(&pb.StartTransferV3Request{
			TransferId: transferID, SenderPackages: []*pb.SenderTransferPackage{pkg(nil)},
		}))
	})
}

// The single manifest gate on the Prepare path: it no-ops for a feeless caller, and still catches
// a signature with nothing to sign now that Prepare no longer checks that separately.
func TestBindManifestIfPresentWithoutAManifest(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{62})
	sender := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	t.Run("no manifest material at all is a no-op", func(t *testing.T) {
		req := &pb.StartTransferV3Request{TransferId: uuid.New().String()}

		require.NoError(t, bindManifestIfPresent(req, btcnetwork.Regtest, nil))
	})

	t.Run("a stray signature is still refused", func(t *testing.T) {
		req := &pb.StartTransferV3Request{
			TransferId: uuid.New().String(),
			SenderPackages: []*pb.SenderTransferPackage{{
				OwnerIdentityPublicKey: sender.Serialize(),
				ManifestHashSignature:  []byte{0x30, 0x44},
			}},
		}

		err := bindManifestIfPresent(req, btcnetwork.Regtest, nil)
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	})
}

// The binding's own rules are covered where it lives; what is pinned here is the wiring — that a
// manifest on the request reaches it at all, and that the owner and value it is judged against
// come from the locked rows rather than from anything the requester supplied.
func TestBindManifestIfPresentBindsAgainstTheLockedRows(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{63})
	senderKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	transferID := uuid.New().String()
	const leafID = "leaf-a"
	const leafSats = 1000

	signedRequest := func(t *testing.T) *pb.StartTransferV3Request {
		t.Helper()
		manifest := &pb.TransferManifest{
			Version:    1,
			TransferId: transferID,
			Network:    pb.Network_REGTEST,
			Edges: []*pb.ManifestEdge{{
				SenderIdentityPublicKey:   senderKey.Public().Serialize(),
				ReceiverIdentityPublicKey: receiver.Serialize(),
				Amount:                    &pb.ManifestAmount{Amount: &pb.ManifestAmount_Sats{Sats: leafSats}},
			}},
		}
		hash, err := common.HashTransferManifest(manifest)
		require.NoError(t, err)

		return &pb.StartTransferV3Request{
			TransferId: transferID,
			SenderPackages: []*pb.SenderTransferPackage{{
				OwnerIdentityPublicKey:     senderKey.Public().Serialize(),
				ReceiverIdentityPublicKeys: map[string][]byte{leafID: receiver.Serialize()},
				ManifestHashSignature:      ecdsa.Sign(senderKey.ToBTCEC(), hash).Serialize(),
			}},
			TransferManifest: manifest,
		}
	}
	lockedLeaves := func(owner keys.Public, valueSats uint64) map[string]*ent.TreeNode {
		return map[string]*ent.TreeNode{leafID: {OwnerIdentityPubkey: owner, Value: valueSats}}
	}

	t.Run("a manifest covering the locked rows binds", func(t *testing.T) {
		err := bindManifestIfPresent(signedRequest(t), btcnetwork.Regtest, lockedLeaves(senderKey.Public(), leafSats))

		require.NoError(t, err)
	})

	t.Run("a manifest disagreeing with the locked value is refused", func(t *testing.T) {
		err := bindManifestIfPresent(signedRequest(t), btcnetwork.Regtest, lockedLeaves(senderKey.Public(), leafSats-1))

		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("a manifest disagreeing with the locked owner is refused", func(t *testing.T) {
		otherOwner := keys.MustGeneratePrivateKeyFromRand(rng).Public()

		err := bindManifestIfPresent(signedRequest(t), btcnetwork.Regtest, lockedLeaves(otherOwner, leafSats))

		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	})
}
