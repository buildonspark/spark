package handler

// Verification behavior is pinned at the endpoint: submissions are built and signed exactly as a client would,
// operator state lives in real leaf rows, and assertions are on the gRPC status — so these tests survive the
// verification call relocating into the consensus prepare.

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/sighash"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	transferpkg "github.com/lightsparkdev/spark/so/transfer"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type mpcVerificationFixture struct {
	ctx      context.Context
	handler  *TransferHandler
	client   *ent.Client
	sender   keys.Private
	receiver keys.Private
	req      *pb.StartTransferMpcRequest
	leafIDs  []uuid.UUID
}

func mpcTaprootSpend(t *testing.T, prevout wire.OutPoint, payTo keys.Public, value int64, lockTime uint32) *wire.MsgTx {
	t.Helper()
	script, err := common.P2TRScriptFromPubKey(payTo)
	require.NoError(t, err)
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(wire.NewTxIn(&prevout, nil, nil))
	tx.AddTxOut(wire.NewTxOut(value, script))
	tx.LockTime = lockTime
	return tx
}

func mpcSighash(t *testing.T, refundTx, sourceTx *wire.MsgTx) []byte {
	t.Helper()
	hash, err := sighash.FromTx(refundTx, 0, sourceTx.TxOut[0])
	require.NoError(t, err)
	return hash[:]
}

// signMpcAuthorization recomputes the submission payload and replaces the authorization signature with signer's
// signature over it, under the given scheme.
func signMpcAuthorization(t *testing.T, req *pb.StartTransferMpcRequest, signer keys.Private, scheme pbcommon.SignatureScheme) {
	t.Helper()
	auth := req.GetMpcTransferPackage().GetAuthorization()
	// The payload does not cover the signature itself, but parsing requires a plausible one; sign in two passes.
	placeholder := []byte{0x30, 0x01}
	if scheme == pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR {
		placeholder = bytes.Repeat([]byte{0x01}, 64)
	}
	auth.Signature = &pbcommon.Signature{Scheme: scheme, Signature: placeholder}
	parsed, err := transferpkg.ParseMpcSubmission(req)
	require.NoError(t, err)
	payload := parsed.AuthorizationPayload()

	var sig []byte
	switch scheme {
	case pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA:
		sig = ecdsa.Sign(signer.ToBTCEC(), payload).Serialize()
	case pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR:
		schnorrSig, err := schnorr.Sign(signer.ToBTCEC(), payload)
		require.NoError(t, err)
		sig = schnorrSig.Serialize()
	default:
		t.Fatalf("unsupported scheme %s", scheme)
	}
	auth.Signature = &pbcommon.Signature{Scheme: scheme, Signature: sig}
}

// newMpcVerificationFixture creates leaves owned by a fresh sender and a submission whose authorized facts, refund
// transactions, sighash digest, and signature are all consistent with them — the honest baseline each test then
// perturbs.
func newMpcVerificationFixture(t *testing.T, numLeaves int) *mpcVerificationFixture {
	t.Helper()
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{knobs.KnobMpcTransferEnabled: 1}))
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	sender := keys.GeneratePrivateKey()
	receiver := keys.GeneratePrivateKey()
	positions := []uint32{1, 2}

	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(sender.Public()).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)
	keyshareSecret := keys.GeneratePrivateKey()
	keyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keyshareSecret).
		SetPublicShares(map[string]keys.Public{"key": keyshareSecret.Public()}).
		SetPublicKey(keyshareSecret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	var leafIDs []uuid.UUID
	var pubLeaves []*pb.MpcSendLeaf
	var authLeaves []*pb.LeafAuthorization
	var cpfpJobs, directJobs, directFromCPFPJobs []*pb.UserSignedTxSigningJob
	var leafSighashes []transferpkg.MpcLeafRefundSighashes
	for i := range numLeaves {
		ownerSigningPub := keys.GeneratePrivateKey().Public()
		value := int64(1000 * (i + 1))
		nodeTx := mpcTaprootSpend(t, wire.OutPoint{Hash: [32]byte{0x01, byte(i)}, Index: 0}, ownerSigningPub, value, 0)
		directTx := mpcTaprootSpend(t, wire.OutPoint{Hash: [32]byte{0x02, byte(i)}, Index: 0}, ownerSigningPub, value, 0)

		cpfpRefund := mpcTaprootSpend(t, wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0}, receiver.Public(), value, 0)
		directRefund := mpcTaprootSpend(t, wire.OutPoint{Hash: directTx.TxHash(), Index: 0}, receiver.Public(), value, 0)
		// Distinct lock time so the direct-from-cpfp refund is a distinct transaction spending the same output.
		directFromCPFPRefund := mpcTaprootSpend(t, wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0}, receiver.Public(), value, 1)

		leaf, err := client.TreeNode.Create().
			SetStatus(st.TreeNodeStatusAvailable).
			SetTree(tree).
			SetNetwork(tree.Network).
			SetSigningKeyshare(keyshare).
			SetValue(uint64(value)).
			SetVerifyingPubkey(keys.GeneratePrivateKey().Public()).
			SetOwnerIdentityPubkey(sender.Public()).
			SetOwnerSigningPubkey(ownerSigningPub).
			SetRawTx(mustSerializeTx(t, nodeTx)).
			SetDirectTx(mustSerializeTx(t, directTx)).
			SetRawRefundTx(mustSerializeTx(t, cpfpRefund)).
			SetVout(0).
			Save(ctx)
		require.NoError(t, err)
		leafIDs = append(leafIDs, leaf.ID)

		commitments := make([]*pb.SubUserCommitment, len(positions))
		for k := range positions {
			commitments[k] = &pb.SubUserCommitment{
				Proofs: [][]byte{keys.GeneratePrivateKey().Public().Serialize()},
			}
		}
		pubLeaves = append(pubLeaves, &pb.MpcSendLeaf{
			LeafId:             leaf.ID.String(),
			SubuserCommitments: commitments,
			SecretCipher:       bytes.Repeat([]byte{0x02}, 129),
			Signature: &pbcommon.Signature{
				Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA,
				Signature: []byte{0x30, 0x01},
			},
		})
		authLeaves = append(authLeaves, &pb.LeafAuthorization{
			LeafId:                    leaf.ID.String(),
			AmountSats:                uint64(value),
			OwnerSigningPublicKey:     ownerSigningPub.Serialize(),
			MaskCommitment:            keys.GeneratePrivateKey().Public().Serialize(),
			ReceiverIdentityPublicKey: receiver.Public().Serialize(),
		})

		job := func(refundTx *wire.MsgTx) *pb.UserSignedTxSigningJob {
			contributions := make([]*pb.SubUserSigningContribution, len(positions))
			for k := range positions {
				contributions[k] = &pb.SubUserSigningContribution{
					NonceCommitment:  mpcTestSigningCommitment(t),
					PartialSignature: bytes.Repeat([]byte{0x11}, transferpkg.MpcPartialSignatureSize),
				}
			}
			return &pb.UserSignedTxSigningJob{
				LeafId:           leaf.ID.String(),
				SigningPublicKey: keys.GeneratePrivateKey().Public().Serialize(),
				RawTx:            mustSerializeTx(t, refundTx),
				SigningCommitments: &pb.SigningCommitments{
					SigningCommitments: map[string]*pbcommon.SigningCommitment{mpcTestOperatorID: mpcTestSigningCommitment(t)},
				},
				SubuserContributions: contributions,
			}
		}
		cpfpJobs = append(cpfpJobs, job(cpfpRefund))
		directJobs = append(directJobs, job(directRefund))
		directFromCPFPJobs = append(directFromCPFPJobs, job(directFromCPFPRefund))

		leafSighashes = append(leafSighashes, transferpkg.MpcLeafRefundSighashes{
			LeafID:         leaf.ID,
			CPFP:           mpcSighash(t, cpfpRefund, nodeTx),
			Direct:         mpcSighash(t, directRefund, directTx),
			DirectFromCPFP: mpcSighash(t, directFromCPFPRefund, nodeTx),
		})
	}

	sealedShares := make([]*pb.MpcSealedShare, len(positions))
	for k, position := range positions {
		sealedShares[k] = &pb.MpcSealedShare{Ecies: []byte{0x04, byte(position)}}
	}
	transferID := uuid.NewString()
	req := &pb.StartTransferMpcRequest{
		TransferId:             transferID,
		OwnerIdentityPublicKey: sender.Public().Serialize(),
		MpcTransferPackage: &pb.MpcTransferPackage{
			Positions:                  positions,
			Leaves:                     pubLeaves,
			KeyTweaks:                  map[string]*pb.MpcOperatorShares{mpcTestOperatorID: {Shares: sealedShares}},
			LeavesToSend:               cpfpJobs,
			DirectLeavesToSend:         directJobs,
			DirectFromCpfpLeavesToSend: directFromCPFPJobs,
			Authorization: &pb.TransferAuthorization{
				TransferId:            transferID,
				Leaves:                authLeaves,
				RefundSighashesDigest: transferpkg.MpcRefundSighashesDigest(leafSighashes),
				ExpiryTime:            timestamppb.New(time.Now().Add(time.Hour).Truncate(time.Second)),
			},
		},
	}
	signMpcAuthorization(t, req, sender, pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA)

	return &mpcVerificationFixture{
		ctx:      ctx,
		handler:  NewTransferHandler(sparktesting.TestConfig(t)),
		client:   client,
		sender:   sender,
		receiver: receiver,
		req:      req,
		leafIDs:  leafIDs,
	}
}

func (f *mpcVerificationFixture) auth() *pb.TransferAuthorization {
	return f.req.GetMpcTransferPackage().GetAuthorization()
}

// A fully consistent, correctly signed submission passes verification and reaches the fail-closed tail — the only
// signal a client gets that every implemented check passed.
func TestStartTransferMpc_AuthorizedSubmissionFailsClosed(t *testing.T) {
	for name, scheme := range map[string]pbcommon.SignatureScheme{
		"ecdsa":   pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA,
		"schnorr": pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR,
	} {
		t.Run(name, func(t *testing.T) {
			f := newMpcVerificationFixture(t, 2)
			signMpcAuthorization(t, f.req, f.sender, scheme)

			_, err := f.handler.StartTransferMpc(f.ctx, f.req)
			require.Error(t, err)
			assert.Equal(t, codes.Unimplemented, status.Code(err))
			assert.Equal(t, sparkerrors.ReasonUnimplementedFeatureIncomplete, grpcErrorReason(t, err))
		})
	}
}

// The signature is verified over the recomputed whole-submission payload, so signing with the wrong key — or
// changing any signed byte after signing, including material outside the authorization message — invalidates it.
func TestStartTransferMpc_AuthorizationSignatureInvalid(t *testing.T) {
	for name, perturb := range map[string]func(t *testing.T, f *mpcVerificationFixture){
		"signed by another key": func(t *testing.T, f *mpcVerificationFixture) {
			signMpcAuthorization(t, f.req, keys.GeneratePrivateKey(), pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA)
		},
		"secret cipher swapped after signing": func(t *testing.T, f *mpcVerificationFixture) {
			f.req.GetMpcTransferPackage().GetLeaves()[0].SecretCipher[0] ^= 1
		},
		"commitment proof swapped after signing": func(t *testing.T, f *mpcVerificationFixture) {
			f.req.GetMpcTransferPackage().GetLeaves()[0].GetSubuserCommitments()[0].Proofs[0] =
				keys.GeneratePrivateKey().Public().Serialize()
		},
		"digest swapped after signing": func(t *testing.T, f *mpcVerificationFixture) {
			f.auth().GetRefundSighashesDigest()[0] ^= 1
		},
		"leaf signature swapped after signing": func(t *testing.T, f *mpcVerificationFixture) {
			// The receiver verifies this signature at claim time, so a coordinator rewriting it after the group
			// signed must fail here, before commit, rather than surfacing as an unclaimable transfer.
			f.req.GetMpcTransferPackage().GetLeaves()[0].GetSignature().Signature[0] ^= 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMpcVerificationFixture(t, 1)
			perturb(t, f)

			_, err := f.handler.StartTransferMpc(f.ctx, f.req)
			require.Error(t, err)
			assert.Equal(t, codes.InvalidArgument, status.Code(err))
			assert.Equal(t, sparkerrors.ReasonInvalidArgumentMpcAuthorizationSignatureInvalid, grpcErrorReason(t, err))
		})
	}
}

// Every authorized fact is checked against the operator's own state: a validly signed authorization naming a fact
// the operator disagrees with is rejected — including refund transactions, which are not signed bytes but are bound
// through the sighash digest and the on-file prevout and receiver-output checks.
func TestStartTransferMpc_AuthorizationFactMismatch(t *testing.T) {
	for name, tc := range map[string]struct {
		perturb              func(t *testing.T, f *mpcVerificationFixture)
		expectedErrSubstring string
	}{
		"amount differs from on-file value": {
			perturb: func(t *testing.T, f *mpcVerificationFixture) {
				f.auth().GetLeaves()[0].AmountSats++
				signMpcAuthorization(t, f.req, f.sender, pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA)
			},
			expectedErrSubstring: "does not match its on-file value",
		},
		"owner signing key differs from on-file key": {
			perturb: func(t *testing.T, f *mpcVerificationFixture) {
				f.auth().GetLeaves()[0].OwnerSigningPublicKey = keys.GeneratePrivateKey().Public().Serialize()
				signMpcAuthorization(t, f.req, f.sender, pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA)
			},
			expectedErrSubstring: "owner signing key",
		},
		"authorized receiver is not whom the refunds pay": {
			perturb: func(t *testing.T, f *mpcVerificationFixture) {
				other := keys.GeneratePrivateKey().Public().Serialize()
				for _, leaf := range f.auth().GetLeaves() {
					leaf.ReceiverIdentityPublicKey = other
				}
				signMpcAuthorization(t, f.req, f.sender, pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA)
			},
			expectedErrSubstring: "send to receiver identity pubkey",
		},
		"signed digest disagrees with the submitted transactions": {
			perturb: func(t *testing.T, f *mpcVerificationFixture) {
				f.auth().RefundSighashesDigest = bytes.Repeat([]byte{0xEE}, transferpkg.RefundSighashesDigestSize)
				signMpcAuthorization(t, f.req, f.sender, pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA)
			},
			expectedErrSubstring: "refund sighashes digest",
		},
		"expiry in the past": {
			perturb: func(t *testing.T, f *mpcVerificationFixture) {
				f.auth().ExpiryTime = timestamppb.New(time.Now().Add(-time.Minute).Truncate(time.Second))
				signMpcAuthorization(t, f.req, f.sender, pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA)
			},
			expectedErrSubstring: "not in the future",
		},
		"refund transaction replaced after signing": {
			perturb: func(t *testing.T, f *mpcVerificationFixture) {
				foreign := mpcTaprootSpend(t, wire.OutPoint{Hash: [32]byte{0x7F}, Index: 0}, f.receiver.Public(), 1000, 0)
				f.req.GetMpcTransferPackage().GetLeavesToSend()[0].RawTx = mustSerializeTx(t, foreign)
			},
			expectedErrSubstring: "does not spend the leaf's on-file output",
		},
		"refund transaction sighash differs from the signed digest": {
			perturb: func(t *testing.T, f *mpcVerificationFixture) {
				// Same prevout, same receiver output, different lock time: passes the structural checks and is
				// caught only by the digest — the binding that makes unsigned raw bytes unswappable.
				job := f.req.GetMpcTransferPackage().GetLeavesToSend()[0]
				refundTx, err := common.TxFromRawTxBytes(job.GetRawTx())
				require.NoError(t, err)
				refundTx.LockTime += 100
				job.RawTx = mustSerializeTx(t, refundTx)
			},
			expectedErrSubstring: "refund sighashes digest",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMpcVerificationFixture(t, 1)
			tc.perturb(t, f)

			_, err := f.handler.StartTransferMpc(f.ctx, f.req)
			require.Error(t, err)
			assert.Equal(t, codes.FailedPrecondition, status.Code(err))
			assert.Equal(t, sparkerrors.ReasonFailedPreconditionMpcAuthorizationMismatch, grpcErrorReason(t, err))
			assert.Contains(t, err.Error(), tc.expectedErrSubstring)
		})
	}
}

// State conflicts that are not authorization mismatches keep their deployed reasons.
func TestStartTransferMpc_LeafStateConflicts(t *testing.T) {
	t.Run("leaf not available", func(t *testing.T) {
		f := newMpcVerificationFixture(t, 1)
		_, err := f.client.TreeNode.UpdateOneID(f.leafIDs[0]).SetStatus(st.TreeNodeStatusTransferLocked).Save(f.ctx)
		require.NoError(t, err)

		_, err = f.handler.StartTransferMpc(f.ctx, f.req)
		require.Error(t, err)
		assert.Equal(t, codes.FailedPrecondition, status.Code(err))
		assert.Equal(t, sparkerrors.ReasonFailedPreconditionLeafUnavailable, grpcErrorReason(t, err))
	})

	t.Run("leaf owned by someone else", func(t *testing.T) {
		f := newMpcVerificationFixture(t, 1)
		_, err := f.client.TreeNode.UpdateOneID(f.leafIDs[0]).
			SetOwnerIdentityPubkey(keys.GeneratePrivateKey().Public()).Save(f.ctx)
		require.NoError(t, err)

		_, err = f.handler.StartTransferMpc(f.ctx, f.req)
		require.Error(t, err)
		assert.Equal(t, codes.FailedPrecondition, status.Code(err))
		assert.Equal(t, sparkerrors.ReasonFailedPreconditionMpcAuthorizationMismatch, grpcErrorReason(t, err))
		assert.Contains(t, err.Error(), "not owned by the sender")
	})

	t.Run("unknown leaf", func(t *testing.T) {
		f := newMpcVerificationFixture(t, 1)
		require.NoError(t, f.client.TreeNode.DeleteOneID(f.leafIDs[0]).Exec(f.ctx))

		_, err := f.handler.StartTransferMpc(f.ctx, f.req)
		require.Error(t, err)
		assert.Equal(t, codes.FailedPrecondition, status.Code(err))
		assert.Equal(t, sparkerrors.ReasonFailedPreconditionMpcAuthorizationMismatch, grpcErrorReason(t, err))
	})

	t.Run("leaf with no direct tx on file", func(t *testing.T) {
		f := newMpcVerificationFixture(t, 1)
		_, err := f.client.TreeNode.UpdateOneID(f.leafIDs[0]).SetDirectTx(nil).Save(f.ctx)
		require.NoError(t, err)

		_, err = f.handler.StartTransferMpc(f.ctx, f.req)
		require.Error(t, err)
		assert.Equal(t, codes.FailedPrecondition, status.Code(err))
		assert.Contains(t, err.Error(), "no on-file source transaction")
	})

	t.Run("transfer id already used", func(t *testing.T) {
		f := newMpcVerificationFixture(t, 1)
		_, err := f.client.Transfer.Create().
			SetID(uuid.MustParse(f.req.GetTransferId())).
			SetSenderIdentityPubkey(f.sender.Public()).
			SetReceiverIdentityPubkey(f.receiver.Public()).
			SetStatus(st.TransferStatusSenderInitiated).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(time.Hour)).
			SetType(st.TransferTypeTransfer).
			SetNetwork(btcnetwork.Regtest).
			Save(f.ctx)
		require.NoError(t, err)

		_, err = f.handler.StartTransferMpc(f.ctx, f.req)
		require.Error(t, err)
		assert.Equal(t, codes.AlreadyExists, status.Code(err))
		assert.Equal(t, sparkerrors.ReasonAlreadyExistsDuplicateOperation, grpcErrorReason(t, err))
	})
}
