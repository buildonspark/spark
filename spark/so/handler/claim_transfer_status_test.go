package handler

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
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
)

// minimalClaimPackage is enough to satisfy parseClaimTransferRequest (it only
// requires a non-nil package). For every claimable status (pre-apply and the
// now-resumable RKA/RRS) the status gate passes and the request proceeds to
// leaf loading, where these fixtures (which create no TransferLeaf rows) fail in
// loadClaimReceiverLeaves with "no leaves to claim". For Completed the gate
// returns AlreadyExists first. Either way the contents past parse are never
// inspected, which is all these gate-level assertions need — full claimable-leaf
// fixtures live in the integration suite.
func minimalClaimPackage() *pb.ClaimPackage {
	return &pb.ClaimPackage{
		LeavesToClaim:   []*pb.UserSignedTxSigningJob{},
		KeyTweakPackage: map[string][]byte{"so1": []byte("data")},
	}
}

func runClaimPrepare(ctx context.Context, t *testing.T, transfer *ent.Transfer, ownerPubKey keys.Public) error {
	t.Helper()
	handler := NewClaimTransferFlowHandler(sparktesting.TestConfig(t))
	_, err := handler.Prepare(ctx, &pbinternal.ClaimTransferPrepareRequest{
		OriginalRequest: &pb.ClaimTransferRequest{
			TransferId:             transfer.ID.String(),
			OwnerIdentityPublicKey: ownerPubKey.Serialize(),
			ClaimPackage:           minimalClaimPackage(),
		},
	})
	return err
}

// TestClaimTransferPrepare_ClaimableStatusesPassGate confirms the status gate
// admits every claimable status — including the already-applied states
// (RKA/RRS). Those are resumable, not terminal: a prior attempt (or a legacy
// claim that committed the settle phase before refund signing during the
// KnobUseConsensusClaim cutover) can leave a durable RKA/RRS transfer that still
// needs its refunds signed and a finalize to reach COMPLETED. Returning
// AlreadyExists for them would strand such partials (the codex-P1 bug). Prepare
// resumes them instead (predicted owner = the already-post-tweak owner; sign
// with the on-disk keyshare; Commit's apply no-ops). Both MIMO and non-MIMO are
// covered. These requests proceed past the gate and then fail later on the
// deliberately-minimal claim package (no leaves), so the assertion is only that
// the error is NOT the terminal AlreadyExists code.
func TestClaimTransferPrepare_ClaimableStatusesPassGate(t *testing.T) {
	t.Run("non-MIMO", func(t *testing.T) {
		claimable := []st.TransferStatus{
			st.TransferStatusSenderKeyTweaked,
			st.TransferStatusReceiverKeyTweaked,
			st.TransferStatusReceiverKeyTweakLocked,
			st.TransferStatusReceiverKeyTweakApplied,
			st.TransferStatusReceiverRefundSigned,
		}
		for _, transferStatus := range claimable {
			t.Run(string(transferStatus), func(t *testing.T) {
				ctx, sessionCtx := db.ConnectToTestPostgres(t)
				rng := rand.NewChaCha8([32]byte{1})
				transfer := createTestTransfer(t, ctx, rng, sessionCtx.Client, transferStatus)

				err := runClaimPrepare(ctx, t, transfer, transfer.ReceiverIdentityPubkey)
				require.Error(t, err)
				require.NotEqual(t, codes.AlreadyExists, status.Code(err), "status %s must remain claimable (resumable)", transferStatus)
			})
		}
	})

	t.Run("MIMO", func(t *testing.T) {
		claimable := []st.TransferReceiverStatus{
			st.TransferReceiverStatusReceiverClaimPending,
			st.TransferReceiverStatusKeyTweaked,
			st.TransferReceiverStatusKeyTweakLocked,
			st.TransferReceiverStatusKeyTweakApplied,
			st.TransferReceiverStatusRefundSigned,
		}
		for _, receiverStatus := range claimable {
			t.Run(string(receiverStatus), func(t *testing.T) {
				ctx, sessionCtx := db.ConnectToTestPostgres(t)
				ctx = mimoEnabledContext(ctx)
				rng := rand.NewChaCha8([32]byte{2})
				receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
				senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

				transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverKeyTweakApplied)
				_, err := sessionCtx.Client.TransferReceiver.Create().
					SetTransferID(transfer.ID).
					SetIdentityPubkey(receiverPubKey).
					SetStatus(receiverStatus).
					SetTransferType(transfer.Type).
					Save(ctx)
				require.NoError(t, err)

				err = runClaimPrepare(ctx, t, transfer, receiverPubKey)
				require.Error(t, err)
				require.NotEqual(t, codes.AlreadyExists, status.Code(err), "receiver status %s must remain claimable (resumable)", receiverStatus)
			})
		}
	})
}

// TestClaimTransferPrepare_CompletedReturnsAlreadyExists confirms a fully
// completed claim is the only terminal status — re-claiming returns the
// deterministic AlreadyExists the SDK treats as idempotent success.
func TestClaimTransferPrepare_CompletedReturnsAlreadyExists(t *testing.T) {
	t.Run("non-MIMO", func(t *testing.T) {
		ctx, sessionCtx := db.ConnectToTestPostgres(t)
		rng := rand.NewChaCha8([32]byte{3})
		transfer := createTestTransfer(t, ctx, rng, sessionCtx.Client, st.TransferStatusCompleted)

		err := runClaimPrepare(ctx, t, transfer, transfer.ReceiverIdentityPubkey)
		require.Error(t, err)
		require.Equal(t, codes.AlreadyExists, status.Code(err))
	})

	t.Run("MIMO", func(t *testing.T) {
		ctx, sessionCtx := db.ConnectToTestPostgres(t)
		ctx = mimoEnabledContext(ctx)
		rng := rand.NewChaCha8([32]byte{4})
		receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverKeyTweakApplied)
		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusCompleted).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)

		err = runClaimPrepare(ctx, t, transfer, receiverPubKey)
		require.Error(t, err)
		require.Equal(t, codes.AlreadyExists, status.Code(err))
	})
}

// buildAppliedClaimRequest builds a single-leaf transfer at the given status
// whose leaf.OwnerSigningPubkey is the receiver's post-tweak key, plus a
// fully-signed ClaimPackage whose refund txs pay that post-tweak key. The SE
// round-1 signing commitments deliberately exclude cfg.Identifier so this SO is
// NOT in the signing set — Prepare then returns nil shares right after the
// refund-validation/predicted-owner work, before any FROST round-2 call (which
// would need the gripmock signer). That lets the test verify the applied-resume
// branch end-to-end with postgres only.
func buildAppliedClaimRequest(t *testing.T, ctx context.Context, client *ent.Client, cfg *so.Config, status st.TransferStatus) *pbinternal.ClaimTransferPrepareRequest {
	t.Helper()
	rng := rand.NewChaCha8([32]byte{7})
	keyshare := createTestSigningKeyshare(t, ctx, rng, client)
	ownerIdentityPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPriv.Public(), client)
	leaf := createTestTreeNode(t, ctx, rng, client, tree, keyshare)

	// The receiver's post-tweak signing key. On a real RKA transfer this is what
	// SettleReceiverKeyTweak already wrote to leaf.OwnerSigningPubkey.
	postTweakOwner := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	_, err := leaf.Update().SetOwnerSigningPubkey(postTweakOwner).Save(ctx)
	require.NoError(t, err)

	transfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(status).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetReceiverIdentityPubkey(ownerIdentityPriv.Public()).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	createTestTransferLeaf(t, ctx, client, transfer, leaf)

	// SE round-1 commitments for two operators OTHER than this SO, so the
	// signing-set filter drops this SO (non-signer) and Prepare returns before
	// FROST round-2.
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
	userJob := func(rawTx []byte) *pb.UserSignedTxSigningJob {
		return &pb.UserSignedTxSigningJob{
			LeafId:                 leaf.ID.String(),
			SigningPublicKey:       postTweakOwner.Serialize(),
			RawTx:                  rawTx,
			SigningNonceCommitment: createTestSigningCommitment(rng),
			SigningCommitments:     signingCommitments,
		}
	}

	refundJob := createTestLeafRefundTxSigningJob(t, rng, leaf, postTweakOwner)
	keyTweakPackage := map[string][]byte{"so1": []byte("data")}
	signingPayload := common.GetClaimPackageSigningPayload(transfer.ID, keyTweakPackage)
	sig := ecdsa.Sign(ownerIdentityPriv.ToBTCEC(), signingPayload).Serialize()

	pkg := &pb.ClaimPackage{
		HashVariant:                 pb.HashVariant_HASH_VARIANT_V2,
		UserSignature:               sig,
		KeyTweakPackage:             keyTweakPackage,
		LeavesToClaim:               []*pb.UserSignedTxSigningJob{userJob(refundJob.RefundTxSigningJob.RawTx)},
		DirectLeavesToClaim:         []*pb.UserSignedTxSigningJob{userJob(refundJob.DirectRefundTxSigningJob.RawTx)},
		DirectFromCpfpLeavesToClaim: []*pb.UserSignedTxSigningJob{userJob(refundJob.DirectFromCpfpRefundTxSigningJob.RawTx)},
	}
	return &pbinternal.ClaimTransferPrepareRequest{
		OriginalRequest: &pb.ClaimTransferRequest{
			TransferId:             transfer.ID.String(),
			OwnerIdentityPublicKey: ownerIdentityPriv.Public().Serialize(),
			ClaimPackage:           pkg,
		},
	}
}

// TestClaimTransferPrepare_AppliedResume_ValidatesAgainstPostTweakOwner is the
// regression guard for the applied-resume branch: at RKA the tweak is already
// applied, so Prepare must validate the submitted refunds against the
// already-post-tweak leaf.OwnerSigningPubkey directly. With the previous
// (pre-apply) predicted = owner - tweak math, validateReceivedRefundTransactions
// would reject the refund tx with a codes.InvalidArgument "signing pubkey does
// not match" error. This drives a real RKA transfer whose refunds pay the
// post-tweak owner, with this SO as a non-signer so Prepare returns cleanly
// (nil) right after the refund-validation work — no FROST round-2 / gripmock
// needed. FROST signature correctness on the on-disk keyshare is covered by the
// minikube integration suite.
func TestClaimTransferPrepare_AppliedResume_ValidatesAgainstPostTweakOwner(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	req := buildAppliedClaimRequest(t, ctx, sessionCtx.Client, cfg, st.TransferStatusReceiverKeyTweakApplied)

	handler := NewClaimTransferFlowHandler(cfg)
	resp, err := handler.Prepare(ctx, req)
	require.NoError(t, err, "applied-resume must validate refunds against the post-tweak owner and pass")
	assert.Nil(t, resp, "non-signing SO returns nil shares after the applied-resume validation")
}
