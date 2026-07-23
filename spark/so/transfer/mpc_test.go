package transfer

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const testOperatorID2 = "0000000000000000000000000000000000000000000000000000000000000002"

var testMpcPositions = []uint32{1, 3}

// validMpcRequest builds a structurally valid two-leaf, two-sub-user, two-operator submission that
// ParseMpcSubmission accepts. Tests mutate one field at a time to pin each rejection.
func validMpcRequest(t *testing.T) *spark.StartTransferMpcRequest {
	t.Helper()
	transferID := uuid.NewString()
	leafIDs := []string{uuid.NewString(), uuid.NewString()}
	receiver := keys.GeneratePrivateKey().Public().Serialize()

	pubLeaves := make([]*spark.MpcSendLeaf, len(leafIDs))
	authLeaves := make([]*spark.LeafAuthorization, len(leafIDs))
	for i, leafID := range leafIDs {
		commitments := make([]*spark.SubUserCommitment, len(testMpcPositions))
		for k := range testMpcPositions {
			commitments[k] = &spark.SubUserCommitment{
				Proofs: [][]byte{
					keys.GeneratePrivateKey().Public().Serialize(),
					keys.GeneratePrivateKey().Public().Serialize(),
				},
			}
		}
		pubLeaves[i] = &spark.MpcSendLeaf{
			LeafId:             leafID,
			SubuserCommitments: commitments,
			SecretCipher:       bytes.Repeat([]byte{0x02}, 129),
			Signature: &pbcommon.Signature{
				Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA,
				Signature: []byte{0x30, 0x01},
			},
		}
		authLeaves[i] = &spark.LeafAuthorization{
			LeafId:                    leafID,
			AmountSats:                1000,
			OwnerSigningPublicKey:     keys.GeneratePrivateKey().Public().Serialize(),
			MaskCommitment:            keys.GeneratePrivateKey().Public().Serialize(),
			ReceiverIdentityPublicKey: receiver,
		}
	}

	keyTweaks := make(map[string]*spark.MpcOperatorShares)
	for _, operatorID := range []string{testOperatorID, testOperatorID2} {
		shares := make([]*spark.MpcSealedShare, len(testMpcPositions))
		for k, position := range testMpcPositions {
			shares[k] = &spark.MpcSealedShare{Ecies: []byte{0x04, byte(position)}}
		}
		keyTweaks[operatorID] = &spark.MpcOperatorShares{Shares: shares}
	}

	signingJobs := func() []*spark.UserSignedTxSigningJob {
		jobs := make([]*spark.UserSignedTxSigningJob, len(leafIDs))
		for i, leafID := range leafIDs {
			contributions := make([]*spark.SubUserSigningContribution, len(testMpcPositions))
			for k := range testMpcPositions {
				contributions[k] = &spark.SubUserSigningContribution{
					NonceCommitment:  testSigningCommitment(t),
					PartialSignature: bytes.Repeat([]byte{0x11}, MpcPartialSignatureSize),
				}
			}
			jobs[i] = &spark.UserSignedTxSigningJob{
				LeafId:           leafID,
				SigningPublicKey: keys.GeneratePrivateKey().Public().Serialize(),
				RawTx:            testRawTx(t, 1),
				SigningCommitments: &spark.SigningCommitments{
					SigningCommitments: map[string]*pbcommon.SigningCommitment{testOperatorID: testSigningCommitment(t)},
				},
				SubuserContributions: contributions,
			}
		}
		return jobs
	}

	return &spark.StartTransferMpcRequest{
		TransferId:             transferID,
		OwnerIdentityPublicKey: keys.GeneratePrivateKey().Public().Serialize(),
		MpcTransferPackage: &spark.MpcTransferPackage{
			Leaves:                     pubLeaves,
			KeyTweaks:                  keyTweaks,
			LeavesToSend:               signingJobs(),
			DirectLeavesToSend:         signingJobs(),
			DirectFromCpfpLeavesToSend: signingJobs(),
			Positions:                  testMpcPositions,
			Authorization: &spark.TransferAuthorization{
				TransferId:            transferID,
				Leaves:                authLeaves,
				RefundSighashesDigest: bytes.Repeat([]byte{0xAB}, RefundSighashesDigestSize),
				ExpiryTime:            timestamppb.New(time.Unix(1893456000, 0)),
				Signature: &pbcommon.Signature{
					Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA,
					Signature: bytes.Repeat([]byte{0x30}, 70),
				},
			},
		},
	}
}

func TestParseMpcSubmission_Valid(t *testing.T) {
	req := validMpcRequest(t)
	parsed, err := ParseMpcSubmission(req)
	require.NoError(t, err)

	assert.Equal(t, req.GetTransferId(), parsed.TransferID().String())
	assert.Equal(t, req.GetOwnerIdentityPublicKey(), parsed.SenderIdentityPublicKey().Serialize())
	assert.Equal(t, time.Unix(1893456000, 0).UTC(), parsed.ExpiryTime())
	assert.Equal(t, pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA, parsed.AuthSignatureScheme())
	assert.Len(t, parsed.AuthSignature(), 70)
	assert.Len(t, parsed.RefundSighashesDigest(), RefundSighashesDigestSize)
	assert.Equal(t, testMpcPositions, parsed.Positions())
	assert.NotNil(t, parsed.AuthorizationProto())

	leaves := parsed.Leaves()
	require.Len(t, leaves, 2)
	for _, leaf := range leaves {
		assert.Equal(t, testMpcPositions, leaf.Positions())
		assert.Equal(t, uint64(1000), leaf.AmountSats())
		assert.Len(t, leaf.SubUserCommitments(), len(testMpcPositions))
		assert.Equal(t, pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA, leaf.SignatureScheme())
		assert.NotEmpty(t, leaf.Signature())
	}

	// Both leaves name the same receiver, so the distinct set has one entry.
	assert.Len(t, parsed.Receivers(), 1)

	assert.Equal(t, []string{testOperatorID, testOperatorID2}, parsed.SealedOperatorIDs())
	sealed := parsed.SealedSharesFor(testOperatorID)
	require.Len(t, sealed, len(testMpcPositions))
	for _, position := range testMpcPositions {
		assert.NotEmpty(t, sealed[position])
	}
	assert.Nil(t, parsed.SealedSharesFor("not-an-operator"))

	assert.Len(t, parsed.LeavesToSend(), 2)
	assert.Len(t, parsed.DirectLeavesToSend(), 2)
	assert.Len(t, parsed.DirectFromCPFPLeavesToSend(), 2)
	for _, job := range parsed.LeavesToSend() {
		assert.Len(t, job.Contributions(), len(testMpcPositions))
		assert.NotNil(t, job.RefundTx())
	}
}

func TestMpcSubmission_SealedSharesForIsCopy(t *testing.T) {
	parsed, err := ParseMpcSubmission(validMpcRequest(t))
	require.NoError(t, err)

	stolen := parsed.SealedSharesFor(testOperatorID)
	require.NotEmpty(t, stolen)
	for position := range stolen {
		delete(stolen, position)
	}
	stolen[99] = []byte{0xFF}

	fresh := parsed.SealedSharesFor(testOperatorID)
	require.Len(t, fresh, len(testMpcPositions))
	assert.NotContains(t, fresh, uint32(99))
}

func TestParseMpcSubmission_DistinctReceivers(t *testing.T) {
	req := validMpcRequest(t)
	req.GetMpcTransferPackage().GetAuthorization().GetLeaves()[1].ReceiverIdentityPublicKey =
		keys.GeneratePrivateKey().Public().Serialize()

	parsed, err := ParseMpcSubmission(req)
	require.NoError(t, err)
	assert.Len(t, parsed.Receivers(), 2)
}

func TestParseMpcSubmission_EnvelopeErrors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(req *spark.StartTransferMpcRequest)
		wantErr error
	}{
		{
			name:    "invalid transfer id",
			mutate:  func(req *spark.StartTransferMpcRequest) { req.TransferId = "not-a-uuid" },
			wantErr: ErrMpcInvalidTransferID,
		},
		{
			name:    "invalid sender identity key",
			mutate:  func(req *spark.StartTransferMpcRequest) { req.OwnerIdentityPublicKey = []byte{0x01, 0x02} },
			wantErr: ErrMpcInvalidSenderIdentityKey,
		},
		{
			name:    "missing package",
			mutate:  func(req *spark.StartTransferMpcRequest) { req.MpcTransferPackage = nil },
			wantErr: ErrMpcMissingPackage,
		},
		{
			name:    "missing authorization",
			mutate:  func(req *spark.StartTransferMpcRequest) { req.GetMpcTransferPackage().Authorization = nil },
			wantErr: ErrMpcMissingAuthorization,
		},
		{
			name: "transfer id mismatch",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().TransferId = uuid.NewString()
			},
			wantErr: ErrMpcTransferIDMismatch,
		},
		{
			name: "missing expiry",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().ExpiryTime = nil
			},
			wantErr: ErrMpcMissingExpiry,
		},
		{
			name: "expiry with nanos",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().ExpiryTime = timestamppb.New(time.Unix(1893456000, 42))
			},
			wantErr: ErrMpcInvalidExpiry,
		},
		{
			name: "empty positions",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().Positions = nil
			},
			wantErr: ErrMpcInvalidPositions,
		},
		{
			name: "position zero",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().Positions = []uint32{0, 3}
			},
			wantErr: ErrMpcInvalidPositions,
		},
		{
			name: "positions not ascending",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().Positions = []uint32{3, 1}
			},
			wantErr: ErrMpcInvalidPositions,
		},
		{
			name: "duplicate positions",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().Positions = []uint32{1, 1}
			},
			wantErr: ErrMpcInvalidPositions,
		},
		{
			name: "too many positions",
			mutate: func(req *spark.StartTransferMpcRequest) {
				positions := make([]uint32, MaxMpcSubUsers+1)
				for i := range positions {
					positions[i] = uint32(i + 1)
				}
				req.GetMpcTransferPackage().Positions = positions
			},
			wantErr: ErrMpcInvalidPositions,
		},
		{
			name: "refund txs digest wrong size",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().RefundSighashesDigest = []byte{0x01, 0x02}
			},
			wantErr: ErrMpcInvalidRefundSighashesDigest,
		},
		{
			name: "missing auth signature",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().Signature = nil
			},
			wantErr: ErrMpcInvalidAuthSignature,
		},
		{
			name: "empty auth signature bytes",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().GetSignature().Signature = nil
			},
			wantErr: ErrMpcInvalidAuthSignature,
		},
		{
			name: "unspecified auth signature scheme",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().GetSignature().Scheme = pbcommon.SignatureScheme_SIGNATURE_SCHEME_UNSPECIFIED
			},
			wantErr: ErrMpcInvalidAuthSignature,
		},
		{
			name: "ecdsa auth signature too long",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().GetSignature().Signature = bytes.Repeat([]byte{0x30}, MaxSignatureSize+1)
			},
			wantErr: ErrMpcInvalidAuthSignature,
		},
		{
			name: "schnorr auth signature wrong length",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().GetSignature().Scheme = pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR
				req.GetMpcTransferPackage().GetAuthorization().GetSignature().Signature = bytes.Repeat([]byte{0x30}, 63)
			},
			wantErr: ErrMpcInvalidAuthSignature,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validMpcRequest(t)
			tt.mutate(req)
			_, err := ParseMpcSubmission(req)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestParseMpcSubmission_SchnorrAuthSignatureAccepted(t *testing.T) {
	req := validMpcRequest(t)
	req.GetMpcTransferPackage().GetAuthorization().Signature = &pbcommon.Signature{
		Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR,
		Signature: bytes.Repeat([]byte{0x40}, SchnorrSignatureSize),
	}
	parsed, err := ParseMpcSubmission(req)
	require.NoError(t, err)
	assert.Equal(t, pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR, parsed.AuthSignatureScheme())
}

func TestParseMpcSubmission_LeafErrors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(req *spark.StartTransferMpcRequest)
		wantErr error
	}{
		{
			name: "no leaves",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().Leaves = nil
				req.GetMpcTransferPackage().GetAuthorization().Leaves = nil
			},
			wantErr: ErrMpcNoLeaves,
		},
		{
			name: "duplicate package leaf",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeaves()[1].LeafId = req.GetMpcTransferPackage().GetLeaves()[0].GetLeafId()
			},
			wantErr: ErrDuplicateLeafID,
		},
		{
			name: "authorization leaf count mismatch",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().Leaves = req.GetMpcTransferPackage().GetAuthorization().GetLeaves()[:1]
			},
			wantErr: ErrMpcLeafSetMismatch,
		},
		{
			name: "authorization names a different leaf",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().GetLeaves()[1].LeafId = uuid.NewString()
			},
			wantErr: ErrMpcLeafSetMismatch,
		},
		{
			name: "duplicate authorization leaf",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().GetLeaves()[1].LeafId = req.GetMpcTransferPackage().GetAuthorization().GetLeaves()[0].GetLeafId()
			},
			wantErr: ErrDuplicateLeafID,
		},
		{
			name: "invalid authorization leaf id",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().GetLeaves()[0].LeafId = "not-a-uuid"
			},
			wantErr: ErrInvalidLeafID,
		},
		{
			name: "invalid owner signing pubkey",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().GetLeaves()[0].OwnerSigningPublicKey = []byte{0x01}
			},
			wantErr: ErrMpcInvalidLeafAuthorization,
		},
		{
			name: "invalid mask commitment",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().GetLeaves()[0].MaskCommitment = []byte{0x01}
			},
			wantErr: ErrMpcInvalidLeafAuthorization,
		},
		{
			name: "invalid receiver identity key",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetAuthorization().GetLeaves()[0].ReceiverIdentityPublicKey = []byte{0x01}
			},
			wantErr: ErrMpcInvalidLeafAuthorization,
		},
		{
			name: "missing secret cipher",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeaves()[0].SecretCipher = nil
			},
			wantErr: ErrMpcMissingSecretCipher,
		},
		{
			name: "missing per-leaf signature",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeaves()[0].Signature = nil
			},
			wantErr: ErrMpcInvalidLeafSignature,
		},
		{
			name: "per-leaf schnorr signature wrong length",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeaves()[0].Signature = &pbcommon.Signature{
					Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR,
					Signature: []byte{0x40, 0x41},
				}
			},
			wantErr: ErrMpcInvalidLeafSignature,
		},
		{
			name: "commitment vectors not aligned to positions",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeaves()[0].SubuserCommitments = nil
			},
			wantErr: ErrMpcPositionSetMismatch,
		},
		{
			name: "empty proofs vector",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeaves()[0].GetSubuserCommitments()[0].Proofs = nil
			},
			wantErr: ErrMpcInvalidProofs,
		},
		{
			name: "too many proofs in a vector",
			mutate: func(req *spark.StartTransferMpcRequest) {
				point := keys.GeneratePrivateKey().Public().Serialize()
				proofs := make([][]byte, MaxMpcProofsPerVector+1)
				for i := range proofs {
					proofs[i] = point
				}
				req.GetMpcTransferPackage().GetLeaves()[0].GetSubuserCommitments()[0].Proofs = proofs
			},
			wantErr: ErrMpcInvalidProofs,
		},
		{
			name: "proofs length mismatch across sub-users",
			mutate: func(req *spark.StartTransferMpcRequest) {
				commitment := req.GetMpcTransferPackage().GetLeaves()[0].GetSubuserCommitments()[1]
				commitment.Proofs = commitment.GetProofs()[:1]
			},
			wantErr: ErrMpcInvalidProofs,
		},
		{
			name: "off-curve proof point",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeaves()[0].GetSubuserCommitments()[0].Proofs[1] = make([]byte, 33)
			},
			wantErr: ErrMpcInvalidProofs,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validMpcRequest(t)
			tt.mutate(req)
			_, err := ParseMpcSubmission(req)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestParseMpcSubmission_SealedShareErrors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(req *spark.StartTransferMpcRequest)
		wantErr error
	}{
		{
			name: "no sealed shares",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().KeyTweaks = nil
			},
			wantErr: ErrMpcNoSealedShares,
		},
		{
			name: "empty sealed share",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetKeyTweaks()[testOperatorID].GetShares()[0].Ecies = nil
			},
			wantErr: ErrMpcMissingSealedShare,
		},
		{
			name: "fewer sealed blobs than positions",
			mutate: func(req *spark.StartTransferMpcRequest) {
				operatorShares := req.GetMpcTransferPackage().GetKeyTweaks()[testOperatorID]
				operatorShares.Shares = operatorShares.GetShares()[:1]
			},
			wantErr: ErrMpcPositionSetMismatch,
		},
		{
			name: "more sealed blobs than positions",
			mutate: func(req *spark.StartTransferMpcRequest) {
				operatorShares := req.GetMpcTransferPackage().GetKeyTweaks()[testOperatorID]
				operatorShares.Shares = append(operatorShares.GetShares(), &spark.MpcSealedShare{Ecies: []byte{0x01}})
			},
			wantErr: ErrMpcPositionSetMismatch,
		},
		{
			name: "sealed shares too large",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetKeyTweaks()[testOperatorID].GetShares()[0].Ecies =
					make([]byte, MaxMpcSealedShareBytes+1)
			},
			wantErr: ErrMpcSealedSharesTooLarge,
		},
		{
			// The cap is an aggregate across every blob of every operator, not a per-blob maximum: four blobs each
			// just over a quarter of the cap must be rejected together.
			name: "sealed shares too large in aggregate",
			mutate: func(req *spark.StartTransferMpcRequest) {
				for _, operatorShares := range req.GetMpcTransferPackage().GetKeyTweaks() {
					for _, share := range operatorShares.GetShares() {
						share.Ecies = make([]byte, MaxMpcSealedShareBytes/4+1)
					}
				}
			},
			wantErr: ErrMpcSealedSharesTooLarge,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validMpcRequest(t)
			tt.mutate(req)
			_, err := ParseMpcSubmission(req)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// TestParseMpcSubmission_SealedSharesAtCap pins the aggregate size cap as inclusive: a submission whose blobs sum to
// exactly MaxMpcSealedShareBytes parses.
func TestParseMpcSubmission_SealedSharesAtCap(t *testing.T) {
	req := validMpcRequest(t)
	for _, operatorShares := range req.GetMpcTransferPackage().GetKeyTweaks() {
		for _, share := range operatorShares.GetShares() {
			share.Ecies = make([]byte, MaxMpcSealedShareBytes/4)
		}
	}

	_, err := ParseMpcSubmission(req)
	require.NoError(t, err)
}

func TestParseMpcSubmission_SigningJobErrors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(req *spark.StartTransferMpcRequest)
		wantErr error
	}{
		{
			name: "cpfp job list count mismatch",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().LeavesToSend = req.GetMpcTransferPackage().GetLeavesToSend()[:1]
			},
			wantErr: ErrMpcLeafSetMismatch,
		},
		{
			name: "direct job list count mismatch",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().DirectLeavesToSend = nil
			},
			wantErr: ErrMpcLeafSetMismatch,
		},
		{
			name: "direct-from-cpfp job list count mismatch",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().DirectFromCpfpLeavesToSend = nil
			},
			wantErr: ErrMpcLeafSetMismatch,
		},
		{
			name: "job for a leaf not in the package",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeavesToSend()[0].LeafId = uuid.NewString()
			},
			wantErr: ErrMpcLeafSetMismatch,
		},
		{
			name: "duplicate job leaf",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeavesToSend()[1].LeafId = req.GetMpcTransferPackage().GetLeavesToSend()[0].GetLeafId()
			},
			wantErr: ErrDuplicateLeafID,
		},
		{
			name: "single-signer nonce commitment present",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeavesToSend()[0].SigningNonceCommitment = testSigningCommitment(t)
			},
			wantErr: ErrMpcSingleSignerFieldsPresent,
		},
		{
			name: "single-signer user signature present",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeavesToSend()[0].UserSignature = []byte{0x01}
			},
			wantErr: ErrMpcSingleSignerFieldsPresent,
		},
		{
			name: "additional inputs present",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeavesToSend()[0].AdditionalInputs = []*spark.InputSigningData{{}}
			},
			wantErr: ErrMpcAdditionalInputsNotAllowed,
		},
		{
			name: "invalid signing pubkey",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeavesToSend()[0].SigningPublicKey = []byte{0x01}
			},
			wantErr: ErrInvalidSigningPublicKey,
		},
		{
			name: "invalid raw tx",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeavesToSend()[0].RawTx = []byte{0x01, 0x02}
			},
			wantErr: ErrInvalidRefundTx,
		},
		{
			name: "refund tx with no inputs",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeavesToSend()[0].RawTx = testRawTx(t, 0)
			},
			wantErr: ErrInvalidRefundTx,
		},
		{
			name: "missing operator commitments",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeavesToSend()[0].SigningCommitments = nil
			},
			wantErr: ErrMissingOperatorCommitment,
		},
		{
			name: "contributions not aligned to positions",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeavesToSend()[0].SubuserContributions = nil
			},
			wantErr: ErrMpcPositionSetMismatch,
		},
		{
			name: "missing contribution nonce commitment",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeavesToSend()[0].GetSubuserContributions()[0].NonceCommitment = nil
			},
			wantErr: ErrInvalidNonceCommitment,
		},
		{
			name: "partial signature wrong length",
			mutate: func(req *spark.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().GetLeavesToSend()[0].GetSubuserContributions()[0].PartialSignature = []byte{0x01}
			},
			wantErr: ErrMpcInvalidPartialSignature,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validMpcRequest(t)
			tt.mutate(req)
			_, err := ParseMpcSubmission(req)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}
