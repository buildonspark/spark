package handler

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/authz"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	transferpkg "github.com/lightsparkdev/spark/so/transfer"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// All StartTransferMpc gates under test run before any DB work, so these tests need only a knob-service context —
// mirroring TestStartTransferV3Consensus_MultiReceiverRejection.

const mpcTestOperatorID = "0000000000000000000000000000000000000000000000000000000000000001"

func mpcEnabledContext(t *testing.T) context.Context {
	t.Helper()
	return knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobMpcTransferEnabled: 1,
	}))
}

func mpcTestSigningCommitment(t *testing.T) *pbcommon.SigningCommitment {
	t.Helper()
	return &pbcommon.SigningCommitment{
		Hiding:  keys.GeneratePrivateKey().Public().Serialize(),
		Binding: keys.GeneratePrivateKey().Public().Serialize(),
	}
}

func mpcTestRawTx(t *testing.T) []byte {
	t.Helper()
	tx := wire.NewMsgTx(wire.TxVersion)
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: [32]byte{1}, Index: 0}, nil, nil))
	tx.AddTxOut(wire.NewTxOut(1000, []byte{0x51}))
	return mustSerializeTx(t, tx)
}

// validMpcTransferRequest builds a structurally valid submission with one leaf per receiver, two sub-users, and one
// operator entry.
func validMpcTransferRequest(t *testing.T, receivers ...keys.Public) *pb.StartTransferMpcRequest {
	t.Helper()
	transferID := uuid.NewString()
	positions := []uint32{1, 2}

	sealedShares := make([]*pb.MpcSealedShare, len(positions))
	for k, position := range positions {
		sealedShares[k] = &pb.MpcSealedShare{Ecies: []byte{0x04, byte(position)}}
	}

	var pubLeaves []*pb.MpcSendLeaf
	var authLeaves []*pb.LeafAuthorization
	var signingJobs []*pb.UserSignedTxSigningJob
	for _, receiver := range receivers {
		leafID := uuid.NewString()

		commitments := make([]*pb.SubUserCommitment, len(positions))
		contributions := make([]*pb.SubUserSigningContribution, len(positions))
		for k := range positions {
			commitments[k] = &pb.SubUserCommitment{
				Proofs: [][]byte{keys.GeneratePrivateKey().Public().Serialize()},
			}
			contributions[k] = &pb.SubUserSigningContribution{
				NonceCommitment:  mpcTestSigningCommitment(t),
				PartialSignature: bytes.Repeat([]byte{0x11}, transferpkg.MpcPartialSignatureSize),
			}
		}

		pubLeaves = append(pubLeaves, &pb.MpcSendLeaf{
			LeafId:             leafID,
			SubuserCommitments: commitments,
			SecretCipher:       bytes.Repeat([]byte{0x02}, 129),
			Signature: &pbcommon.Signature{
				Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA,
				Signature: []byte{0x30, 0x01},
			},
		})
		authLeaves = append(authLeaves, &pb.LeafAuthorization{
			LeafId:                    leafID,
			AmountSats:                1000,
			OwnerSigningPublicKey:     keys.GeneratePrivateKey().Public().Serialize(),
			MaskCommitment:            keys.GeneratePrivateKey().Public().Serialize(),
			ReceiverIdentityPublicKey: receiver.Serialize(),
		})
		signingJobs = append(signingJobs, &pb.UserSignedTxSigningJob{
			LeafId:           leafID,
			SigningPublicKey: keys.GeneratePrivateKey().Public().Serialize(),
			RawTx:            mpcTestRawTx(t),
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: map[string]*pbcommon.SigningCommitment{mpcTestOperatorID: mpcTestSigningCommitment(t)},
			},
			SubuserContributions: contributions,
		})
	}

	cloneJobs := func() []*pb.UserSignedTxSigningJob {
		out := make([]*pb.UserSignedTxSigningJob, len(signingJobs))
		for i, job := range signingJobs {
			contributions := make([]*pb.SubUserSigningContribution, len(job.GetSubuserContributions()))
			for k, contribution := range job.GetSubuserContributions() {
				contributions[k] = &pb.SubUserSigningContribution{
					NonceCommitment:  mpcTestSigningCommitment(t),
					PartialSignature: contribution.GetPartialSignature(),
				}
			}
			out[i] = &pb.UserSignedTxSigningJob{
				LeafId:               job.GetLeafId(),
				SigningPublicKey:     job.GetSigningPublicKey(),
				RawTx:                job.GetRawTx(),
				SigningCommitments:   job.GetSigningCommitments(),
				SubuserContributions: contributions,
			}
		}
		return out
	}

	return &pb.StartTransferMpcRequest{
		TransferId:             transferID,
		OwnerIdentityPublicKey: keys.GeneratePrivateKey().Public().Serialize(),
		MpcTransferPackage: &pb.MpcTransferPackage{
			Positions:                  positions,
			Leaves:                     pubLeaves,
			KeyTweaks:                  map[string]*pb.MpcOperatorShares{mpcTestOperatorID: {Shares: sealedShares}},
			LeavesToSend:               signingJobs,
			DirectLeavesToSend:         cloneJobs(),
			DirectFromCpfpLeavesToSend: cloneJobs(),
			Authorization: &pb.TransferAuthorization{
				TransferId:            transferID,
				Leaves:                authLeaves,
				RefundSighashesDigest: bytes.Repeat([]byte{0xAB}, transferpkg.RefundSighashesDigestSize),
				ExpiryTime:            timestamppb.New(time.Now().Add(time.Hour).Truncate(time.Second)),
				Signature: &pbcommon.Signature{
					Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA,
					Signature: bytes.Repeat([]byte{0x30}, 70),
				},
			},
		},
	}
}

// grpcErrorReason extracts the machine-readable ErrorInfo reason from a sparkerrors-produced error.
func grpcErrorReason(t *testing.T, err error) string {
	t.Helper()
	for _, d := range status.Convert(err).Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}

// The knob gate runs first within the handler: with the endpoint disabled the handler behaves as if the RPC were
// absent, returning Unimplemented before inspecting the request — even for requests that would otherwise be rejected
// as malformed. (In a live server the authn interceptor still rejects unauthenticated calls before the handler, as it
// does for every AuthSession method including the deprecated ones.)
func TestStartTransferMpc_KnobOff_Unimplemented(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)
	ctx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(nil))

	receiver := keys.GeneratePrivateKey().Public()
	for name, req := range map[string]*pb.StartTransferMpcRequest{
		"valid request":     validMpcTransferRequest(t, receiver),
		"malformed request": {TransferId: "not-a-uuid"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := handler.StartTransferMpc(ctx, req)
			require.Error(t, err)
			assert.Equal(t, codes.Unimplemented, status.Code(err))
			assert.Contains(t, err.Error(), "not enabled")
			assert.Equal(t, sparkerrors.ReasonUnavailableMethodDisabled, grpcErrorReason(t, err))
		})
	}
}

// With authz enforced, a session identity that doesn't match the request's owner key is rejected before any parsing.
// Kill-switch and identity mismatch share PermissionDenied and the same message on the wire by design (probing callers
// must not distinguish them); the internal authz.ErrorCode differentiates.
func TestStartTransferMpc_SessionIdentityMismatch(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	cfg.AuthzEnforced = true
	handler := NewTransferHandler(cfg)

	ctx := mpcEnabledContext(t)
	ctx = authn.InjectSessionForTests(ctx, keys.GeneratePrivateKey().Public(), 9999999999)

	req := validMpcTransferRequest(t, keys.GeneratePrivateKey().Public())

	_, err := handler.StartTransferMpc(ctx, req)
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	var authzErr *authz.Error
	require.ErrorAs(t, err, &authzErr)
	assert.Equal(t, authz.ErrorCodeIdentityMismatch, authzErr.Code)
}

// A kill-switched sender wallet is rejected before any parsing.
func TestStartTransferMpc_WalletKillSwitched(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	senderPrivKey := keys.GeneratePrivateKey()
	req := validMpcTransferRequest(t, keys.GeneratePrivateKey().Public())
	req.OwnerIdentityPublicKey = senderPrivKey.Public().Serialize()

	ctx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobMpcTransferEnabled:                                      1,
		knobs.KnobKillSwitchWallet + "@" + senderPrivKey.Public().ToHex(): 1,
	}))

	_, err := handler.StartTransferMpc(ctx, req)
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	var authzErr *authz.Error
	require.ErrorAs(t, err, &authzErr)
	assert.Equal(t, authz.ErrorCodeWalletKillSwitched, authzErr.Code)
}

// The per-transfer leaf limit every single-party front applies (KnobSoTransferLimit) gates this front too, before any
// parsing work.
func TestStartTransferMpc_LeafLimit(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)
	ctx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobMpcTransferEnabled: 1,
		knobs.KnobSoTransferLimit:    1,
	}))

	receiver := keys.GeneratePrivateKey().Public()
	req := validMpcTransferRequest(t, receiver, receiver)

	_, err := handler.StartTransferMpc(ctx, req)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "too many leaves to send")
}

func TestStartTransferMpc_Malformed_InvalidArgument(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)
	ctx := mpcEnabledContext(t)
	receiver := keys.GeneratePrivateKey().Public()

	tests := []struct {
		name   string
		mutate func(req *pb.StartTransferMpcRequest)
	}{
		{
			name:   "invalid transfer id",
			mutate: func(req *pb.StartTransferMpcRequest) { req.TransferId = "not-a-uuid" },
		},
		{
			name:   "missing package",
			mutate: func(req *pb.StartTransferMpcRequest) { req.MpcTransferPackage = nil },
		},
		{
			name: "missing authorization",
			mutate: func(req *pb.StartTransferMpcRequest) {
				req.GetMpcTransferPackage().Authorization = nil
			},
		},
		{
			name: "signing job in single-signer form",
			mutate: func(req *pb.StartTransferMpcRequest) {
				job := req.GetMpcTransferPackage().GetLeavesToSend()[0]
				job.SubuserContributions = nil
				job.SigningNonceCommitment = mpcTestSigningCommitment(t)
				job.UserSignature = []byte{0x01}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validMpcTransferRequest(t, receiver)
			tt.mutate(req)
			_, err := handler.StartTransferMpc(ctx, req)
			require.Error(t, err)
			assert.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

// A malformed owner identity key is rejected at the envelope (before session enforcement), with the same code and
// wrapping as the single-party entry points.
func TestStartTransferMpc_InvalidOwnerKey(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)
	ctx := mpcEnabledContext(t)

	req := validMpcTransferRequest(t, keys.GeneratePrivateKey().Public())
	req.OwnerIdentityPublicKey = []byte{0x01, 0x02}

	_, err := handler.StartTransferMpc(ctx, req)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "owner identity public key")
}

// The wire shape allows a receiver per leaf, but the MVP accepts exactly one distinct receiver — the relaxation is a
// later validation change, mirroring the single-party multi-receiver knob discipline.
func TestStartTransferMpc_MultiReceiverRejected(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)
	ctx := mpcEnabledContext(t)

	req := validMpcTransferRequest(t, keys.GeneratePrivateKey().Public(), keys.GeneratePrivateKey().Public())

	_, err := handler.StartTransferMpc(ctx, req)
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), "exactly one distinct receiver")
}

// A structurally valid submission whose authorization signature does not verify is rejected before any state is
// read — the fixture's placeholder signature parses but proves nothing. The authorized-submission path (which
// reaches the fail-closed tail) lives in transfer_handler_mpc_validation_test.go.
func TestStartTransferMpc_PlaceholderSignatureRejected(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)
	ctx := mpcEnabledContext(t)

	req := validMpcTransferRequest(t, keys.GeneratePrivateKey().Public())

	_, err := handler.StartTransferMpc(ctx, req)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Equal(t, sparkerrors.ReasonInvalidArgumentMpcAuthorizationSignatureInvalid, grpcErrorReason(t, err))
}
