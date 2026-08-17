package transfer

// The payload and digest constructions are signed-message byte layouts: their exact bytes are invisible at the
// endpoint boundary (which only surfaces signature valid/invalid), yet byte agreement is the whole contract — a
// client computes the same bytes to sign. Golden vectors and per-fact sensitivity are therefore pinned here, at the
// construction itself; endpoint-level rejection behavior is covered by the handler tests.

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deterministicTestKey(t *testing.T, b byte) keys.Public {
	t.Helper()
	scalar := append(bytes.Repeat([]byte{0}, 31), b)
	priv, err := keys.ParsePrivateKey(scalar)
	require.NoError(t, err)
	return priv.Public()
}

// deterministicMpcRequest pins every payload-relevant field of the fixture to fixed values so the payload digest is
// reproducible across runs; material outside the signed payload (sealed shares, signing jobs, sender key) stays as
// the base fixture built it.
func deterministicMpcRequest(t *testing.T) *spark.StartTransferMpcRequest {
	t.Helper()
	req := validMpcRequest(t)
	req.TransferId = "3f1c8747-7bd0-4e1c-9a2b-000000000001"
	pkg := req.GetMpcTransferPackage()
	auth := pkg.GetAuthorization()
	auth.TransferId = req.GetTransferId()

	leafIDs := []string{
		"1af52a0d-45c1-4f5b-8c4d-000000000001",
		"0be41b3c-34b2-4a6a-7b3c-000000000002",
	}
	for i, leafID := range leafIDs {
		pkg.GetLeaves()[i].LeafId = leafID
		pkg.GetLeaves()[i].SecretCipher = bytes.Repeat([]byte{0x51 + byte(i)}, 129)
		for k, commitment := range pkg.GetLeaves()[i].GetSubuserCommitments() {
			commitment.Proofs = [][]byte{
				deterministicTestKey(t, 0x60+byte(4*i+2*k)).Serialize(),
				deterministicTestKey(t, 0x61+byte(4*i+2*k)).Serialize(),
			}
		}
		authLeaf := auth.GetLeaves()[i]
		authLeaf.LeafId = leafID
		authLeaf.AmountSats = 1000 * uint64(i+1)
		authLeaf.OwnerSigningPublicKey = deterministicTestKey(t, 0x11+byte(i)).Serialize()
		authLeaf.MaskCommitment = deterministicTestKey(t, 0x21+byte(i)).Serialize()
		authLeaf.ReceiverIdentityPublicKey = deterministicTestKey(t, 0x31).Serialize()
		for _, jobs := range [][]*spark.UserSignedTxSigningJob{
			pkg.GetLeavesToSend(), pkg.GetDirectLeavesToSend(), pkg.GetDirectFromCpfpLeavesToSend(),
		} {
			jobs[i].LeafId = leafID
		}
	}
	return req
}

func parsePayload(t *testing.T, req *spark.StartTransferMpcRequest) []byte {
	t.Helper()
	parsed, err := ParseMpcSubmission(req)
	require.NoError(t, err)
	return parsed.AuthorizationPayload()
}

func TestAuthorizationPayload_GoldenVector(t *testing.T) {
	payload := parsePayload(t, deterministicMpcRequest(t))
	assert.Equal(t, "74ebd8323dc7406a58d913a516db26f37fd19a4d7a1a39015c07d9e03f7e1e5f", hex.EncodeToString(payload))
}

func TestAuthorizationPayload_IndependentOfWireOrder(t *testing.T) {
	req := deterministicMpcRequest(t)
	reference := parsePayload(t, req)

	reordered := deterministicMpcRequest(t)
	pkg := reordered.GetMpcTransferPackage()
	leaves := pkg.GetLeaves()
	leaves[0], leaves[1] = leaves[1], leaves[0]
	authLeaves := pkg.GetAuthorization().GetLeaves()
	authLeaves[0], authLeaves[1] = authLeaves[1], authLeaves[0]

	assert.Equal(t, reference, parsePayload(t, reordered))
}

func TestAuthorizationPayload_BindsEveryFact(t *testing.T) {
	reference := parsePayload(t, deterministicMpcRequest(t))

	for name, mutate := range map[string]func(t *testing.T, req *spark.StartTransferMpcRequest){
		"transfer id": func(t *testing.T, req *spark.StartTransferMpcRequest) {
			req.TransferId = "3f1c8747-7bd0-4e1c-9a2b-000000000002"
			req.GetMpcTransferPackage().GetAuthorization().TransferId = req.GetTransferId()
		},
		"expiry": func(t *testing.T, req *spark.StartTransferMpcRequest) {
			req.GetMpcTransferPackage().GetAuthorization().GetExpiryTime().Seconds++
		},
		"leaf id": func(t *testing.T, req *spark.StartTransferMpcRequest) {
			// The leaf id is the key every other per-leaf fact is bound under, so it has to move in all four
			// aligned lists at once; the parser joins them by leaf-id set and rejects any disagreement.
			const moved = "1af52a0d-45c1-4f5b-8c4d-00000000000f"
			pkg := req.GetMpcTransferPackage()
			pkg.GetLeaves()[0].LeafId = moved
			pkg.GetAuthorization().GetLeaves()[0].LeafId = moved
			for _, jobs := range [][]*spark.UserSignedTxSigningJob{
				pkg.GetLeavesToSend(), pkg.GetDirectLeavesToSend(), pkg.GetDirectFromCpfpLeavesToSend(),
			} {
				jobs[0].LeafId = moved
			}
		},
		"positions": func(t *testing.T, req *spark.StartTransferMpcRequest) {
			// Both positions lists stay length-2 so every aligned list still parses; only the values move.
			pkg := req.GetMpcTransferPackage()
			pkg.Positions = []uint32{pkg.GetPositions()[0], pkg.GetPositions()[1] + 1}
		},
		"amount": func(t *testing.T, req *spark.StartTransferMpcRequest) {
			req.GetMpcTransferPackage().GetAuthorization().GetLeaves()[0].AmountSats++
		},
		"owner signing key": func(t *testing.T, req *spark.StartTransferMpcRequest) {
			req.GetMpcTransferPackage().GetAuthorization().GetLeaves()[0].OwnerSigningPublicKey = deterministicTestKey(t, 0x7F).Serialize()
		},
		"mask commitment": func(t *testing.T, req *spark.StartTransferMpcRequest) {
			req.GetMpcTransferPackage().GetAuthorization().GetLeaves()[0].MaskCommitment = deterministicTestKey(t, 0x7F).Serialize()
		},
		"receiver": func(t *testing.T, req *spark.StartTransferMpcRequest) {
			for _, leaf := range req.GetMpcTransferPackage().GetAuthorization().GetLeaves() {
				leaf.ReceiverIdentityPublicKey = deterministicTestKey(t, 0x7F).Serialize()
			}
		},
		"secret cipher": func(t *testing.T, req *spark.StartTransferMpcRequest) {
			req.GetMpcTransferPackage().GetLeaves()[0].SecretCipher[0] ^= 1
		},
		"commitment proof": func(t *testing.T, req *spark.StartTransferMpcRequest) {
			req.GetMpcTransferPackage().GetLeaves()[0].GetSubuserCommitments()[0].Proofs[0] = deterministicTestKey(t, 0x7F).Serialize()
		},
		"leaf signature": func(t *testing.T, req *spark.StartTransferMpcRequest) {
			req.GetMpcTransferPackage().GetLeaves()[0].GetSignature().Signature = []byte{0x30, 0x02}
		},
		"leaf signature scheme": func(t *testing.T, req *spark.StartTransferMpcRequest) {
			req.GetMpcTransferPackage().GetLeaves()[0].Signature = &pbcommon.Signature{
				Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR,
				Signature: bytes.Repeat([]byte{0x40}, SchnorrSignatureSize),
			}
		},
		"refund sighashes digest": func(t *testing.T, req *spark.StartTransferMpcRequest) {
			req.GetMpcTransferPackage().GetAuthorization().GetRefundSighashesDigest()[0] ^= 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			req := deterministicMpcRequest(t)
			mutate(t, req)
			assert.NotEqual(t, reference, parsePayload(t, req))
		})
	}
}

func TestMpcRefundSighashesDigest_GoldenVector(t *testing.T) {
	digest := MpcRefundSighashesDigest([]MpcLeafRefundSighashes{
		{
			LeafID:         uuid.MustParse("1af52a0d-45c1-4f5b-8c4d-000000000001"),
			CPFP:           bytes.Repeat([]byte{0x01}, 32),
			Direct:         bytes.Repeat([]byte{0x02}, 32),
			DirectFromCPFP: bytes.Repeat([]byte{0x03}, 32),
		},
		{
			LeafID: uuid.MustParse("0be41b3c-34b2-4a6a-7b3c-000000000002"),
			CPFP:   bytes.Repeat([]byte{0x04}, 32),
		},
	})
	assert.Equal(t, "33c8916b097b9feb4779f3749adb3314e676136979fe333886b9d082e7c2d255", hex.EncodeToString(digest))
}

func TestMpcRefundSighashesDigest_IndependentOfInputOrder(t *testing.T) {
	a := MpcLeafRefundSighashes{
		LeafID: uuid.MustParse("1af52a0d-45c1-4f5b-8c4d-000000000001"),
		CPFP:   bytes.Repeat([]byte{0x01}, 32),
	}
	b := MpcLeafRefundSighashes{
		LeafID: uuid.MustParse("0be41b3c-34b2-4a6a-7b3c-000000000002"),
		CPFP:   bytes.Repeat([]byte{0x04}, 32),
	}
	assert.Equal(t,
		MpcRefundSighashesDigest([]MpcLeafRefundSighashes{a, b}),
		MpcRefundSighashesDigest([]MpcLeafRefundSighashes{b, a}))
}

func TestMpcRefundSighashesDigest_BindsFlavourSlot(t *testing.T) {
	leafID := uuid.MustParse("1af52a0d-45c1-4f5b-8c4d-000000000001")
	sighash := bytes.Repeat([]byte{0x01}, 32)
	asCPFP := MpcRefundSighashesDigest([]MpcLeafRefundSighashes{{LeafID: leafID, CPFP: sighash}})
	asDirect := MpcRefundSighashesDigest([]MpcLeafRefundSighashes{{LeafID: leafID, Direct: sighash}})
	asDirectFromCPFP := MpcRefundSighashesDigest([]MpcLeafRefundSighashes{{LeafID: leafID, DirectFromCPFP: sighash}})
	assert.NotEqual(t, asCPFP, asDirect)
	assert.NotEqual(t, asCPFP, asDirectFromCPFP)
	assert.NotEqual(t, asDirect, asDirectFromCPFP)
}
