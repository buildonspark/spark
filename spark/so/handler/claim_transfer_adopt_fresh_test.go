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
) (*pbinternal.ClaimTransferPrepareRequest, *ent.TransferLeaf, *pb.ClaimLeafKeyTweak, keys.Private) {
	t.Helper()
	rng := rand.NewChaCha8([32]byte{42})

	keyshare := createTestSigningKeyshare(t, ctx, rng, client)
	publicShares := make(map[string]keys.Public, len(cfg.SigningOperatorMap))
	for identifier := range cfg.SigningOperatorMap {
		publicShares[identifier] = keys.MustGeneratePrivateKeyFromRand(rng).Public()
	}
	keyshare, err := keyshare.Update().SetPublicShares(publicShares).Save(ctx)
	require.NoError(t, err)
	ownerIdentityPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPriv.Public(), client)
	leaf := createTestTreeNode(t, ctx, rng, client, tree, keyshare)

	// Pre-tweak owner on the leaf; the fresh tweak moves ownership to newOwner.
	origOwnerPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	newOwnerPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	leaf, err = leaf.Update().SetOwnerSigningPubkey(origOwnerPriv.Public()).Save(ctx)
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
	createTestTransferReceiver(t, ctx, client, transfer)

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
	}, transferLeaf, freshTweak, ownerIdentityPriv
}

func replaceClaimPackageKeyTweak(
	t *testing.T,
	cfg *so.Config,
	req *pbinternal.ClaimTransferPrepareRequest,
	ownerIdentityPriv keys.Private,
	keyTweakBytes []byte,
) {
	t.Helper()
	leafTweak := &pb.ClaimLeafKeyTweak{}
	require.NoError(t, proto.Unmarshal(keyTweakBytes, leafTweak))
	replaceClaimPackageKeyTweaks(t, cfg, req, ownerIdentityPriv, []*pb.ClaimLeafKeyTweak{leafTweak})
}

func replaceClaimPackageKeyTweaks(
	t *testing.T,
	cfg *so.Config,
	req *pbinternal.ClaimTransferPrepareRequest,
	ownerIdentityPriv keys.Private,
	leafTweaks []*pb.ClaimLeafKeyTweak,
) {
	t.Helper()
	packageBytes, err := proto.Marshal(&pb.ClaimLeafKeyTweaks{LeavesToReceive: leafTweaks})
	require.NoError(t, err)
	identityPub := eciesgo.NewPrivateKeyFromBytes(cfg.IdentityPrivateKey.Serialize()).PublicKey
	cipher, err := eciesgo.Encrypt(identityPub, packageBytes)
	require.NoError(t, err)
	keyTweakPackage := map[string][]byte{cfg.Identifier: cipher}
	req.OriginalRequest.ClaimPackage.KeyTweakPackage = keyTweakPackage
	signingPayload := common.GetClaimPackageSigningPayload(
		uuid.MustParse(req.GetOriginalRequest().GetTransferId()),
		keyTweakPackage,
	)
	req.OriginalRequest.ClaimPackage.UserSignature = ecdsa.Sign(ownerIdentityPriv.ToBTCEC(), signingPayload).Serialize()
}

func appendFreshClaimLeafToRequest(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	cfg *so.Config,
	req *pbinternal.ClaimTransferPrepareRequest,
	ownerIdentityPriv keys.Private,
) (*ent.TransferLeaf, *pb.ClaimLeafKeyTweak) {
	t.Helper()
	rng := rand.NewChaCha8([32]byte{44})

	keyshare := createTestSigningKeyshare(t, ctx, rng, client)
	publicShares := make(map[string]keys.Public, len(cfg.SigningOperatorMap))
	for identifier := range cfg.SigningOperatorMap {
		publicShares[identifier] = keys.MustGeneratePrivateKeyFromRand(rng).Public()
	}
	keyshare, err := keyshare.Update().SetPublicShares(publicShares).Save(ctx)
	require.NoError(t, err)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPriv.Public(), client)
	leaf := createTestTreeNode(t, ctx, rng, client, tree, keyshare)

	origOwnerPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	newOwnerPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	leaf, err = leaf.Update().SetOwnerSigningPubkey(origOwnerPriv.Public()).Save(ctx)
	require.NoError(t, err)

	transferID := uuid.MustParse(req.GetOriginalRequest().GetTransferId())
	transfer, err := client.Transfer.Get(ctx, transferID)
	require.NoError(t, err)
	transferLeaf := createTestTransferLeaf(t, ctx, client, transfer, leaf)
	receivers, err := transfer.QueryTransferReceivers().All(ctx)
	require.NoError(t, err)
	require.Len(t, receivers, 1)
	transferLeaf, err = transferLeaf.Update().SetTransferReceiverID(receivers[0].ID).Save(ctx)
	require.NoError(t, err)

	tweakPriv := origOwnerPriv.Sub(newOwnerPriv)
	shares, err := secretsharing.SplitSecretWithProofs(
		new(big.Int).SetBytes(tweakPriv.Serialize()),
		secp256k1.S256().N,
		int(cfg.Threshold),
		len(cfg.SigningOperatorMap),
	)
	require.NoError(t, err)
	var myShare *secretsharing.VerifiableSecretShare
	for _, share := range shares {
		if share.Index.Cmp(big.NewInt(int64(cfg.Index+1))) == 0 {
			myShare = share
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

	decryptionPrivateKey := eciesgo.NewPrivateKeyFromBytes(cfg.IdentityPrivateKey.Serialize())
	decrypted, err := eciesgo.Decrypt(
		decryptionPrivateKey,
		req.GetOriginalRequest().GetClaimPackage().GetKeyTweakPackage()[cfg.Identifier],
	)
	require.NoError(t, err)
	claimKeyTweaks := &pb.ClaimLeafKeyTweaks{}
	require.NoError(t, proto.Unmarshal(decrypted, claimKeyTweaks))
	claimKeyTweaks.LeavesToReceive = append(claimKeyTweaks.LeavesToReceive, freshTweak)
	replaceClaimPackageKeyTweaks(t, cfg, req, ownerIdentityPriv, claimKeyTweaks.GetLeavesToReceive())

	otherOps := make([]string, 0, 2)
	for identifier := range cfg.SigningOperatorMap {
		if identifier != cfg.Identifier {
			otherOps = append(otherOps, identifier)
		}
		if len(otherOps) == 2 {
			break
		}
	}
	require.Len(t, otherOps, 2)
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
	pkg := req.GetOriginalRequest().GetClaimPackage()
	pkg.LeavesToClaim = append(pkg.LeavesToClaim, userJob(refundJob.GetRefundTxSigningJob().GetRawTx()))
	pkg.DirectLeavesToClaim = append(pkg.DirectLeavesToClaim, userJob(refundJob.GetDirectRefundTxSigningJob().GetRawTx()))
	pkg.DirectFromCpfpLeavesToClaim = append(pkg.DirectFromCpfpLeavesToClaim, userJob(refundJob.GetDirectFromCpfpRefundTxSigningJob().GetRawTx()))

	return transferLeaf, freshTweak
}

func TestClaimTransferPrepare_ReusesStoredTweak(t *testing.T) {
	statuses := []st.TransferStatus{
		st.TransferStatusSenderKeyTweaked,
		st.TransferStatusReceiverKeyTweaked,
		st.TransferStatusReceiverKeyTweakLocked,
	}
	for _, transferStatus := range statuses {
		t.Run(string(transferStatus), func(t *testing.T) {
			ctx, sessionCtx := db.ConnectToTestPostgres(t)
			cfg := sparktesting.TestConfig(t)
			req, transferLeaf, storedTweak, ownerIdentityPriv := buildFreshClaimRequestWithRealTweak(t, ctx, sessionCtx.Client, cfg, transferStatus)

			storedBytes, err := proto.Marshal(storedTweak)
			require.NoError(t, err)
			transferLeaf, err = transferLeaf.Update().SetKeyTweak(storedBytes).Save(ctx)
			require.NoError(t, err)

			incomingBytes := createReceiverClaimKeyTweakBytes(t, cfg, rand.NewChaCha8([32]byte{43}), transferLeafLeafID(t, ctx, transferLeaf))
			replaceClaimPackageKeyTweak(t, cfg, req, ownerIdentityPriv, incomingBytes)

			handler := NewClaimTransferFlowHandler(cfg)
			resp, err := handler.Prepare(ctx, req)
			require.NoError(t, err, "a saved polynomial must remain authoritative during recovery")

			report, ok := resp.(*pbinternal.ClaimTransferPrepareResponse)
			require.True(t, ok, "digest-reporting Prepare must return ClaimTransferPrepareResponse, got %T", resp)
			require.Len(t, report.GetLeafTweakDigests(), 1)
			assert.Equal(t, claimTweakProofsDigest(storedTweak.GetSecretShareTweak().GetProofs()), report.GetLeafTweakDigests()[0].GetProofsHash())
			assert.Len(t, report.GetLeafTweakDigests()[0].GetPostTweakKeyshareHash(), sha256.Size)
			assert.False(t, report.GetTweaksAlreadyApplied())

			entTx, err := ent.GetTxFromContext(ctx)
			require.NoError(t, err)
			require.NoError(t, entTx.Commit())
			updated, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
			require.NoError(t, err)
			assert.Equal(t, storedBytes, updated.KeyTweak, "Prepare must not replace the durable recovery polynomial")
		})
	}
}

func TestInitiateSettleReceiverKeyTweak_DoesNotOverwriteStoredTweak(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	req, transferLeaf, storedTweak, ownerIdentityPriv := buildFreshClaimRequestWithRealTweak(
		t, ctx, sessionCtx.Client, cfg, st.TransferStatusReceiverKeyTweakLocked,
	)
	storedBytes, err := proto.Marshal(storedTweak)
	require.NoError(t, err)
	transferLeaf, err = transferLeaf.Update().SetKeyTweak(storedBytes).Save(ctx)
	require.NoError(t, err)

	incomingBytes := createReceiverClaimKeyTweakBytes(t, cfg, rand.NewChaCha8([32]byte{47}), transferLeafLeafID(t, ctx, transferLeaf))
	incomingTweak := &pb.ClaimLeafKeyTweak{}
	require.NoError(t, proto.Unmarshal(incomingBytes, incomingTweak))
	replaceClaimPackageKeyTweak(t, cfg, req, ownerIdentityPriv, incomingBytes)

	leafID := transferLeafLeafID(t, ctx, transferLeaf).String()
	handler := NewClaimTransferFlowHandler(cfg)
	err = handler.InitiateSettleReceiverKeyTweak(ctx, &pbinternal.InitiateSettleReceiverKeyTweakRequest{
		TransferId: req.GetOriginalRequest().GetTransferId(),
		KeyTweakProofs: map[string]*pb.SecretProof{
			leafID: {Proofs: incomingTweak.GetSecretShareTweak().GetProofs()},
		},
		UserPublicKeys: map[string][]byte{
			leafID: req.GetOriginalRequest().GetClaimPackage().GetLeavesToClaim()[0].GetSigningPublicKey(),
		},
		EncryptedClaimKeyTweakPackage: req.GetOriginalRequest().GetClaimPackage().GetKeyTweakPackage(),
		ClaimSignature:                req.GetOriginalRequest().GetClaimPackage().GetUserSignature(),
	})
	require.ErrorContains(t, err, "proof provided is not the same")

	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	updated, err := entTx.TransferLeaf.Get(ctx, transferLeaf.ID)
	require.NoError(t, err)
	assert.Equal(t, storedBytes, updated.KeyTweak)
}

func TestClaimTransferPrepare_MIMOReusesStoredTweak(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	req, transferLeaf, storedTweak, ownerIdentityPriv := buildFreshClaimRequestWithRealTweak(t, ctx, sessionCtx.Client, cfg, st.TransferStatusSenderKeyTweaked)

	transferID := uuid.MustParse(req.GetOriginalRequest().GetTransferId())
	transferEnt, err := sessionCtx.Client.Transfer.Get(ctx, transferID)
	require.NoError(t, err)
	receivers, err := transferEnt.QueryTransferReceivers().All(ctx)
	require.NoError(t, err)
	require.Len(t, receivers, 1)
	_, err = receivers[0].Update().SetStatus(st.TransferReceiverStatusKeyTweakLocked).Save(ctx)
	require.NoError(t, err)

	storedBytes, err := proto.Marshal(storedTweak)
	require.NoError(t, err)
	transferLeaf, err = transferLeaf.Update().SetKeyTweak(storedBytes).Save(ctx)
	require.NoError(t, err)
	incomingBytes := createReceiverClaimKeyTweakBytes(t, cfg, rand.NewChaCha8([32]byte{46}), transferLeafLeafID(t, ctx, transferLeaf))
	replaceClaimPackageKeyTweak(t, cfg, req, ownerIdentityPriv, incomingBytes)

	handler := NewClaimTransferFlowHandler(cfg)
	resp, err := handler.Prepare(ctx, req)
	require.NoError(t, err, "MIMO receiver at KEY_TWEAK_LOCKED must reuse the durable recovery polynomial")
	report, ok := resp.(*pbinternal.ClaimTransferPrepareResponse)
	require.True(t, ok)
	require.Len(t, report.GetLeafTweakDigests(), 1)
	assert.Equal(t, claimTweakProofsDigest(storedTweak.GetSecretShareTweak().GetProofs()), report.GetLeafTweakDigests()[0].GetProofsHash())

	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())
	updated, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
	require.NoError(t, err)
	assert.Equal(t, storedBytes, updated.KeyTweak)
}

func TestClaimTransferPrepare_MIMOPreservesStoredTweakAndStagesFreshSibling(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	req, storedLeaf, storedTweak, ownerIdentityPriv := buildFreshClaimRequestWithRealTweak(
		t, ctx, sessionCtx.Client, cfg, st.TransferStatusSenderKeyTweaked,
	)
	freshLeaf, freshTweak := appendFreshClaimLeafToRequest(
		t, ctx, sessionCtx.Client, cfg, req, ownerIdentityPriv,
	)

	storedBytes, err := proto.Marshal(storedTweak)
	require.NoError(t, err)
	storedLeaf, err = storedLeaf.Update().SetKeyTweak(storedBytes).Save(ctx)
	require.NoError(t, err)

	incomingStoredLeafBytes := createReceiverClaimKeyTweakBytes(
		t, cfg, rand.NewChaCha8([32]byte{45}), transferLeafLeafID(t, ctx, storedLeaf),
	)
	incomingStoredLeafTweak := &pb.ClaimLeafKeyTweak{}
	require.NoError(t, proto.Unmarshal(incomingStoredLeafBytes, incomingStoredLeafTweak))
	replaceClaimPackageKeyTweaks(
		t, cfg, req, ownerIdentityPriv,
		[]*pb.ClaimLeafKeyTweak{incomingStoredLeafTweak, freshTweak},
	)

	handler := NewClaimTransferFlowHandler(cfg)
	_, err = handler.Prepare(ctx, req)
	require.NoError(t, err, "a MIMO retry must preserve saved leaf tweaks and stage fresh tweaks only for missing siblings")

	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	updatedStored, err := sessionCtx.Client.TransferLeaf.Get(ctx, storedLeaf.ID)
	require.NoError(t, err)
	assert.Equal(t, storedBytes, updatedStored.KeyTweak, "the durable leaf polynomial must remain authoritative")
	updatedFresh, err := sessionCtx.Client.TransferLeaf.Get(ctx, freshLeaf.ID)
	require.NoError(t, err)
	persistedFresh := &pb.ClaimLeafKeyTweak{}
	require.NoError(t, proto.Unmarshal(updatedFresh.KeyTweak, persistedFresh))
	assert.True(t, proto.Equal(freshTweak, persistedFresh), "the missing sibling must be staged from the signed fresh package")
}

// TestClaimTransferPrepare_CorruptStoredTweakFailsLoudly pins that unreadable
// persisted key_tweak bytes surface as a typed internal-data error instead of
// being silently replaced — corruption of a durable proof anchor is an
// operator-attention event, not stale data.
func TestClaimTransferPrepare_CorruptStoredTweakFailsLoudly(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	req, transferLeaf, _, _ := buildFreshClaimRequestWithRealTweak(t, ctx, sessionCtx.Client, cfg, st.TransferStatusReceiverKeyTweakLocked)

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

func TestClaimTransferPrepare_MalformedStoredTweakIsInternalDataInconsistency(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	req, transferLeaf, storedTweak, _ := buildFreshClaimRequestWithRealTweak(
		t, ctx, sessionCtx.Client, cfg, st.TransferStatusSenderKeyTweaked,
	)
	storedTweak.SecretShareTweak = nil
	storedBytes, err := proto.Marshal(storedTweak)
	require.NoError(t, err)
	transferLeaf, err = transferLeaf.Update().SetKeyTweak(storedBytes).Save(ctx)
	require.NoError(t, err)

	handler := NewClaimTransferFlowHandler(cfg)
	_, err = handler.Prepare(ctx, req)
	require.Error(t, err)
	require.Equal(t, codes.Internal, status.Code(err))
	require.ErrorContains(t, err, "stored key tweak")

	updated, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
	require.NoError(t, err)
	assert.Equal(t, storedBytes, updated.KeyTweak, "malformed stored bytes must be preserved for operator repair")
}

func TestClaimTransferPrepare_RejectsStoredTweakForDifferentLeaf(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	req, transferLeaf, storedTweak, _ := buildFreshClaimRequestWithRealTweak(
		t, ctx, sessionCtx.Client, cfg, st.TransferStatusSenderKeyTweaked,
	)
	actualLeafID := transferLeafLeafID(t, ctx, transferLeaf)
	storedTweak.LeafId = uuid.New().String()
	storedBytes, err := proto.Marshal(storedTweak)
	require.NoError(t, err)
	transferLeaf, err = transferLeaf.Update().SetKeyTweak(storedBytes).Save(ctx)
	require.NoError(t, err)

	handler := NewClaimTransferFlowHandler(cfg)
	_, err = handler.Prepare(ctx, req)
	require.Error(t, err)
	require.ErrorContains(t, err, "references tree node")
	require.ErrorContains(t, err, actualLeafID.String())

	updated, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
	require.NoError(t, err)
	assert.Equal(t, storedBytes, updated.KeyTweak, "mismatched stored bytes must be preserved for manual repair")
}

// TestClaimTransferPrepare_ReportsAppliedWhenResuming verifies that an applied
// SO reports its durable post-tweak keyshare state without a staged proof hash.
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
	require.Len(t, report.GetLeafTweakDigests(), 1)
	assert.Empty(t, report.GetLeafTweakDigests()[0].GetProofsHash())
	assert.Len(t, report.GetLeafTweakDigests()[0].GetPostTweakKeyshareHash(), sha256.Size)
}

// transferLeafLeafID resolves the tree-node id behind a transfer_leaf row.
func transferLeafLeafID(t *testing.T, ctx context.Context, transferLeaf *ent.TransferLeaf) uuid.UUID {
	t.Helper()
	leaf, err := transferLeaf.QueryLeaf().Only(ctx)
	require.NoError(t, err)
	return leaf.ID
}
