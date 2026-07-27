package handler

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// createReceiverClaimKeyTweakBytes builds a marshaled ClaimLeafKeyTweak for
// this SO from a freshly split polynomial. Lives in this untagged file (rather
// than the lightspark-tagged key-tweak leaf-state tests that also use it) so
// the OSS build compiles.
func createReceiverClaimKeyTweakBytes(t *testing.T, cfg *so.Config, rng *rand.ChaCha8, leafID uuid.UUID) []byte {
	t.Helper()

	tweakPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	secretInt := new(big.Int).SetBytes(tweakPrivKey.Serialize())
	shares, err := secretsharing.SplitSecretWithProofs(
		secretInt,
		secp256k1.S256().N,
		int(cfg.Threshold),
		len(cfg.SigningOperatorMap),
	)
	require.NoError(t, err)
	require.NotEmpty(t, shares)

	secretShareBytes := make([]byte, 32)
	shares[0].Share.FillBytes(secretShareBytes)

	claimKeyTweak := &pb.ClaimLeafKeyTweak{
		LeafId: leafID.String(),
		SecretShareTweak: &pb.SecretShare{
			SecretShare: secretShareBytes,
			Proofs:      shares[0].Proofs,
		},
		PubkeySharesTweak: buildValidPubkeySharesTweak(t, cfg, shares[0].Proofs),
	}
	keyTweakBytes, err := proto.Marshal(claimKeyTweak)
	require.NoError(t, err)
	return keyTweakBytes
}

// claimTweakProofsDigest hand-computes the wire digest format (SHA-256 over
// BE32-length-prefixed ordered VSS proofs) so these tests pin the format
// rather than trusting the production helper.
func claimTweakProofsDigest(proofs [][]byte) []byte {
	h := sha256.New()
	var lenBuf [4]byte
	for _, p := range proofs {
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(p)))
		h.Write(lenBuf[:])
		h.Write(p)
	}
	return h.Sum(nil)
}

// buildFreshClaimRequestWithRealTweak builds a single-leaf transfer whose
// claim package carries a REAL encrypted key-tweak slice this SO can decrypt,
// with refund txs paying the post-tweak owner the fresh tweak predicts. The
// SE round-1 commitments exclude cfg.Identifier so this SO is a non-signer
// and Prepare completes without a FROST connection. Returns the request, the
// transfer_leaf row (for staging a stale stored tweak / asserting the stored
// bytes), and the fresh tweak's VSS proofs.
func buildFreshClaimRequestWithRealTweak(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	cfg *so.Config,
	transferStatus st.TransferStatus,
) (*pbinternal.ClaimTransferPrepareRequest, *ent.TransferLeaf, [][]byte) {
	t.Helper()
	rng := rand.NewChaCha8([32]byte{42})

	keyshare := createTestSigningKeyshare(t, ctx, rng, client)
	ownerIdentityPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPriv.Public(), client)
	leaf := createTestTreeNode(t, ctx, rng, client, tree, keyshare)

	// Pre-tweak owner on the leaf; the fresh tweak moves ownership to newOwner.
	origOwnerPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	newOwnerPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	leaf, err := leaf.Update().SetOwnerSigningPubkey(origOwnerPriv.Public()).Save(ctx)
	require.NoError(t, err)

	transfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(transferStatus).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetReceiverIdentityPubkey(ownerIdentityPriv.Public()).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	transferLeaf := createTestTransferLeaf(t, ctx, client, transfer, leaf)

	// Fresh polynomial for tweak = origOwner - newOwner, so the predicted
	// post-tweak owner (leaf.OwnerSigningPubkey - Proofs[0]) is newOwner.
	tweakPriv := origOwnerPriv.Sub(newOwnerPriv)
	shares, err := secretsharing.SplitSecretWithProofs(
		new(big.Int).SetBytes(tweakPriv.Serialize()),
		secp256k1.S256().N,
		int(cfg.Threshold),
		len(cfg.SigningOperatorMap),
	)
	require.NoError(t, err)
	var myShare *secretsharing.VerifiableSecretShare
	for _, s := range shares {
		if s.Index.Cmp(big.NewInt(int64(cfg.Index+1))) == 0 {
			myShare = s
			break
		}
	}
	require.NotNil(t, myShare)
	secretShareBytes := make([]byte, 32)
	myShare.Share.FillBytes(secretShareBytes)
	freshTweak := &pb.ClaimLeafKeyTweak{
		LeafId: leaf.ID.String(),
		SecretShareTweak: &pb.SecretShare{
			SecretShare: secretShareBytes,
			Proofs:      myShare.Proofs,
		},
		PubkeySharesTweak: buildValidPubkeySharesTweak(t, cfg, myShare.Proofs),
	}

	tweaksBytes, err := proto.Marshal(&pb.ClaimLeafKeyTweaks{LeavesToReceive: []*pb.ClaimLeafKeyTweak{freshTweak}})
	require.NoError(t, err)
	identityPub := eciesgo.NewPrivateKeyFromBytes(cfg.IdentityPrivateKey.Serialize()).PublicKey
	cipher, err := eciesgo.Encrypt(identityPub, tweaksBytes)
	require.NoError(t, err)
	keyTweakPackage := map[string][]byte{cfg.Identifier: cipher}

	signingPayload := common.GetClaimPackageSigningPayload(transfer.ID, keyTweakPackage)
	sig := ecdsa.Sign(ownerIdentityPriv.ToBTCEC(), signingPayload).Serialize()

	// SE round-1 commitments exclude this SO → non-signer.
	otherOps := make([]string, 0, 2)
	for id := range cfg.SigningOperatorMap {
		if id != cfg.Identifier {
			otherOps = append(otherOps, id)
		}
		if len(otherOps) == 2 {
			break
		}
	}
	require.Len(t, otherOps, 2, "test config must have at least 3 operators")
	signingCommitments := &pb.SigningCommitments{SigningCommitments: map[string]*pbcommon.SigningCommitment{
		otherOps[0]: createTestSigningCommitment(rng),
		otherOps[1]: createTestSigningCommitment(rng),
	}}
	refundJob := createTestLeafRefundTxSigningJob(t, rng, leaf, newOwnerPriv.Public())
	userJob := func(rawTx []byte) *pb.UserSignedTxSigningJob {
		return &pb.UserSignedTxSigningJob{
			LeafId:                 leaf.ID.String(),
			SigningPublicKey:       newOwnerPriv.Public().Serialize(),
			RawTx:                  rawTx,
			SigningNonceCommitment: createTestSigningCommitment(rng),
			SigningCommitments:     signingCommitments,
		}
	}

	pkg := &pb.ClaimPackage{
		HashVariant:                 pb.HashVariant_HASH_VARIANT_V2,
		UserSignature:               sig,
		KeyTweakPackage:             keyTweakPackage,
		LeavesToClaim:               []*pb.UserSignedTxSigningJob{userJob(refundJob.GetRefundTxSigningJob().GetRawTx())},
		DirectLeavesToClaim:         []*pb.UserSignedTxSigningJob{userJob(refundJob.GetDirectRefundTxSigningJob().GetRawTx())},
		DirectFromCpfpLeavesToClaim: []*pb.UserSignedTxSigningJob{userJob(refundJob.GetDirectFromCpfpRefundTxSigningJob().GetRawTx())},
	}
	return &pbinternal.ClaimTransferPrepareRequest{
		OriginalRequest: &pb.ClaimTransferRequest{
			TransferId:             transfer.ID.String(),
			OwnerIdentityPublicKey: ownerIdentityPriv.Public().Serialize(),
			ClaimPackage:           pkg,
		},
		ReportTweakDigests: true,
	}, transferLeaf, myShare.Proofs
}

// TestClaimTransferPrepare_AdoptsFreshPackageOverDivergentStoredTweak is the
// regression guard for the prod incident where a claim retry silently applied
// a STALE stored tweak on one SO while other SOs applied the fresh package's
// tweak, permanently diverging the leaf's signing keyshare. A stale tweak
// stranded at RKL by a prior attempt must be OVERWRITTEN by the freshly
// validated claim package — never silently reused — so every SO stages the
// same polynomial.
func TestClaimTransferPrepare_AdoptsFreshPackageOverDivergentStoredTweak(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	req, transferLeaf, freshProofs := buildFreshClaimRequestWithRealTweak(t, ctx, sessionCtx.Client, cfg, st.TransferStatusReceiverKeyTweakLocked)

	// Strand a stale tweak from a different polynomial on the row — the durable
	// state a prior partially-failed attempt leaves behind.
	staleBytes := createReceiverClaimKeyTweakBytes(t, cfg, rand.NewChaCha8([32]byte{43}), transferLeafLeafID(t, ctx, transferLeaf))
	transferLeaf, err := transferLeaf.Update().SetKeyTweak(staleBytes).Save(ctx)
	require.NoError(t, err)

	handler := NewClaimTransferFlowHandler(cfg)
	resp, err := handler.Prepare(ctx, req)
	require.NoError(t, err, "a fresh, fully-validated claim package must supersede a stale stored tweak")

	// The digest report must carry the FRESH polynomial's digest.
	report, ok := resp.(*pbinternal.ClaimTransferPrepareResponse)
	require.True(t, ok, "digest-reporting Prepare must return ClaimTransferPrepareResponse, got %T", resp)
	require.Len(t, report.GetLeafTweakDigests(), 1)
	assert.Equal(t, claimTweakProofsDigest(freshProofs), report.GetLeafTweakDigests()[0].GetProofsHash())
	assert.False(t, report.GetTweaksAlreadyApplied())

	// The durable staged tweak — what Commit will apply — must now be the
	// fresh polynomial, not the stale one.
	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())
	updated, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
	require.NoError(t, err)
	stored := &pb.ClaimLeafKeyTweak{}
	require.NoError(t, proto.Unmarshal(updated.KeyTweak, stored))
	assert.Equal(t, claimTweakProofsDigest(freshProofs), claimTweakProofsDigest(stored.GetSecretShareTweak().GetProofs()),
		"stored tweak must be overwritten with the fresh package's polynomial")
}

// stageUnresolvedClaimFlowRow writes an IN_FLIGHT participant FlowExecution
// row whose prepare payload binds the given transfer + owner — the durable
// trace of a prior claim flow whose commit/rollback hasn't landed yet.
func stageUnresolvedClaimFlowRow(t *testing.T, ctx context.Context, client *ent.Client, status st.FlowExecutionStatus, transferID string, ownerIDPK []byte) uuid.UUID {
	t.Helper()
	prepAny, err := anypb.New(&pbinternal.ClaimTransferPrepareRequest{
		OriginalRequest: &pb.ClaimTransferRequest{
			TransferId:             transferID,
			OwnerIdentityPublicKey: ownerIDPK,
			ClaimPackage:           minimalClaimPackage(),
		},
	})
	require.NoError(t, err)
	prepBytes, err := proto.Marshal(prepAny)
	require.NoError(t, err)
	row, err := client.FlowExecution.Create().
		SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CLAIM_TRANSFER)).
		SetCoordinatorIndex(1).
		SetStatus(status).
		SetPreparePayload(prepBytes).
		Save(ctx)
	require.NoError(t, err)
	return row.ID
}

// TestClaimTransferPrepare_FencesTweakReplacementWhileClaimFlowUnresolved pins
// the write-once anchor invariant: while a prior claim flow for this transfer
// is still IN_FLIGHT, its staged tweak may yet be committed (the delayed
// commit applies whatever is stored), so a retry that would REPLACE that
// polynomial must be rejected until the reconciler resolves the flow. Once
// the flow is terminal (rolled back), the same retry adopts the fresh
// package.
func TestClaimTransferPrepare_FencesTweakReplacementWhileClaimFlowUnresolved(t *testing.T) {
	t.Run("in-flight prior flow rejects replacement", func(t *testing.T) {
		ctx, sessionCtx := db.ConnectToTestPostgres(t)
		cfg := sparktesting.TestConfig(t)
		req, transferLeaf, _ := buildFreshClaimRequestWithRealTweak(t, ctx, sessionCtx.Client, cfg, st.TransferStatusReceiverKeyTweakLocked)

		staleBytes := createReceiverClaimKeyTweakBytes(t, cfg, rand.NewChaCha8([32]byte{44}), transferLeafLeafID(t, ctx, transferLeaf))
		transferLeaf, err := transferLeaf.Update().SetKeyTweak(staleBytes).Save(ctx)
		require.NoError(t, err)

		stageUnresolvedClaimFlowRow(t, ctx, sessionCtx.Client, st.FlowExecutionStatusInFlight,
			req.GetOriginalRequest().GetTransferId(), req.GetOriginalRequest().GetOwnerIdentityPublicKey())

		handler := NewClaimTransferFlowHandler(cfg)
		_, err = handler.Prepare(ctx, req)
		require.Error(t, err, "replacing a staged tweak that an unresolved flow may still commit must be rejected")
		require.Equal(t, codes.Aborted, status.Code(err))
		require.ErrorContains(t, err, "unresolved prior claim flow")

		updated, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
		require.NoError(t, err)
		assert.Equal(t, staleBytes, updated.KeyTweak, "the unresolved flow's anchor must remain intact")
	})

	t.Run("resolved prior flow allows adopt-fresh", func(t *testing.T) {
		ctx, sessionCtx := db.ConnectToTestPostgres(t)
		cfg := sparktesting.TestConfig(t)
		req, transferLeaf, freshProofs := buildFreshClaimRequestWithRealTweak(t, ctx, sessionCtx.Client, cfg, st.TransferStatusReceiverKeyTweakLocked)

		staleBytes := createReceiverClaimKeyTweakBytes(t, cfg, rand.NewChaCha8([32]byte{45}), transferLeafLeafID(t, ctx, transferLeaf))
		transferLeaf, err := transferLeaf.Update().SetKeyTweak(staleBytes).Save(ctx)
		require.NoError(t, err)

		stageUnresolvedClaimFlowRow(t, ctx, sessionCtx.Client, st.FlowExecutionStatusRolledBack,
			req.GetOriginalRequest().GetTransferId(), req.GetOriginalRequest().GetOwnerIdentityPublicKey())

		handler := NewClaimTransferFlowHandler(cfg)
		_, err = handler.Prepare(ctx, req)
		require.NoError(t, err, "a terminal prior flow no longer owns the anchor; the fresh package must supersede it")

		entTx, err := ent.GetTxFromContext(ctx)
		require.NoError(t, err)
		require.NoError(t, entTx.Commit())
		updated, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
		require.NoError(t, err)
		stored := &pb.ClaimLeafKeyTweak{}
		require.NoError(t, proto.Unmarshal(updated.KeyTweak, stored))
		assert.Equal(t, claimTweakProofsDigest(freshProofs), claimTweakProofsDigest(stored.GetSecretShareTweak().GetProofs()))
	})
}

// TestClaimTransferPrepare_MIMOAdoptsFreshPackageOverDivergentStoredTweak
// drives the same stale-tweak-overwrite contract through the MIMO
// receiver.Status branch (TransferReceiver row at KEY_TWEAK_LOCKED) instead of
// the legacy transfer.Status branch.
func TestClaimTransferPrepare_MIMOAdoptsFreshPackageOverDivergentStoredTweak(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	ctx = mimoEnabledContext(ctx)
	cfg := sparktesting.TestConfig(t)
	req, transferLeaf, freshProofs := buildFreshClaimRequestWithRealTweak(t, ctx, sessionCtx.Client, cfg, st.TransferStatusSenderKeyTweaked)

	transferID := uuid.MustParse(req.GetOriginalRequest().GetTransferId())
	receiverPK, err := keys.ParsePublicKey(req.GetOriginalRequest().GetOwnerIdentityPublicKey())
	require.NoError(t, err)
	receiver, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transferID).
		SetIdentityPubkey(receiverPK).
		SetStatus(st.TransferReceiverStatusKeyTweakLocked).
		SetTransferType(st.TransferTypeTransfer).
		Save(ctx)
	require.NoError(t, err)
	transferLeaf, err = transferLeaf.Update().SetTransferReceiverID(receiver.ID).Save(ctx)
	require.NoError(t, err)

	staleBytes := createReceiverClaimKeyTweakBytes(t, cfg, rand.NewChaCha8([32]byte{46}), transferLeafLeafID(t, ctx, transferLeaf))
	transferLeaf, err = transferLeaf.Update().SetKeyTweak(staleBytes).Save(ctx)
	require.NoError(t, err)

	handler := NewClaimTransferFlowHandler(cfg)
	resp, err := handler.Prepare(ctx, req)
	require.NoError(t, err, "MIMO receiver at KEY_TWEAK_LOCKED must adopt the fresh package over a stale stored tweak")
	report, ok := resp.(*pbinternal.ClaimTransferPrepareResponse)
	require.True(t, ok)
	require.Len(t, report.GetLeafTweakDigests(), 1)
	assert.Equal(t, claimTweakProofsDigest(freshProofs), report.GetLeafTweakDigests()[0].GetProofsHash())

	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())
	updated, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
	require.NoError(t, err)
	stored := &pb.ClaimLeafKeyTweak{}
	require.NoError(t, proto.Unmarshal(updated.KeyTweak, stored))
	assert.Equal(t, claimTweakProofsDigest(freshProofs), claimTweakProofsDigest(stored.GetSecretShareTweak().GetProofs()))
}

// TestClaimTransferPrepare_CorruptStoredTweakFailsLoudly pins that unreadable
// persisted key_tweak bytes surface as a typed internal-data error instead of
// being silently replaced — corruption of a durable proof anchor is an
// operator-attention event, not stale data.
func TestClaimTransferPrepare_CorruptStoredTweakFailsLoudly(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	req, transferLeaf, _ := buildFreshClaimRequestWithRealTweak(t, ctx, sessionCtx.Client, cfg, st.TransferStatusReceiverKeyTweakLocked)

	corrupt := []byte{0xFF, 0x00, 0xDE, 0xAD}
	transferLeaf, err := transferLeaf.Update().SetKeyTweak(corrupt).Save(ctx)
	require.NoError(t, err)

	handler := NewClaimTransferFlowHandler(cfg)
	_, err = handler.Prepare(ctx, req)
	require.Error(t, err)
	require.ErrorContains(t, err, "unreadable")

	updated, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
	require.NoError(t, err)
	assert.Equal(t, corrupt, updated.KeyTweak, "corrupt bytes must be preserved as evidence, not overwritten")
}

// TestClaimTransferPrepare_ReportsAppliedWhenResuming pins the digest report
// for the applied-resume branch: an SO that already applied the tweak stages
// nothing and must say so, so the coordinator can abort a claim that mixes
// applied and pre-apply SOs (not safely committable).
func TestClaimTransferPrepare_ReportsAppliedWhenResuming(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	req := buildAppliedClaimRequest(t, ctx, sessionCtx.Client, cfg, st.TransferStatusReceiverKeyTweakApplied)
	req.ReportTweakDigests = true

	handler := NewClaimTransferFlowHandler(cfg)
	resp, err := handler.Prepare(ctx, req)
	require.NoError(t, err)
	report, ok := resp.(*pbinternal.ClaimTransferPrepareResponse)
	require.True(t, ok, "digest-reporting Prepare must return ClaimTransferPrepareResponse, got %T", resp)
	assert.True(t, report.GetTweaksAlreadyApplied())
	assert.Empty(t, report.GetLeafTweakDigests())
}

// transferLeafLeafID resolves the tree-node id behind a transfer_leaf row.
func transferLeafLeafID(t *testing.T, ctx context.Context, transferLeaf *ent.TransferLeaf) uuid.UUID {
	t.Helper()
	leaf, err := transferLeaf.QueryLeaf().Only(ctx)
	require.NoError(t, err)
	return leaf.ID
}
