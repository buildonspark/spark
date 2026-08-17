package handler

// Prepare is exercised at the flow boundary with fully honest submissions built the way a client group would build
// them — real resharing polynomials, real sealed blobs encrypted to this operator's identity key, a real group
// signature — then perturbed one element at a time. Round-1 commitment sets deliberately exclude this operator, so
// Prepare runs the complete check-and-persist stack and skips only the FROST round-2 call (which needs the signer
// sidecar and is covered by the integration suite).

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	rootspark "github.com/lightsparkdev/spark"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/secret_sharing/curve"
	"github.com/lightsparkdev/spark/common/secret_sharing/polynomial"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type mpcFlowFixture struct {
	ctx     context.Context
	cfg     *so.Config
	handler *MpcSendTransferFlowHandler
	client  *ent.Client
	sender  keys.Private
	req     *pb.StartTransferMpcRequest
	leafIDs []uuid.UUID
	// sealedPayloads holds, per participant position, the plaintext this
	// operator's blob was sealed from, so tests can perturb and re-seal.
	sealedPayloads map[uint32]*pb.MpcSealedSharePayload
}

func mpcFlowRandomScalar(t *testing.T) curve.Scalar {
	t.Helper()
	priv := keys.GeneratePrivateKey()
	s, err := curve.ParseScalar(priv.Serialize())
	require.NoError(t, err)
	return s
}

func mpcFlowSeal(t *testing.T, recipient keys.Public, payload *pb.MpcSealedSharePayload) []byte {
	t.Helper()
	plaintext, err := proto.Marshal(payload)
	require.NoError(t, err)
	pub, err := eciesgo.NewPublicKeyFromBytes(recipient.Serialize())
	require.NoError(t, err)
	blob, err := eciesgo.Encrypt(pub, plaintext)
	require.NoError(t, err)
	return blob
}

func (f *mpcFlowFixture) resealSelf(t *testing.T) {
	t.Helper()
	positions := f.req.GetMpcTransferPackage().GetPositions()
	shares := make([]*pb.MpcSealedShare, len(positions))
	for k, position := range positions {
		shares[k] = &pb.MpcSealedShare{Ecies: mpcFlowSeal(t, f.cfg.IdentityPrivateKey.Public(), f.sealedPayloads[position])}
	}
	f.req.GetMpcTransferPackage().GetKeyTweaks()[f.cfg.Identifier] = &pb.MpcOperatorShares{Shares: shares}
}

// newMpcFlowFixture builds an honest submission for numLeaves leaves owned by a fresh sender: for each leaf a
// tweak t and mask m with the leaf's owner key set to (t+m)·G, a Shamir split of t over two sub-user positions,
// per-sub-user resharing polynomials of degree threshold−1 with their Feldman commitments on the wire, sub-shares
// evaluated at every operator's identifier and sealed to this operator's identity key (peers get placeholder blobs
// this operator never opens), and a group signature over the recomputed payload.
func newMpcFlowFixture(t *testing.T, numLeaves int) *mpcFlowFixture {
	t.Helper()
	ctx, _ := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{knobs.KnobMpcTransferEnabled: 1}))
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	positions := []uint32{1, 2}
	threshold := int(cfg.Threshold)
	sender := keys.GeneratePrivateKey()
	receiver := keys.GeneratePrivateKey()

	operatorIDs := make([]string, 0, len(cfg.SigningOperatorMap))
	for id := range cfg.SigningOperatorMap {
		operatorIDs = append(operatorIDs, id)
	}

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

	transferID := uuid.NewString()
	sealedPayloads := make(map[uint32]*pb.MpcSealedSharePayload, len(positions))
	for _, position := range positions {
		sealedPayloads[position] = &pb.MpcSealedSharePayload{TransferId: transferID}
	}

	var leafIDs []uuid.UUID
	var pubLeaves []*pb.MpcSendLeaf
	var authLeaves []*pb.LeafAuthorization
	var cpfpJobs, directJobs, dfcJobs []*pb.UserSignedTxSigningJob
	var leafSighashes []transferpkg.MpcLeafRefundSighashes
	for i := range numLeaves {
		tweak := mpcFlowRandomScalar(t)
		mask := mpcFlowRandomScalar(t)
		ownerSigningPub, err := tweak.Add(mask).Point().ToPublicKey()
		require.NoError(t, err)
		maskPub, err := mask.Point().ToPublicKey()
		require.NoError(t, err)

		value := int64(1000 * (i + 1))
		nodeTx := mpcTaprootSpend(t, wire.OutPoint{Hash: [32]byte{0x01, byte(i)}, Index: 0}, ownerSigningPub, value, 0)
		directTx := mpcTaprootSpend(t, wire.OutPoint{Hash: [32]byte{0x02, byte(i)}, Index: 0}, ownerSigningPub, value, 0)

		// The transfer core reconstructs each refund from on-file state and requires byte equality plus the
		// stepped-down timelock, so the fixture builds refunds with the validator's own constructor, anchored on an
		// on-file current refund carrying the previous timelock.
		const currentTimelock = uint32(2000)
		onFileRefund := wire.NewMsgTx(3)
		onFileRefund.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0}, Sequence: currentTimelock})
		cpfpSeq := currentTimelock - rootspark.TimeLockInterval
		directSeq := cpfpSeq + rootspark.DirectTimelockOffset
		cpfpRefund, err := bitcointransaction.ConstructExpectedTransaction(
			mustSerializeTx(t, nodeTx), 0, currentTimelock, bitcointransaction.TxTypeRefundCPFP, receiver.Public(), cpfpSeq, 3)
		require.NoError(t, err)
		directRefund, err := bitcointransaction.ConstructExpectedTransaction(
			mustSerializeTx(t, directTx), 0, currentTimelock, bitcointransaction.TxTypeRefundDirect, receiver.Public(), directSeq, 3)
		require.NoError(t, err)
		dfcRefund, err := bitcointransaction.ConstructExpectedTransaction(
			mustSerializeTx(t, nodeTx), 0, currentTimelock, bitcointransaction.TxTypeRefundDirectFromCPFP, receiver.Public(), directSeq, 3)
		require.NoError(t, err)

		leaf, err := client.TreeNode.Create().
			SetStatus(st.TreeNodeStatusAvailable).
			SetTree(tree).
			SetNetwork(tree.Network).
			SetSigningKeyshare(keyshare).
			SetValue(uint64(value)).
			SetVerifyingPubkey(keyshareSecret.Public().Add(ownerSigningPub)).
			SetOwnerIdentityPubkey(sender.Public()).
			SetOwnerSigningPubkey(ownerSigningPub).
			SetRawTx(mustSerializeTx(t, nodeTx)).
			SetDirectTx(mustSerializeTx(t, directTx)).
			SetRawRefundTx(mustSerializeTx(t, onFileRefund)).
			SetVout(0).
			Save(ctx)
		require.NoError(t, err)
		leafIDs = append(leafIDs, leaf.ID)

		// Shamir-split the tweak over the sub-user positions, then reshare each
		// split with its own committed polynomial.
		shamir := &polynomial.ScalarPolynomial{Coefs: []curve.Scalar{tweak, mpcFlowRandomScalar(t)}}
		commitments := make([]*pb.SubUserCommitment, len(positions))
		for j, position := range positions {
			coefs := make([]curve.Scalar, threshold)
			coefs[0] = shamir.Eval(curve.ScalarFromInt(position))
			for k := 1; k < threshold; k++ {
				coefs[k] = mpcFlowRandomScalar(t)
			}
			subPoly := &polynomial.ScalarPolynomial{Coefs: coefs}

			proofs := make([][]byte, threshold)
			for k, coef := range coefs {
				pub, err := coef.Point().ToPublicKey()
				require.NoError(t, err)
				proofs[k] = pub.Serialize()
			}
			commitments[j] = &pb.SubUserCommitment{Proofs: proofs}

			selfX, err := curve.ParseScalar(mustHexDecode32(t, cfg.Identifier))
			require.NoError(t, err)
			sealedPayloads[position].LeafShares = append(sealedPayloads[position].LeafShares, &pb.MpcLeafSubShare{
				LeafId:      leaf.ID.String(),
				SecretShare: subPoly.Eval(selfX).Serialize(),
			})
		}

		pubLeaves = append(pubLeaves, &pb.MpcSendLeaf{
			LeafId:             leaf.ID.String(),
			SubuserCommitments: commitments,
			SecretCipher:       make([]byte, 129),
			Signature: &pbcommon.Signature{
				Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA,
				Signature: []byte{0x30, 0x01},
			},
		})
		authLeaves = append(authLeaves, &pb.LeafAuthorization{
			LeafId:                    leaf.ID.String(),
			AmountSats:                uint64(value),
			OwnerSigningPublicKey:     ownerSigningPub.Serialize(),
			MaskCommitment:            maskPub.Serialize(),
			ReceiverIdentityPublicKey: receiver.Public().Serialize(),
		})

		job := func(refundTx *wire.MsgTx) *pb.UserSignedTxSigningJob {
			contributions := make([]*pb.SubUserSigningContribution, len(positions))
			for k := range positions {
				contributions[k] = &pb.SubUserSigningContribution{
					NonceCommitment:  mpcTestSigningCommitment(t),
					PartialSignature: make([]byte, transferpkg.MpcPartialSignatureSize),
				}
			}
			return &pb.UserSignedTxSigningJob{
				LeafId:           leaf.ID.String(),
				SigningPublicKey: keys.GeneratePrivateKey().Public().Serialize(),
				RawTx:            mustSerializeTx(t, refundTx),
				// Round-1 commitments name a peer, never this operator, so Prepare's
				// signing-set filter leaves nothing for the sidecar to sign.
				SigningCommitments: &pb.SigningCommitments{
					SigningCommitments: map[string]*pbcommon.SigningCommitment{peerOperatorID(t, cfg): mpcTestSigningCommitment(t)},
				},
				SubuserContributions: contributions,
			}
		}
		cpfpJobs = append(cpfpJobs, job(cpfpRefund))
		directJobs = append(directJobs, job(directRefund))
		dfcJobs = append(dfcJobs, job(dfcRefund))

		leafSighashes = append(leafSighashes, transferpkg.MpcLeafRefundSighashes{
			LeafID:         leaf.ID,
			CPFP:           mpcSighash(t, cpfpRefund, nodeTx),
			Direct:         mpcSighash(t, directRefund, directTx),
			DirectFromCPFP: mpcSighash(t, dfcRefund, nodeTx),
		})
	}

	keyTweaks := make(map[string]*pb.MpcOperatorShares, len(operatorIDs))
	for _, operatorID := range operatorIDs {
		if operatorID == cfg.Identifier {
			continue
		}
		shares := make([]*pb.MpcSealedShare, len(positions))
		for k := range positions {
			shares[k] = &pb.MpcSealedShare{Ecies: []byte{0x04, byte(k)}}
		}
		keyTweaks[operatorID] = &pb.MpcOperatorShares{Shares: shares}
	}

	req := &pb.StartTransferMpcRequest{
		TransferId:             transferID,
		OwnerIdentityPublicKey: sender.Public().Serialize(),
		MpcTransferPackage: &pb.MpcTransferPackage{
			Positions:                  positions,
			Leaves:                     pubLeaves,
			KeyTweaks:                  keyTweaks,
			LeavesToSend:               cpfpJobs,
			DirectLeavesToSend:         directJobs,
			DirectFromCpfpLeavesToSend: dfcJobs,
			Authorization: &pb.TransferAuthorization{
				TransferId:            transferID,
				Leaves:                authLeaves,
				RefundSighashesDigest: transferpkg.MpcRefundSighashesDigest(leafSighashes),
				ExpiryTime:            timestamppb.New(time.Now().Add(time.Hour).Truncate(time.Second)),
			},
		},
	}

	f := &mpcFlowFixture{
		ctx:            ctx,
		cfg:            cfg,
		handler:        NewMpcSendTransferFlowHandler(cfg),
		client:         client,
		sender:         sender,
		req:            req,
		leafIDs:        leafIDs,
		sealedPayloads: sealedPayloads,
	}
	f.resealSelf(t)
	signMpcAuthorization(t, req, sender, pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA)
	return f
}

func peerOperatorID(t *testing.T, cfg *so.Config) string {
	t.Helper()
	for id := range cfg.SigningOperatorMap {
		if id != cfg.Identifier {
			return id
		}
	}
	t.Fatal("test config has no peer operators")
	return ""
}

func mustHexDecode32(t *testing.T, s string) []byte {
	t.Helper()
	out, err := hex.DecodeString(s)
	require.NoError(t, err)
	require.Len(t, out, 32)
	return out
}

func (f *mpcFlowFixture) prepare(t *testing.T) (proto.Message, error) {
	t.Helper()
	return f.handler.Prepare(f.ctx, &pbinternal.MpcSendTransferPrepareRequest{OriginalRequest: f.req})
}

func TestMpcSendTransferPrepare_HonestSubmission(t *testing.T) {
	f := newMpcFlowFixture(t, 2)

	resp, err := f.prepare(t)
	require.NoError(t, err)
	assert.Nil(t, resp, "this operator is outside every round-1 commitment set, so no shares are produced")

	transferEnt, err := f.client.Transfer.Get(f.ctx, uuid.MustParse(f.req.GetTransferId()))
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderKeyTweakPending, transferEnt.Status)

	require.NoError(t, f.handler.Rollback(f.ctx, &pbinternal.SendTransferRollbackRequest{TransferId: f.req.GetTransferId()}))
	transferEnt, err = f.client.Transfer.Get(f.ctx, transferEnt.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReturned, transferEnt.Status)
}

// Commit drives the synthesized tweak through the deployed rotation path (ValidateSendLeafKeyTweak →
// TweakKeyShare): the combined tweak t satisfies t·G = ownerSigningPubkey − maskCommitment, and rotation derives
// the new owner key as VerifyingPubkey − rotatedKeysharePubkey, so the owner signing key must land exactly on the
// group-signed mask commitment — the end state the binding check exists to guarantee.
func TestMpcSendTransferCommit_OwnerKeyLandsOnMaskCommitment(t *testing.T) {
	f := newMpcFlowFixture(t, 1)

	_, err := f.prepare(t)
	require.NoError(t, err)

	transferEnt, err := f.client.Transfer.Get(f.ctx, uuid.MustParse(f.req.GetTransferId()))
	require.NoError(t, err)
	_, err = f.handler.commitSenderKeyTweaks(f.ctx, transferEnt)
	require.NoError(t, err)

	node, err := f.client.TreeNode.Get(f.ctx, f.leafIDs[0])
	require.NoError(t, err)
	assert.Equal(t,
		f.req.GetMpcTransferPackage().GetAuthorization().GetLeaves()[0].GetMaskCommitment(),
		node.OwnerSigningPubkey.Serialize())
}

func TestMpcSendTransferPrepare_Rejections(t *testing.T) {
	for name, tc := range map[string]struct {
		perturb        func(t *testing.T, f *mpcFlowFixture)
		expectedCode   codes.Code
		expectedReason string
	}{
		"wrong mask": {
			perturb: func(t *testing.T, f *mpcFlowFixture) {
				f.req.GetMpcTransferPackage().GetAuthorization().GetLeaves()[0].MaskCommitment = keys.GeneratePrivateKey().Public().Serialize()
				signMpcAuthorization(t, f.req, f.sender, pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA)
			},
			expectedCode:   codes.InvalidArgument,
			expectedReason: sparkerrors.ReasonInvalidArgumentMpcTweakBindingMismatch,
		},
		"tampered sealed blob": {
			perturb: func(t *testing.T, f *mpcFlowFixture) {
				f.req.GetMpcTransferPackage().GetKeyTweaks()[f.cfg.Identifier].GetShares()[0].GetEcies()[40] ^= 1
			},
			expectedCode:   codes.InvalidArgument,
			expectedReason: sparkerrors.ReasonInvalidArgumentMpcSubShareUnsealable,
		},
		"sub-share off its committed polynomial": {
			perturb: func(t *testing.T, f *mpcFlowFixture) {
				f.sealedPayloads[1].GetLeafShares()[0].SecretShare = keys.GeneratePrivateKey().Serialize()
				f.resealSelf(t)
			},
			expectedCode:   codes.InvalidArgument,
			expectedReason: sparkerrors.ReasonInvalidArgumentMpcSubShareInvalid,
		},
		"zero config threshold is an internal error, not the client's fault": {
			perturb: func(t *testing.T, f *mpcFlowFixture) {
				f.cfg.Threshold = 0
			},
			expectedCode:   codes.Internal,
			expectedReason: sparkerrors.ReasonInternalDataInconsistency,
		},
		"sealed replay from another transfer": {
			perturb: func(t *testing.T, f *mpcFlowFixture) {
				f.sealedPayloads[1].TransferId = uuid.NewString()
				f.resealSelf(t)
			},
			expectedCode:   codes.InvalidArgument,
			expectedReason: sparkerrors.ReasonInvalidArgumentMpcSubShareUnsealable,
		},
		"missing operator sealed entry": {
			perturb: func(t *testing.T, f *mpcFlowFixture) {
				delete(f.req.GetMpcTransferPackage().GetKeyTweaks(), peerOperatorID(t, f.cfg))
			},
			expectedCode: codes.InvalidArgument,
		},
		"authorized amount mismatch": {
			perturb: func(t *testing.T, f *mpcFlowFixture) {
				f.req.GetMpcTransferPackage().GetAuthorization().GetLeaves()[0].AmountSats++
				signMpcAuthorization(t, f.req, f.sender, pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA)
			},
			expectedCode:   codes.FailedPrecondition,
			expectedReason: sparkerrors.ReasonFailedPreconditionMpcAuthorizationMismatch,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMpcFlowFixture(t, 1)
			tc.perturb(t, f)

			_, err := f.prepare(t)
			require.Error(t, err)
			assert.Equal(t, tc.expectedCode, status.Code(err))
			if tc.expectedReason != "" {
				assert.Equal(t, tc.expectedReason, grpcErrorReason(t, err))
			}
			_, err = f.client.Transfer.Get(f.ctx, uuid.MustParse(f.req.GetTransferId()))
			assert.True(t, ent.IsNotFound(err), "a rejected submission must leave no transfer row")
		})
	}
}

func TestMpcSendTransferPrepare_KnobOff(t *testing.T) {
	f := newMpcFlowFixture(t, 1)
	ctx := knobs.InjectKnobsService(f.ctx, knobs.NewFixedKnobs(nil))

	_, err := f.handler.Prepare(ctx, &pbinternal.MpcSendTransferPrepareRequest{OriginalRequest: f.req})
	require.Error(t, err)
	assert.Equal(t, codes.Unimplemented, status.Code(err))
}
