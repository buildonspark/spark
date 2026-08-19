package handler

import (
	"math/rand/v2"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Each rejection here is a signature that covers the right edges under the wrong binding, which is
// what stops a manifest being paired with an invoice it was not quoted for.
func TestVerifyAttestorSignature(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{81})
	attestorKey := keys.MustGeneratePrivateKeyFromRand(rng)
	impostorKey := keys.MustGeneratePrivateKeyFromRand(rng)
	payee := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	paymentHash := make([]byte, 32)
	paymentHash[0] = 0xab
	otherPaymentHash := make([]byte, 32)
	otherPaymentHash[0] = 0xcd

	// The attestor is not a receiver here: on a delegated receive it holds the preimage share and
	// the net is paid to a wallet that never signed anything.
	manifest := &pbspark.TransferManifest{
		Version:    common.SupportedTransferManifestVersion,
		TransferId: uuid.NewString(),
		Network:    pbspark.Network_REGTEST,
		Edges: []*pbspark.ManifestEdge{{
			SenderIdentityPublicKey:   keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
			ReceiverIdentityPublicKey: payee.Serialize(),
			Amount:                    &pbspark.ManifestAmount{Amount: &pbspark.ManifestAmount_Sats{Sats: 1000}},
		}},
	}
	manifestHash, err := common.HashTransferManifest(manifest)
	require.NoError(t, err)

	envelopeOn := func(t *testing.T, network pbspark.Network, role common.QuoteRole, boundTo []byte) []byte {
		t.Helper()
		target, err := common.ReceiveAttestorTarget(boundTo)
		require.NoError(t, err)
		digest, err := common.QuoteEnvelopeDigest(
			network, manifestHash, common.QuoteReasonReceive, role, target)
		require.NoError(t, err)
		return digest
	}
	envelope := func(t *testing.T, role common.QuoteRole, boundTo []byte) []byte {
		t.Helper()
		return envelopeOn(t, manifest.GetNetwork(), role, boundTo)
	}
	request := func(signature []byte) *pbspark.InitiatePreimageSwapV4Request {
		return &pbspark.InitiatePreimageSwapV4Request{
			Reason:            pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE,
			PaymentHash:       paymentHash,
			AttestorSignature: signature,
			TransferV3Request: &pbspark.StartTransferV3Request{TransferManifest: manifest},
		}
	}
	signWith := func(signer keys.Private, digest []byte) []byte {
		return ecdsa.Sign(signer.ToBTCEC(), digest).Serialize()
	}

	t.Run("the attestor envelope for this payment hash is accepted", func(t *testing.T) {
		require.NoError(t, verifyAttestorSignature(t.Context(),
			request(signWith(attestorKey, envelope(t, common.QuoteRoleAttestor, paymentHash))),
			attestorKey.Public()))
	})

	t.Run("a signature over the bare manifest hash is refused", func(t *testing.T) {
		err := verifyAttestorSignature(t.Context(),
			request(signWith(attestorKey, manifestHash)), attestorKey.Public())

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "signature is invalid")
	})

	// Two same-gross invoices from one attestor are otherwise interchangeable, which is how a
	// manifest gets paired with the wrong payee at no cost to whoever pairs them.
	t.Run("an envelope bound to another payment hash is refused", func(t *testing.T) {
		err := verifyAttestorSignature(t.Context(),
			request(signWith(attestorKey, envelope(t, common.QuoteRoleAttestor, otherPaymentHash))),
			attestorKey.Public())

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "signature is invalid")
	})

	// The role component is what keeps the two signers on one envelope from covering for each
	// other, and it has to hold even where their targets already differ.
	t.Run("the same inputs signed under the issuer role are refused", func(t *testing.T) {
		err := verifyAttestorSignature(t.Context(),
			request(signWith(attestorKey, envelope(t, common.QuoteRoleIssuer, paymentHash))),
			attestorKey.Public())

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "signature is invalid")
	})

	t.Run("a correct envelope signed by another key is refused", func(t *testing.T) {
		err := verifyAttestorSignature(t.Context(),
			request(signWith(impostorKey, envelope(t, common.QuoteRoleAttestor, paymentHash))),
			attestorKey.Public())

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "signature is invalid")
	})

	// Every other case takes the network from this manifest, so a gate that hardcoded one would
	// still pass them. This one signs a different network for the same manifest.
	t.Run("an envelope signed for another network is refused", func(t *testing.T) {
		err := verifyAttestorSignature(t.Context(),
			request(signWith(attestorKey, envelopeOn(t, pbspark.Network_MAINNET, common.QuoteRoleAttestor, paymentHash))),
			attestorKey.Public())

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "signature is invalid")
	})

	t.Run("a payment hash that is not 32 bytes cannot produce a target", func(t *testing.T) {
		req := request(signWith(attestorKey, envelope(t, common.QuoteRoleAttestor, paymentHash)))
		req.PaymentHash = make([]byte, 31)

		err := verifyAttestorSignature(t.Context(), req, attestorKey.Public())

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "attestation target")
	})
}
