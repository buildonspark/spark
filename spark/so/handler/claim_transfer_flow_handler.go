package handler

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	sparkdb "github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferreceiver "github.com/lightsparkdev/spark/so/ent/transferreceiver"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/handler/signing_handler"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ---------------------------------------------------------------------------
// Public entry point (consensus path)
// ---------------------------------------------------------------------------

// claimTransferConsensus runs the claim flow through the 2PC consensus engine
// instead of the legacy cross-SO fanout (settleReceiverKeyTweakWithClaimPackage
// + finalize gossip). Gated by KnobUseConsensusClaim at the public
// ClaimTransfer entry point.
//
// Coordinator-side preflight (mirrors claimTransferLegacy so the consensus
// path has the same fast-fail behavior — same observable error codes, same
// rejection ordering — before any cross-SO ConsensusPrepare fan-out):
//
//  1. parse the request (transferID, ownerIDPK, claimPackage)
//  2. session-auth the parsed owner identity pubkey
//  3. NoWait FOR UPDATE on the transfer row (fast-fail on concurrent claims)
//  4. load receiver under FOR UPDATE
//  5. non-MIMO: rejectLegacyAggregateClaimForMultiReceiverTransfer + identity match
//     MIMO: validateTransferReadyForReceiverClaim + checkCoopExitTxBroadcasted
//  6. validateClaimStatus (claimable status switch)
//  7. loadClaimReceiverLeaves + leaf-count parity vs claimPackage.LeavesToClaim
//  8. validateClaimPackageStructure (DFC coverage, KeyTweakPackage non-empty)
//  9. verifyClaimPackageSignature (user signature over the encrypted package)
//
// The receiver leaves loaded at step 7 are then passed into
// buildClaimTransferCoordinatorFlow which only does signing-job
// pre-computation for BuildCommitPayload's FROST aggregation — no second
// round-trip.
//
// Participant Prepare repeats the same checks (defense-in-depth) so the
// engine is the authoritative validator; the coordinator preflight just
// gives the fast path: it surfaces deterministic gRPC codes to the SDK and
// avoids wasted RPC fanout / peer locks on requests the coordinator can
// reject on its own.
func (h *TransferHandler) claimTransferConsensus(ctx context.Context, req *pb.ClaimTransferRequest) (*pb.ClaimTransferResponse, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.ClaimTransfer.consensus")
	defer span.End()

	// Parse the request once. parseClaimTransferRequest returns
	// InvalidArgument-tagged errors for malformed owner pubkey, malformed
	// transfer id, and missing claim_package; legacy returns plain errors
	// (gRPC Unknown) for the same conditions. The new codes are more
	// accurate; the rollout note in KnobUseConsensusClaim's doc covers the
	// observable change in SDK retry semantics.
	parsed, err := parseClaimTransferRequest(req)
	if err != nil {
		return nil, err
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, parsed.ownerIDPK); err != nil {
		return nil, err
	}
	// Mirror claimTransferLegacy: a kill-switched wallet must be blocked on the
	// consensus path too. Without this the cutover would silently re-open
	// claims for kill-switched wallets the moment KnobUseConsensusClaim flips.
	if err := authz.EnforceWalletNotKillSwitched(ctx, parsed.ownerIDPK); err != nil {
		return nil, err
	}

	// NoWait FOR UPDATE on the transfer row so two concurrent claims on the
	// same transfer fail fast with AbortedConcurrentClaimConflict rather than
	// queuing on the row lock. Reuses claimLockConflictError so the lock
	// failure is logged with the Postgres-level cause server-side (the
	// client only sees the generic gRPC code).
	handler := NewClaimTransferFlowHandler(h.config)
	transferEnt, err := handler.loadTransferForUpdate(ctx, parsed.transferID, sql.WithLockAction(sql.NoWait))
	if err != nil {
		if sparkdb.IsLockNotAvailableError(err) {
			return nil, claimLockConflictError(ctx, parsed.transferID, err)
		}
		return nil, fmt.Errorf("unable to load transfer %s: %w", parsed.transferID, err)
	}
	// Match legacy: tag the span with transfer_type so tracing of consensus-
	// path claims carries the same dimension as legacy.
	span.SetAttributes(transferTypeKey.String(string(transferEnt.Type)))
	_, receiver, err := handler.loadTransferReceiverByPublicKeyForUpdate(ctx, transferEnt, &parsed.ownerIDPK)
	if err != nil {
		return nil, err
	}
	isMimo := isMimoReceiveEnabled(ctx, receiver)

	// Match legacy's coordinator-side gates BEFORE the engine fan-out. The
	// same gates run again inside each SO's Prepare (defense-in-depth); this
	// layer just gives us legacy's fast-fail behavior + deterministic gRPC
	// codes back to the SDK. Check ordering mirrors claimTransferLegacy
	// exactly: non-MIMO does rejectLegacyAggregate before the identity
	// check; MIMO does readiness + coop-exit guards.
	if !isMimo {
		if err := rejectLegacyAggregateClaimForMultiReceiverTransfer(ctx, transferEnt); err != nil {
			return nil, err
		}
		if !transferEnt.ReceiverIdentityPubkey.Equals(parsed.ownerIDPK) {
			return nil, fmt.Errorf("cannot claim transfer %s, receiver identity public key mismatch", parsed.transferID)
		}
	} else {
		if err := validateTransferReadyForReceiverClaim(transferEnt); err != nil {
			return nil, err
		}
		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get db: %w", err)
		}
		if err := checkCoopExitTxBroadcasted(ctx, db, transferEnt); err != nil {
			return nil, err
		}
	}
	if err := validateClaimStatus(parsed.transferID, transferEnt, receiver, isMimo); err != nil {
		return nil, err
	}

	// Load receiver leaves so we can do the leaf-count parity check the
	// legacy path runs before signature verify (catches a wrong-leaf-set
	// claim package on the coordinator instead of after fanning out to every
	// peer). The loaded leaves feed buildClaimTransferCoordinatorFlow below.
	// Count against len(leavesByID) (deduplicated by TreeNode.ID) so the
	// coordinator and participant Prepare reach the same decision under any
	// data-integrity edge case where two TransferLeaf rows reference the
	// same TreeNode.
	_, leavesByID, err := loadClaimReceiverLeaves(ctx, transferEnt, receiver)
	if err != nil {
		return nil, err
	}
	if len(leavesByID) != len(parsed.claimPackage.LeavesToClaim) {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("inconsistent leaves to claim for transfer %s: expected %d, got %d", parsed.transferID, len(leavesByID), len(parsed.claimPackage.LeavesToClaim)))
	}
	if err := validateClaimPackageStructure(parsed.claimPackage); err != nil {
		return nil, err
	}
	if err := verifyClaimPackageSignature(parsed.transferID, parsed.claimPackage, parsed.ownerIDPK); err != nil {
		return nil, err
	}

	// Pre-compute the per-leaf aggregation jobs from the already-loaded
	// receiver leaves — no redundant query.
	flow, err := buildClaimTransferCoordinatorFlow(ctx, handler, req, parsed, leavesByID)
	if err != nil {
		return nil, err
	}

	engine, err := consensus.GetEngine(ctx)
	if err != nil {
		return nil, err
	}
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	if _, err := engine.Execute(ctx,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_CLAIM_TRANSFER,
		&selection,
		flow,
	); err != nil {
		return nil, fmt.Errorf("consensus claim transfer failed: %w", err)
	}
	if flow.response == nil {
		return nil, fmt.Errorf("internal: consensus claim transfer for %s succeeded but produced no response", req.GetTransferId())
	}
	return flow.response, nil
}

// ---------------------------------------------------------------------------
// ClaimTransferFlowHandler — participant side (Prepare / Commit / Rollback)
// ---------------------------------------------------------------------------

// ClaimTransferFlowHandler implements consensus.FlowHandler for
// CONSENSUS_OPERATION_TYPE_CLAIM_TRANSFER.
//
// Embeds *TransferHandler for access to the existing claim helpers
// (InitiateSettleReceiverKeyTweak, SettleReceiverKeyTweak,
// prepareClaimRefundSigningJobs, revertClaimTransfer, etc.). Reached via the
// engine when ClaimTransfer routes through it (gated on
// KnobUseConsensusClaim).
type ClaimTransferFlowHandler struct {
	*TransferHandler
}

var _ consensus.FlowHandler = (*ClaimTransferFlowHandler)(nil)

func NewClaimTransferFlowHandler(config *so.Config) *ClaimTransferFlowHandler {
	return &ClaimTransferFlowHandler{TransferHandler: NewTransferHandler(config)}
}

// Prepare runs on every SO. It validates the claim package, decrypts this SO's
// slice of the receiver key tweaks, persists them on the transfer_leaf rows
// (status moves to RECEIVER_KEY_TWEAK_LOCKED), validates the key tweak proofs,
// persists the new refund txs on each leaf, and produces local FROST round-2
// signature shares for the leaves where this SO is part of the signing set.
//
// SOs outside the signing set still persist state; they return nil shares.
func (h *ClaimTransferFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	req, ok := op.(*pbinternal.ClaimTransferPrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for claim transfer prepare", op)
	}
	orig := req.GetOriginalRequest()
	if orig == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("original_request is required"))
	}

	parsed, err := parseClaimTransferRequest(orig)
	if err != nil {
		return nil, err
	}

	transferEnt, receiver, err := h.loadClaimContext(ctx, parsed)
	if err != nil {
		return nil, err
	}
	isMimo := isMimoReceiveEnabled(ctx, receiver)

	// Non-MIMO gates — ordered to match legacy claimTransferLegacy /
	// claimTransferConsensus preflight: rejectLegacyAggregate (multi-receiver
	// guard) runs BEFORE the identity check so a multi-receiver request
	// with a wrong claimer surfaces the same error code at every layer.
	if !isMimo {
		if err := rejectLegacyAggregateClaimForMultiReceiverTransfer(ctx, transferEnt); err != nil {
			return nil, err
		}
		// loadTransferReceiverByPublicKeyForUpdate returns (false, nil, nil)
		// for non-MIMO when no receiver row matches; without this guard a
		// wrong claimer would slip past validation.
		if !transferEnt.ReceiverIdentityPubkey.Equals(parsed.ownerIDPK) {
			return nil, fmt.Errorf("cannot claim transfer %s, receiver identity public key mismatch", parsed.transferID)
		}
	}

	// MIMO-only readiness checks: transfer must be past the sender's key
	// tweak (validateTransferReadyForReceiverClaim) and the cooperative-exit
	// tx must not have broadcast yet.
	if isMimo {
		if err := validateTransferReadyForReceiverClaim(transferEnt); err != nil {
			return nil, err
		}
		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get db: %w", err)
		}
		if err := checkCoopExitTxBroadcasted(ctx, db, transferEnt); err != nil {
			return nil, err
		}
	}

	if err := validateClaimStatus(parsed.transferID, transferEnt, receiver, isMimo); err != nil {
		return nil, err
	}

	// Load receiver leaves + leaf-count parity check, then structural
	// validation, then signature verify — matches legacy
	// claimTransferLegacy's ordering exactly so error precedence is
	// consistent with the coordinator preflight regardless of which SO
	// rejects the request first.
	transferLeavesPre, leavesByID, err := loadClaimReceiverLeaves(ctx, transferEnt, receiver)
	if err != nil {
		return nil, err
	}
	if len(leavesByID) != len(parsed.claimPackage.LeavesToClaim) {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("inconsistent leaves to claim for transfer %s: expected %d, got %d", parsed.transferID, len(leavesByID), len(parsed.claimPackage.LeavesToClaim)))
	}
	if err := validateClaimPackageStructure(parsed.claimPackage); err != nil {
		return nil, err
	}
	if err := verifyClaimPackageSignature(parsed.transferID, parsed.claimPackage, parsed.ownerIDPK); err != nil {
		return nil, err
	}

	// A claim can re-enter at an already-applied state (KeyTweakApplied /
	// RefundSigned): a prior consensus attempt, or — during the
	// KnobUseConsensusClaim cutover — a legacy claim that committed the settle
	// phase before refund signing and then crashed. The key is tweaked exactly
	// once and the apply is idempotent, so the two cases need different Prepare
	// work:
	//
	//   - applied (RKA/RRS): the on-disk keyshare and leaf.OwnerSigningPubkey are
	//     already post-tweak. Do NOT re-lock or re-tweak. Validate the submitted
	//     refunds against the current owner and FROST-sign with the on-disk
	//     keyshare as-is. Commit's Phase-2 apply then no-ops (SettleReceiverKeyTweak
	//     returns nil for an applied status) and just finalizes the signatures.
	//     This mirrors the legacy refund-signing-only resume.
	//
	//   - pre-apply (SKT/RKT/RKL): stage the tweak (Phase-1 lock) and sign with an
	//     in-memory post-tweak key package; the durable keyshare apply waits for
	//     Commit.
	applied := isCommittedClaim(ctx, transferEnt, receiver)

	predictedOwnerByLeaf := make(map[string]keys.Public, len(parsed.claimPackage.LeavesToClaim))
	// tweaksByLeaf is only needed on the pre-apply path (Phase-1 lock + in-memory
	// FROST tweak folding); it stays nil when the tweak is already applied.
	var tweaksByLeaf map[string]*pb.ClaimLeafKeyTweak

	if applied {
		// leaf.OwnerSigningPubkey is already the receiver's post-tweak key.
		// Log because this resume path only fires for partials left behind by a
		// prior attempt / the legacy→consensus cutover — useful for confirming
		// the cutover drains and for knowing when this branch can be retired.
		logging.GetLoggerFromContext(ctx).Sugar().Infof(
			"claim transfer 2pc prepare: resuming already-applied claim for transfer %s (status %s) — signing refunds against the post-tweak owner without re-tweaking",
			parsed.transferID, transferEnt.Status)
		for _, job := range parsed.claimPackage.LeavesToClaim {
			leaf, ok := leavesByID[job.LeafId]
			if !ok {
				return nil, fmt.Errorf("claim references unknown leaf %s", job.LeafId)
			}
			predictedOwnerByLeaf[job.LeafId] = leaf.OwnerSigningPubkey
		}
	} else {
		// useStoredKeyTweaks: if Phase 1 (lock) already committed key tweaks on
		// this SO from a prior coordinator attempt (RKL), the stored proofs may
		// not match a fresh claim package's decryption. Read tweaks from
		// leaf.KeyTweak instead of decrypting the package, and forward neither
		// the package nor the user signature on the settle call — the participant
		// settle helper detects already-locked state and skips re-stamping.
		useStoredKeyTweaks := shouldUseStoredKeyTweaks(ctx, transferEnt, receiver)
		var encryptedPackage map[string][]byte
		var claimSignature []byte
		if useStoredKeyTweaks {
			tweaksByLeaf, err = decryptStoredClaimKeyTweaks(h.config, transferLeavesPre)
			if err != nil {
				return nil, err
			}
		} else {
			tweaksByLeaf, err = decryptClaimKeyTweaks(h.config, parsed.claimPackage)
			if err != nil {
				return nil, err
			}
			encryptedPackage = parsed.claimPackage.KeyTweakPackage
			claimSignature = parsed.claimPackage.UserSignature
		}
		keyTweakProofs := make(map[string]*pb.SecretProof, len(tweaksByLeaf))
		for leafID, leafTweak := range tweaksByLeaf {
			keyTweakProofs[leafID] = &pb.SecretProof{Proofs: leafTweak.SecretShareTweak.Proofs}
		}

		userPublicKeys := make(map[string][]byte, len(parsed.claimPackage.LeavesToClaim))
		for _, job := range parsed.claimPackage.LeavesToClaim {
			userPublicKeys[job.LeafId] = job.SigningPublicKey
		}

		// Phase-1 lock only: persist the encrypted tweak package + per-leaf
		// proofs, validate against the claim signature, advance status to
		// KeyTweakLocked. We deliberately do NOT call the Phase-2 apply
		// (SettleReceiverKeyTweak with COMMIT) here — that mutates the
		// signing keyshare and tree node ownership, which under the consensus
		// engine must wait until Commit. The engine's ent.Tx is rolled back on
		// any Prepare failure, undoing this Phase-1 lock cleanly.
		var receiverIDPKBytes []byte
		if isMimo {
			receiverIDPKBytes = receiver.IdentityPubkey.Serialize()
		}
		settleReq := &pbinternal.InitiateSettleReceiverKeyTweakRequest{
			TransferId:                    parsed.transferID.String(),
			KeyTweakProofs:                keyTweakProofs,
			UserPublicKeys:                userPublicKeys,
			ReceiverIdentityPublicKey:     receiverIDPKBytes,
			EncryptedClaimKeyTweakPackage: encryptedPackage,
			ClaimSignature:                claimSignature,
		}
		if err := h.InitiateSettleReceiverKeyTweak(ctx, settleReq); err != nil {
			return nil, fmt.Errorf("unable to settle receiver key tweak locally: %w", err)
		}

		// Predicted post-tweak owner pubkey per leaf:
		//   predicted = leaf.OwnerSigningPubkey - SecretShareTweak.Proofs[0]
		// where Proofs[0] is the public commitment (G * secret_share_tweak) to
		// the additive scalar tweak. The leaves still carry pre-tweak
		// OwnerSigningPubkey (Phase-2 apply is deferred to Commit), so
		// validateReceivedRefundTransactions needs the predicted value to verify
		// the refund tx pays the receiver's new key.
		for leafID, leafTweak := range tweaksByLeaf {
			leaf, ok := leavesByID[leafID]
			if !ok {
				return nil, fmt.Errorf("claim tweak references unknown leaf %s", leafID)
			}
			pubKeyTweak, err := keys.ParsePublicKey(leafTweak.SecretShareTweak.Proofs[0])
			if err != nil {
				return nil, fmt.Errorf("unable to parse public key tweak for leaf %s: %w", leafID, err)
			}
			predictedOwnerByLeaf[leafID] = leaf.OwnerSigningPubkey.Sub(pubKeyTweak)
		}
	}

	signingJobsResult, err := h.prepareClaimRefundSigningJobs(ctx, parsed.claimPackage, leavesByID, transferEnt, predictedOwnerByLeaf)
	if err != nil {
		return nil, err
	}

	// Build the keyshare → leaf-id map BEFORE marshaling rewrites JobIDs.
	// prepareClaimRefundSigningJobs builds helper.SigningJobWithPregeneratedNonce
	// with uuid.New() random JobIDs and keys leafJobMap by those random IDs.
	// marshalClaimSigningJobsForFrost mutates each job.JobID to a deterministic
	// (transferID, leafID, txKind) UUID — after that, leafJobMap can no longer
	// be looked up by JobId. Walk the result here while the random IDs still
	// match, and use the keyshare ID (which marshaling doesn't touch) as the
	// stable key into the tweaked-key-packages lookup downstream.
	keyshareToLeafID := make(map[uuid.UUID]string, len(signingJobsResult.signingJobs))
	for _, job := range signingJobsResult.signingJobs {
		leaf, ok := signingJobsResult.leafJobMap[job.JobID]
		if !ok {
			return nil, fmt.Errorf("internal: signing job %s missing leaf mapping", job.JobID)
		}
		keyshareToLeafID[job.SigningKeyshareID] = leaf.ID.String()
	}

	jobs, err := marshalClaimSigningJobsForFrost(parsed.transferID, signingJobsResult)
	if err != nil {
		return nil, err
	}
	jobs = filterClaimJobsForThisOperator(jobs, h.config.Identifier)
	if len(jobs) == 0 {
		// This SO isn't in any leaf's t-of-n signing set; Phase-1 DB writes
		// still happened above, but there are no FROST shares to contribute.
		logging.GetLoggerFromContext(ctx).Sugar().Debugf("claim transfer 2pc prepare: SO %s not in signing set for transfer %s, returning nil shares", h.config.Identifier, parsed.transferID)
		return nil, nil
	}

	frostHandler := signing_handler.NewFrostSigningHandler(h.config)
	var frostResp *pbinternal.FrostRound2Response
	if applied {
		// On-disk keyshare is already post-tweak — sign with it directly. Folding
		// the tweak again would double-tweak and produce an invalid signature.
		frostResp, err = frostHandler.FrostRound2(ctx, &pbinternal.FrostRound2Request{SigningJobs: jobs})
	} else {
		// Load each job's KeyPackage and fold the receiver key tweak in-memory
		// before signing. The on-disk keyshare stays at the pre-tweak value —
		// FROST signs with the post-tweak share via the fresh KeyPackage. The
		// durable keyshare apply happens in Commit.
		var keyPackages map[uuid.UUID]*pbfrost.KeyPackage
		keyPackages, err = tweakedKeyPackagesForFrost(ctx, h.config, jobs, keyshareToLeafID, tweaksByLeaf)
		if err != nil {
			return nil, fmt.Errorf("unable to build tweaked key packages: %w", err)
		}
		frostResp, err = frostHandler.FrostRound2WithKeyPackages(ctx, &pbinternal.FrostRound2Request{SigningJobs: jobs}, keyPackages)
	}
	if err != nil {
		return nil, fmt.Errorf("local frost round 2 failed during prepare: %w", err)
	}
	return frostResp, nil
}

// tweakedKeyPackagesForFrost loads the KeyPackage for every job's keyshare
// and folds the receiver key tweak for that leaf in-memory. Result is keyed
// by keyshare ID for FrostRound2WithKeyPackages. The on-disk keyshare is
// never read or written here — only the loaded KeyPackage struct is mutated,
// and the result is local to this call.
//
// keyshareToLeafID must cover every keyshare referenced by jobs. The caller
// builds it from prepareClaimRefundSigningJobs's leafJobMap before
// marshalClaimSigningJobsForFrost rewrites the JobIDs, since the keyshare ID
// is the only field that survives the rewrite untouched.
func tweakedKeyPackagesForFrost(
	ctx context.Context,
	config *so.Config,
	jobs []*pbinternal.SigningJob,
	keyshareToLeafID map[uuid.UUID]string,
	tweaksByLeaf map[string]*pb.ClaimLeafKeyTweak,
) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
	keyshareIDs := make([]uuid.UUID, 0, len(jobs))
	seen := make(map[uuid.UUID]struct{}, len(jobs))
	for _, job := range jobs {
		ksID, err := uuid.Parse(job.KeyshareId)
		if err != nil {
			return nil, fmt.Errorf("invalid keyshare id %s: %w", job.KeyshareId, err)
		}
		if _, dup := seen[ksID]; dup {
			continue
		}
		seen[ksID] = struct{}{}
		if _, ok := keyshareToLeafID[ksID]; !ok {
			return nil, fmt.Errorf("no leaf mapping for keyshare %s", ksID)
		}
		keyshareIDs = append(keyshareIDs, ksID)
	}
	loaded, err := ent.GetKeyPackages(ctx, config, keyshareIDs)
	if err != nil {
		return nil, fmt.Errorf("get key packages: %w", err)
	}
	out := make(map[uuid.UUID]*pbfrost.KeyPackage, len(loaded))
	for ksID, kp := range loaded {
		if kp == nil {
			return nil, fmt.Errorf("signing keyshare %s not found", ksID)
		}
		leafID := keyshareToLeafID[ksID]
		tweak, ok := tweaksByLeaf[leafID]
		if !ok {
			return nil, fmt.Errorf("no tweak for leaf %s (keyshare %s)", leafID, ksID)
		}
		tweaked, err := signing_handler.ApplyKeysharePackageTweak(
			kp,
			tweak.SecretShareTweak.SecretShare,
			tweak.SecretShareTweak.Proofs[0],
			tweak.PubkeySharesTweak,
		)
		if err != nil {
			return nil, fmt.Errorf("apply tweak for leaf %s: %w", leafID, err)
		}
		out[ksID] = tweaked
	}
	return out, nil
}

// Commit runs on every participant (the coordinator's equivalent work lives in
// BuildCommitPayload). It applies the aggregated refund signatures to the
// TreeNode rows via finalizeHandler.updateNode, applies the key tweaks via
// SettleReceiverKeyTweak(COMMIT), and marks the transfer + receiver Completed.
func (h *ClaimTransferFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.ClaimTransferCommitRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for claim transfer commit", op)
	}
	return h.applyClaimTransferCommit(ctx, req)
}

// Rollback runs on every participant (and on the coordinator if Prepare or
// BuildCommitPayload fails). It reverts any receiver-side state Prepare wrote
// — clearing stored key tweaks and restoring the transfer to
// SENDER_KEY_TWEAKED so the receiver can retry. Idempotent: a never-prepared
// transfer is a no-op; revertClaimTransfer treats terminal states as a no-op.
//
// Accepts both ClaimTransferRollbackRequest (the normal rollback payload) and
// ClaimTransferPrepareRequest (echoed back by the reconciler when the
// coordinator's row was lost).
func (h *ClaimTransferFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	var (
		transferIDStr   string
		receiverPKBytes []byte
	)
	switch r := op.(type) {
	case *pbinternal.ClaimTransferRollbackRequest:
		transferIDStr = r.GetTransferId()
		receiverPKBytes = r.GetReceiverIdentityPublicKey()
	case *pbinternal.ClaimTransferPrepareRequest:
		// Reconciler-echoed prepare op (coordinator row lost). The original
		// claim request carries the claimer's identity; thread it through so
		// MIMO rollbacks revert the right receiver.
		if orig := r.GetOriginalRequest(); orig != nil {
			transferIDStr = orig.GetTransferId()
			receiverPKBytes = orig.GetOwnerIdentityPublicKey()
		}
	default:
		return fmt.Errorf("unexpected operation type %T for claim transfer rollback", op)
	}
	if transferIDStr == "" {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_id is required for rollback"))
	}
	transferID, err := uuid.Parse(transferIDStr)
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer_id: %w", err))
	}
	var receiverPK *keys.Public
	if len(receiverPKBytes) > 0 {
		parsed, err := keys.ParsePublicKey(receiverPKBytes)
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid receiver_identity_public_key on rollback: %w", err))
		}
		receiverPK = &parsed
	}
	return h.rollbackClaimTransfer(ctx, transferID, receiverPK)
}

// applyClaimTransferCommit applies aggregated signatures + key tweaks on a
// single SO. Shared by participant Commit and coordinator BuildCommitPayload.
//
// Idempotent against replayed commit gossip: if the transfer is already past
// the pre-commit states (KeyTweakLocked / KeyTweakApplied /
// ReceiverRefundSigned), short-circuit when status is already Completed.
// Anything before KeyTweakLocked means Prepare never landed on this SO,
// which is a real invariant violation we want to surface.
func (h *ClaimTransferFlowHandler) applyClaimTransferCommit(ctx context.Context, req *pbinternal.ClaimTransferCommitRequest) error {
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer_id: %w", err))
	}

	transferEnt, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s for commit: %w", transferID, err)
	}

	// Pick the receiver identity from the commit payload when the coordinator
	// provided one (MIMO transfers can have multiple receivers — the claimer's
	// identity differs from transferEnt.ReceiverIdentityPubkey). Fall back to
	// the transfer's primary receiver when the field is empty (non-MIMO, or
	// a legacy gossip payload).
	receiverPK := transferEnt.ReceiverIdentityPubkey
	if rawPK := req.GetReceiverIdentityPublicKey(); len(rawPK) > 0 {
		parsedPK, err := keys.ParsePublicKey(rawPK)
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid receiver_identity_public_key on commit: %w", err))
		}
		receiverPK = parsedPK
	}
	_, receiver, err := h.loadTransferReceiverByPublicKeyForUpdate(ctx, transferEnt, &receiverPK)
	if err != nil {
		return err
	}

	if isMimoReceiveEnabled(ctx, receiver) {
		switch receiver.Status {
		case st.TransferReceiverStatusKeyTweakLocked,
			st.TransferReceiverStatusKeyTweakApplied,
			st.TransferReceiverStatusRefundSigned:
			// fall through
		case st.TransferReceiverStatusCompleted:
			logging.GetLoggerFromContext(ctx).Sugar().Infof(
				"claim transfer 2pc commit: receiver for transfer %s already completed, treating as idempotent retry",
				transferID)
			return nil
		default:
			return fmt.Errorf("claim transfer 2pc commit: receiver for transfer %s is in unexpected status %s", transferID, receiver.Status)
		}
	} else {
		switch transferEnt.Status {
		case st.TransferStatusReceiverKeyTweakLocked,
			st.TransferStatusReceiverKeyTweakApplied,
			st.TransferStatusReceiverRefundSigned:
			// fall through
		case st.TransferStatusCompleted:
			logging.GetLoggerFromContext(ctx).Sugar().Infof(
				"claim transfer 2pc commit: transfer %s already completed, treating as idempotent retry",
				transferID)
			return nil
		default:
			return fmt.Errorf("claim transfer 2pc commit: transfer %s is in unexpected status %s", transferID, transferEnt.Status)
		}
	}

	// Phase-2 apply: mutate the signing keyshare + tree node ownership using
	// the encrypted tweak persisted during Prepare's Phase-1 lock. Idempotent
	// against replayed commit gossip: when receiver/transfer status is already
	// KeyTweakApplied or further, SettleReceiverKeyTweak returns nil.
	//
	// For the coordinator path, BuildCommitPayload already applied the tweak
	// before FROST aggregation (so leaf.OwnerSigningPubkey holds the
	// receiver's new pubkey when aggregateLeafSignature reads it); this second
	// call is a no-op there.
	var receiverIDPKBytes []byte
	if receiver != nil {
		receiverIDPKBytes = receiver.IdentityPubkey.Serialize()
	}
	applyReq := &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId:                transferID.String(),
		Action:                    pbinternal.SettleKeyTweakAction_COMMIT,
		ReceiverIdentityPublicKey: receiverIDPKBytes,
	}
	if err := h.SettleReceiverKeyTweak(ctx, applyReq); err != nil {
		return fmt.Errorf("unable to apply receiver key tweaks during commit: %w", err)
	}

	// Reload after the apply advanced status + mutated tree node ownership.
	transferEnt, err = h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return fmt.Errorf("unable to reload transfer %s after settle: %w", transferID, err)
	}
	if receiver != nil {
		_, receiver, err = h.loadTransferReceiverByPublicKeyForUpdate(ctx, transferEnt, &receiver.IdentityPubkey)
		if err != nil {
			return err
		}
	}

	// Move transfer + receiver to RECEIVER_REFUND_SIGNED before applying
	// signatures. Updates are independently gated: for MIMO, transferEnt is
	// often already at ReceiverRefundSigned (from a prior receiver's claim)
	// while THIS receiver row is still at KeyTweakApplied — the receiver
	// update must still fire so each receiver passes through RefundSigned
	// before reaching Completed (matches legacy dual-write at
	// transfer_handler.go:3645-3655).
	if transferEnt.Status != st.TransferStatusReceiverRefundSigned &&
		transferEnt.Status != st.TransferStatusCompleted {
		if _, err := transferEnt.Update().SetStatus(st.TransferStatusReceiverRefundSigned).Save(ctx); err != nil {
			return fmt.Errorf("unable to update transfer status to RECEIVER_REFUND_SIGNED for %s: %w", transferID, err)
		}
	}
	if receiver != nil &&
		receiver.Status != st.TransferReceiverStatusRefundSigned &&
		receiver.Status != st.TransferReceiverStatusCompleted {
		if _, err := receiver.Update().SetStatus(st.TransferReceiverStatusRefundSigned).Save(ctx); err != nil {
			return fmt.Errorf("unable to update receiver status to RECEIVER_REFUND_SIGNED for %s: %w", transferID, err)
		}
	}

	// Defense-in-depth: verify the commit payload covers every receiver leaf
	// exactly once, each entry's leaf is owned by the transfer's receiver,
	// and every entry carries a non-empty refund signature. An honest
	// coordinator produces one entry per claimed leaf, but participants
	// receive the gossip payload and must not trust its completeness — a
	// short or duplicate-collapsed payload would otherwise mark
	// receiver/transfer Completed below while leaving some leaves unsigned
	// (the updateNode loop iterates the payload, not the leaf set).
	leafSignatures := req.GetLeafSignatures()
	if err := assertLeafSignaturesCover(ctx, transferEnt, receiver, leafSignatures); err != nil {
		return err
	}

	// Apply aggregated signatures to each TreeNode.
	finalizeHandler := NewFinalizeSignatureHandler(h.config)
	for _, sig := range leafSignatures {
		nodeSig := &pb.NodeSignatures{
			NodeId:                          sig.LeafId,
			NodeTxSignature:                 []byte{},
			DirectNodeTxSignature:           []byte{},
			RefundTxSignature:               sig.RefundSignature,
			DirectRefundTxSignature:         sig.DirectRefundSignature,
			DirectFromCpfpRefundTxSignature: sig.DirectFromCpfpRefundSignature,
		}
		if _, _, err := finalizeHandler.updateNode(ctx, nodeSig, pbcommon.SignatureIntent_TRANSFER, true); err != nil {
			return fmt.Errorf("failed to update node %s during commit: %w", sig.LeafId, err)
		}
	}

	// Mark Completed (MIMO: all receivers must be completed for the transfer
	// to be considered done).
	completionTime := time.Now()
	if receiver != nil {
		if _, err := receiver.Update().
			SetStatus(st.TransferReceiverStatusCompleted).
			SetCompletionTime(completionTime).
			Save(ctx); err != nil {
			return fmt.Errorf("unable to complete transfer receiver for %s: %w", transferID, err)
		}
	}
	allReceiversComplete := true
	if isMimoReceiveEnabled(ctx, receiver) {
		incomplete, err := transferEnt.QueryTransferReceivers().
			Where(enttransferreceiver.StatusNEQ(st.TransferReceiverStatusCompleted)).
			Count(ctx)
		if err != nil {
			return fmt.Errorf("unable to count incomplete receivers for transfer %s: %w", transferID, err)
		}
		allReceiversComplete = incomplete == 0
	}
	if !isMimoReceiveEnabled(ctx, receiver) || allReceiversComplete {
		if _, err := transferEnt.Update().
			SetStatus(st.TransferStatusCompleted).
			SetCompletionTime(completionTime).
			Save(ctx); err != nil {
			return fmt.Errorf("unable to complete transfer %s: %w", transferID, err)
		}
	}
	return nil
}

// rollbackClaimTransfer reverts the Phase-1 lock written by Prepare —
// clearing leaf.KeyTweak/KeyTweakProofs and restoring the transfer/receiver
// to SENDER_KEY_TWEAKED so the receiver can retry. Prepare no longer applies
// the tweak (that's deferred to Commit), so the engine's ent.Tx rollback
// alone undoes everything Prepare wrote when rollback fires before Commit.
// This handler is still invoked by the engine when a participant rejects
// Prepare or Commit, in which case revertClaimTransfer cleans up any
// committed Phase-1 state on participants whose tx already finalized.
//
// Idempotent in three directions:
//   - if Prepare never landed on this SO → NotFound, no-op
//   - if rollback already ran (status terminal) → revertClaimTransfer no-op
//   - if Commit already landed (status KeyTweakApplied+) → return
//     AlreadyExistsDuplicateOperation so the gossip rollback dispatcher
//     treats it as success and marks the FlowExecution row ROLLED_BACK
//     instead of looping the reconciler forever.
func (h *ClaimTransferFlowHandler) rollbackClaimTransfer(ctx context.Context, transferID uuid.UUID, receiverPK *keys.Public) error {
	transferEnt, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("unable to load transfer %s for rollback: %w", transferID, err)
	}

	// Prefer the claimer's identity from the rollback payload over
	// transferEnt.ReceiverIdentityPubkey — for MIMO transfers the two can
	// differ, and reverting the wrong receiver would leave the actual claimer
	// stuck. Fall back to the transfer's primary receiver only when the
	// payload omitted the field (pre-upgrade gossip or stub paths).
	loadPK := transferEnt.ReceiverIdentityPubkey
	if receiverPK != nil {
		loadPK = *receiverPK
	}
	_, receiver, err := h.loadTransferReceiverByPublicKeyForUpdate(ctx, transferEnt, &loadPK)
	if err != nil {
		return err
	}

	// Already-committed short-circuit: when the engine drives a rollback after
	// Commit landed (typically a reconciler-driven presumed-abort firing
	// against a Commit that succeeded but whose FlowExecution row update
	// failed), revertClaimTransfer would return "key tweak is already applied,
	// cannot revert it". The gossip rollback dispatcher only treats
	// codes.AlreadyExists as success, so a bare error would leave the row
	// IN_FLIGHT and the reconciler would loop. Surface it as AlreadyExists.
	if isCommittedClaim(ctx, transferEnt, receiver) {
		return sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer %s rollback: claim already committed, treating as idempotent success", transferID))
	}

	leaves, err := getTransferLeavesForReceiverQuery(ctx, transferEnt, receiver).All(ctx)
	if err != nil {
		return fmt.Errorf("unable to load transfer leaves for rollback of %s: %w", transferID, err)
	}
	if err := h.revertClaimTransfer(ctx, transferEnt, receiver, leaves); err != nil {
		return fmt.Errorf("unable to revert claim transfer %s: %w", transferID, err)
	}
	logging.GetLoggerFromContext(ctx).Sugar().Infof("claim transfer 2pc rollback: transfer %s reverted to SENDER_KEY_TWEAKED", transferID)
	return nil
}

// assertLeafSignaturesCover verifies the commit payload contains exactly one
// signature entry per receiver leaf, that each leaf id is owned by the
// transfer's receiver, and that every entry carries a non-empty refund
// signature. A partial payload would otherwise mark the receiver/transfer
// Completed (status writes downstream) while leaving uncovered leaves
// unsigned, since the updateNode loop iterates the payload not the receiver
// leaves. updateNode below would still detect a misrouted sig via FROST
// verification, but the cleaner failure is up-front membership + coverage.
func assertLeafSignaturesCover(ctx context.Context, transferEnt *ent.Transfer, receiver *ent.TransferReceiver, leafSignatures []*pbinternal.ClaimTransferLeafSignatures) error {
	if len(leafSignatures) == 0 {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("commit payload has no leaf signatures for transfer %s", transferEnt.ID))
	}
	payloadLeaves := make(map[string]struct{}, len(leafSignatures))
	for _, sig := range leafSignatures {
		if _, err := uuid.Parse(sig.LeafId); err != nil {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid leaf_id %q in commit payload: %w", sig.LeafId, err))
		}
		if _, dup := payloadLeaves[sig.LeafId]; dup {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("commit payload has duplicate signature entry for leaf %s in transfer %s", sig.LeafId, transferEnt.ID))
		}
		payloadLeaves[sig.LeafId] = struct{}{}
		if len(sig.GetRefundSignature()) == 0 {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("commit payload has empty refund signature for leaf %s in transfer %s", sig.LeafId, transferEnt.ID))
		}
	}

	transferLeaves, err := getTransferLeavesForReceiverQuery(ctx, transferEnt, receiver).WithLeaf().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to load receiver leaves for transfer %s: %w", transferEnt.ID, err)
	}
	expected := make(map[string]struct{}, len(transferLeaves))
	for _, tl := range transferLeaves {
		if tl.Edges.Leaf == nil {
			return fmt.Errorf("transfer leaf %s missing tree node edge", tl.ID)
		}
		expected[tl.Edges.Leaf.ID.String()] = struct{}{}
	}
	if len(payloadLeaves) != len(expected) {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("commit payload covers %d leaves but transfer %s receiver has %d", len(payloadLeaves), transferEnt.ID, len(expected)))
	}
	for leafID := range payloadLeaves {
		if _, ok := expected[leafID]; !ok {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("commit payload references leaf %s not owned by transfer %s receiver", leafID, transferEnt.ID))
		}
	}
	return nil
}

// isCommittedClaim reports whether the claim has already landed Phase-2
// (KeyTweakApplied or later) for the given receiver. MIMO uses the receiver's
// per-row status; non-MIMO uses the transfer-level status. Completed is
// included: it's the primary case for the Rollback caller (a finalized claim
// can't be rolled back), and in Prepare it's unreachable because
// validateClaimStatus returns AlreadyExists for Completed before this is called.
func isCommittedClaim(ctx context.Context, transferEnt *ent.Transfer, receiver *ent.TransferReceiver) bool {
	if isMimoReceiveEnabled(ctx, receiver) {
		switch receiver.Status { //nolint:exhaustive // only post-apply states gate the already-committed branch
		case st.TransferReceiverStatusKeyTweakApplied,
			st.TransferReceiverStatusRefundSigned,
			st.TransferReceiverStatusCompleted:
			return true
		}
		return false
	}
	switch transferEnt.Status { //nolint:exhaustive // only post-apply states gate the already-committed branch
	case st.TransferStatusReceiverKeyTweakApplied,
		st.TransferStatusReceiverRefundSigned,
		st.TransferStatusCompleted:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// claimTransferCoordinatorFlow — coordinator side
// ---------------------------------------------------------------------------

// claimTransferCoordinatorFlow drives the coordinator-side of a claim_transfer
// through the 2PC engine. The handler delegates Prepare/Commit/Rollback to the
// participant-side ClaimTransferFlowHandler (the engine calls Prepare on the
// coordinator too); BuildCommitPayload is where coordinator-only work lives.
type claimTransferCoordinatorFlow struct {
	*ClaimTransferFlowHandler

	req               *pb.ClaimTransferRequest
	parsed            parsedClaimTransferRequest
	signingJobsByLeaf map[string]*claimTransferLeafSigningJobs

	// response is populated during BuildCommitPayload so the public
	// ClaimTransfer handler can return it after engine.Execute completes.
	response *pb.ClaimTransferResponse
}

var _ consensus.CoordinatorFlow = (*claimTransferCoordinatorFlow)(nil)

// PrepareOp returns the prepare request sent to every SO (engine fans this out).
func (f *claimTransferCoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.ClaimTransferPrepareRequest{OriginalRequest: f.req}
}

// BuildCommitPayload aggregates FROST shares from all SOs, applies the resulting
// signatures + receiver key tweaks on the coordinator, builds the response, and
// returns the commit payload (aggregated signatures keyed by leaf) for the
// engine to gossip to participants.
func (f *claimTransferCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	allShares, _, err := collectSignatureShares(results)
	if err != nil {
		return nil, fmt.Errorf("failed to collect signature shares: %w", err)
	}

	// Phase-2 apply must run before FROST aggregation: aggregateLeafSignature
	// reads leaf.OwnerSigningPubkey as the user's signing pubkey, and the user
	// signed with their post-tweak key. Apply the tweak now so the next
	// refreshLeafOwnership sees the post-tweak owner pubkey in DB.
	//
	// applyClaimTransferCommit below will call SettleReceiverKeyTweak a
	// second time — it's idempotent on KeyTweakApplied+ status and no-ops.
	receiverPKBytes := f.parsed.ownerIDPK.Serialize()
	if err := f.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId:                f.parsed.transferID.String(),
		Action:                    pbinternal.SettleKeyTweakAction_COMMIT,
		ReceiverIdentityPublicKey: receiverPKBytes,
	}); err != nil {
		return nil, fmt.Errorf("unable to apply receiver key tweaks during coordinator commit: %w", err)
	}

	// Reload the in-memory leaves captured by buildClaimTransferCoordinatorFlow
	// (pre-tweak snapshot) so aggregateLeafSignature sees the post-tweak
	// OwnerSigningPubkey just written by the apply above.
	if err := f.refreshLeafOwnership(ctx); err != nil {
		return nil, fmt.Errorf("unable to refresh leaf ownership for aggregation: %w", err)
	}

	leafSignatures := make([]*pbinternal.ClaimTransferLeafSignatures, 0, len(f.signingJobsByLeaf))

	leafIDs := make([]string, 0, len(f.signingJobsByLeaf))
	for id := range f.signingJobsByLeaf {
		leafIDs = append(leafIDs, id)
	}
	slices.Sort(leafIDs)

	for _, leafID := range leafIDs {
		jobs := f.signingJobsByLeaf[leafID]
		sigs := &pbinternal.ClaimTransferLeafSignatures{LeafId: leafID}

		if jobs.cpfp != nil {
			sig, err := f.aggregateLeafSignature(ctx, jobs.cpfp, allShares, jobs.leaf, jobs.cpfpUserSig)
			if err != nil {
				return nil, fmt.Errorf("aggregate cpfp signature for leaf %s: %w", leafID, err)
			}
			sigs.RefundSignature = sig
		}
		if jobs.direct != nil {
			sig, err := f.aggregateLeafSignature(ctx, jobs.direct, allShares, jobs.leaf, jobs.directUserSig)
			if err != nil {
				return nil, fmt.Errorf("aggregate direct signature for leaf %s: %w", leafID, err)
			}
			sigs.DirectRefundSignature = sig
		}
		if jobs.dfc != nil {
			sig, err := f.aggregateLeafSignature(ctx, jobs.dfc, allShares, jobs.leaf, jobs.dfcUserSig)
			if err != nil {
				return nil, fmt.Errorf("aggregate direct-from-cpfp signature for leaf %s: %w", leafID, err)
			}
			sigs.DirectFromCpfpRefundSignature = sig
		}
		leafSignatures = append(leafSignatures, sigs)
	}

	commitReq := &pbinternal.ClaimTransferCommitRequest{
		TransferId:                f.parsed.transferID.String(),
		LeafSignatures:            leafSignatures,
		ReceiverIdentityPublicKey: f.parsed.ownerIDPK.Serialize(),
	}
	if err := f.applyClaimTransferCommit(ctx, commitReq); err != nil {
		return nil, fmt.Errorf("failed to apply commit on coordinator: %w", err)
	}

	// Build the response ClaimTransfer returns to the client.
	transferEnt, err := f.loadTransferForUpdate(ctx, f.parsed.transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to reload transfer %s after commit: %w", f.parsed.transferID, err)
	}
	_, receiver, err := f.loadTransferReceiverByPublicKeyForUpdate(ctx, transferEnt, &f.parsed.ownerIDPK)
	if err != nil {
		return nil, err
	}
	isMimo := isMimoReceiveEnabled(ctx, receiver)
	freshDb, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get db for marshal: %w", err)
	}
	// Reload the transfer post-Commit so the marshaled response carries the
	// final status + edges. Eager-load TransferSenders + TransferReceivers
	// unconditionally to match legacy claimTransferLegacy's marshal reload
	// — Transfer.MarshalProto / MarshalProtoForReceiver both consult these
	// edges and emit a spark_transfer_marshal_missing_edge_total metric when
	// they're absent (see warnIfParticipantEdgesMissing). The metric is on
	// track to become a hard error per the TODO in transfer_extension.go.
	freshTransferQuery := freshDb.Transfer.Query().
		Where(enttransfer.ID(transferEnt.ID)).
		WithTransferSenders().
		WithTransferReceivers()
	freshTransfer, err := freshTransferQuery.Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to reload transfer for marshal: %w", err)
	}
	var transferProto *pb.Transfer
	if isMimo {
		transferProto, err = freshTransfer.MarshalProtoForReceiver(ctx, f.parsed.ownerIDPK)
	} else {
		transferProto, err = freshTransfer.MarshalProto(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer %s for response: %w", f.parsed.transferID, err)
	}
	f.response = &pb.ClaimTransferResponse{Transfer: transferProto}

	return commitReq, nil
}

// RollbackPayload returns the minimal payload sent to participants on rollback.
// Includes receiver_identity_public_key so participants can locate the same
// receiver row the coordinator did — in MIMO transfers
// transferEnt.ReceiverIdentityPubkey isn't necessarily the claimer, and
// reverting the wrong receiver would leave the actual claimer permanently
// stuck.
func (f *claimTransferCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.ClaimTransferRollbackRequest{
		TransferId:                f.parsed.transferID.String(),
		ReceiverIdentityPublicKey: f.parsed.ownerIDPK.Serialize(),
	}
}

// aggregateLeafSignature drives a single FROST AggregateFrost RPC for one job.
func (f *claimTransferCoordinatorFlow) aggregateLeafSignature(
	ctx context.Context,
	job *helper.SigningJobWithPregeneratedNonce,
	allShares map[string]map[string][]byte,
	leaf *ent.TreeNode,
	userSignatureShare []byte,
) ([]byte, error) {
	keyPackage, err := ent.GetKeyPackage(ctx, f.config, job.SigningKeyshareID)
	if err != nil {
		return nil, fmt.Errorf("unable to get key package: %w", err)
	}
	shares, ok := allShares[job.JobID.String()]
	if !ok {
		return nil, fmt.Errorf("missing signature shares for job %s", job.JobID)
	}
	// Public shares must match the signing set per job, not the global
	// participant set. Different leaves can carry different round1 commitment
	// sets, so filter from the actual contributors to this job's shares.
	publicShares := make(map[string][]byte, len(shares))
	for id := range shares {
		share, ok := keyPackage.PublicShares[id]
		if !ok {
			return nil, fmt.Errorf("missing public share for operator %s", id)
		}
		publicShares[id] = share
	}

	conn, err := f.config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("unable to connect to frost: %w", err)
	}
	defer conn.Close()
	frostClient := pbfrost.NewFrostServiceClient(conn)

	userCommitment, err := job.UserCommitment.MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("unable to marshal user commitment: %w", err)
	}
	roundCommitments, err := marshalClaimRoundCommitments(job.Round1Packages)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal round1 commitments: %w", err)
	}
	resp, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            job.Message,
		SignatureShares:    shares,
		PublicShares:       publicShares,
		VerifyingKey:       leaf.VerifyingPubkey.Serialize(),
		Commitments:        roundCommitments,
		UserCommitments:    userCommitment,
		UserPublicKey:      leaf.OwnerSigningPubkey.Serialize(),
		UserSignatureShare: userSignatureShare,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to aggregate frost signature: %w", err)
	}
	return resp.Signature, nil
}

// buildClaimTransferCoordinatorFlow pre-computes the per-leaf signing-job
// helpers the coordinator needs during BuildCommitPayload's FROST
// aggregation. The receiver leaves are taken as a parameter (already loaded
// by claimTransferConsensus's preflight for the leaf-count parity check) so
// this function doesn't redundantly re-query them.
func buildClaimTransferCoordinatorFlow(
	ctx context.Context,
	handler *ClaimTransferFlowHandler,
	req *pb.ClaimTransferRequest,
	parsed parsedClaimTransferRequest,
	leavesByID map[string]*ent.TreeNode,
) (*claimTransferCoordinatorFlow, error) {
	jobsByLeaf, err := buildClaimTransferAggregationJobs(ctx, parsed.transferID, parsed.claimPackage, leavesByID)
	if err != nil {
		return nil, fmt.Errorf("unable to build signing-job helpers: %w", err)
	}

	return &claimTransferCoordinatorFlow{
		ClaimTransferFlowHandler: handler,
		req:                      req,
		parsed:                   parsed,
		signingJobsByLeaf:        jobsByLeaf,
	}, nil
}

// refreshLeafOwnership reloads every leaf referenced by f.signingJobsByLeaf
// from the DB and rebinds the in-memory pointer. Called from
// BuildCommitPayload AFTER its early settleReceiverKeyTweakLocal(COMMIT)
// call has applied the receiver key tweak to the coordinator's keyshare
// rows — this is the call that rewrites leaf.OwnerSigningPubkey to the
// post-tweak value. Prepare itself does NOT mutate keyshare state (the
// in-memory keyshare tweak applied in Prepare's FrostRound2 is local to
// that signing pass). The original pointers captured pre-Execute carry the
// pre-tweak owner pubkey and would cause FROST verification to fail when
// passed to AggregateFrost as UserPublicKey.
func (f *claimTransferCoordinatorFlow) refreshLeafOwnership(ctx context.Context) error {
	if len(f.signingJobsByLeaf) == 0 {
		return nil
	}
	leafIDs := make([]uuid.UUID, 0, len(f.signingJobsByLeaf))
	for id := range f.signingJobsByLeaf {
		leafUUID, err := uuid.Parse(id)
		if err != nil {
			return fmt.Errorf("invalid leaf id %s in signing jobs map: %w", id, err)
		}
		leafIDs = append(leafIDs, leafUUID)
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	fresh, err := db.TreeNode.Query().Where(enttreenode.IDIn(leafIDs...)).All(ctx)
	if err != nil {
		return fmt.Errorf("unable to reload leaves for transfer %s: %w", f.parsed.transferID, err)
	}
	if len(fresh) != len(f.signingJobsByLeaf) {
		freshIDs := make(map[string]struct{}, len(fresh))
		for _, leaf := range fresh {
			freshIDs[leaf.ID.String()] = struct{}{}
		}
		missing := make([]string, 0)
		for id := range f.signingJobsByLeaf {
			if _, ok := freshIDs[id]; !ok {
				missing = append(missing, id)
			}
		}
		return fmt.Errorf("transfer %s: reloaded %d leaves but expected %d; missing leaves: %v", f.parsed.transferID, len(fresh), len(f.signingJobsByLeaf), missing)
	}
	for _, leaf := range fresh {
		jobs, ok := f.signingJobsByLeaf[leaf.ID.String()]
		if !ok {
			return fmt.Errorf("transfer %s: reloaded unexpected leaf %s", f.parsed.transferID, leaf.ID)
		}
		jobs.leaf = leaf
	}
	return nil
}

// ---------------------------------------------------------------------------
// Parsing + validation helpers
// ---------------------------------------------------------------------------

type parsedClaimTransferRequest struct {
	transferID   uuid.UUID
	ownerIDPK    keys.Public
	claimPackage *pb.ClaimPackage
}

// parseClaimTransferRequest extracts and validates the structural fields shared
// by every call site (Prepare on each SO, buildClaimTransferCoordinatorFlow).
func parseClaimTransferRequest(req *pb.ClaimTransferRequest) (parsedClaimTransferRequest, error) {
	var empty parsedClaimTransferRequest
	if req == nil {
		return empty, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return empty, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer id: %w", err))
	}
	ownerIDPK, err := keys.ParsePublicKey(req.OwnerIdentityPublicKey)
	if err != nil {
		return empty, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid owner identity public key: %w", err))
	}
	if req.ClaimPackage == nil {
		return empty, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("claim_package is required"))
	}
	return parsedClaimTransferRequest{
		transferID:   transferID,
		ownerIDPK:    ownerIDPK,
		claimPackage: req.ClaimPackage,
	}, nil
}

// loadClaimContext loads the transfer + receiver under FOR UPDATE NoWait
// locks. NoWait on the transfer row prevents a distributed deadlock when two
// claims for the same transfer race through different coordinators: each
// coordinator's preflight holds its own DB's transfer row NoWait, then
// engine.Execute fans Prepare out to the other; without NoWait here, both
// participants block waiting for the other's coordinator preflight to
// release, deadlocking until the engine RPC times out. With NoWait, the
// loser fails fast with AbortedConcurrentClaimConflict, the engine surfaces
// it as a Prepare failure, and the cluster rolls back cleanly.
//
// The coordinator's own self-Prepare runs in the same request transaction
// as its preflight, so re-acquiring the lock here is a no-op (Postgres
// reentrant-lock semantics within the same tx).
func (h *ClaimTransferFlowHandler) loadClaimContext(ctx context.Context, parsed parsedClaimTransferRequest) (*ent.Transfer, *ent.TransferReceiver, error) {
	transferEnt, err := h.loadTransferForUpdate(ctx, parsed.transferID, sql.WithLockAction(sql.NoWait))
	if err != nil {
		if sparkdb.IsLockNotAvailableError(err) {
			return nil, nil, sparkerrors.AbortedConcurrentClaimConflict(fmt.Errorf("unable to load transfer %s: %w", parsed.transferID, err))
		}
		return nil, nil, fmt.Errorf("unable to load transfer %s: %w", parsed.transferID, err)
	}
	_, receiver, err := h.loadTransferReceiverByPublicKeyForUpdate(ctx, transferEnt, &parsed.ownerIDPK)
	if err != nil {
		return nil, nil, err
	}
	return transferEnt, receiver, nil
}

// validateClaimStatus enforces the same status preconditions as the legacy
// ClaimTransfer entry point. Branches on isMimoReceiveEnabled (not just
// receiver != nil) to match the legacy semantics — when the MIMO knob is off
// but a TransferReceiver row happens to exist for the requested pubkey (loader
// returns (false, receiver, nil)), legacy checks transfer.Status, not
// receiver.Status.
//
// The already-applied states (KeyTweakApplied / RefundSigned) are claimable, not
// terminal: a prior attempt — or, during the KnobUseConsensusClaim cutover, a
// legacy claim that committed the settle phase before refund signing — can leave
// a durable RKA/RRS transfer that still needs its refunds signed and a finalize
// to reach COMPLETED. The consensus Prepare path resumes those (see Prepare's
// applied branch): the key is tweaked exactly once, so it signs against the
// already-post-tweak owner and Commit's apply no-ops. Returning AlreadyExists
// here instead would strand such partials — their refunds would never get
// signed and the SDK would stop retrying.
func validateClaimStatus(transferID uuid.UUID, transferEnt *ent.Transfer, receiver *ent.TransferReceiver, isMimo bool) error {
	if isMimo && receiver != nil {
		switch receiver.Status {
		case st.TransferReceiverStatusReceiverClaimPending,
			st.TransferReceiverStatusKeyTweaked,
			st.TransferReceiverStatusKeyTweakLocked,
			st.TransferReceiverStatusKeyTweakApplied,
			st.TransferReceiverStatusRefundSigned:
			return nil
		case st.TransferReceiverStatusCompleted:
			return sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer %s has already been claimed by this receiver", transferID))
		default:
			return fmt.Errorf("transfer %s receiver is not in a claimable status, current status: %s", transferID, receiver.Status)
		}
	}
	switch transferEnt.Status {
	case st.TransferStatusSenderKeyTweaked,
		st.TransferStatusReceiverKeyTweaked,
		st.TransferStatusReceiverKeyTweakLocked,
		st.TransferStatusReceiverKeyTweakApplied,
		st.TransferStatusReceiverRefundSigned:
		return nil
	case st.TransferStatusCompleted:
		return sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer %s has already been claimed", transferID))
	case st.TransferStatusExpired, st.TransferStatusReturned:
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("transfer %s is in terminal state %s and cannot be claimed", transferID, transferEnt.Status))
	default:
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("transfer %s is not in a claimable status, current status: %s", transferID, transferEnt.Status))
	}
}

// validateClaimPackageStructure enforces the structural invariants the legacy
// ClaimTransfer entry point asserts.
func validateClaimPackageStructure(pkg *pb.ClaimPackage) error {
	if len(pkg.LeavesToClaim) == 0 {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("claim package leaves_to_claim is required and must be non-empty"))
	}
	if len(pkg.KeyTweakPackage) == 0 {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("claim package key_tweak_package is required and must be non-empty"))
	}
	dfcLeafIDs := make(map[string]struct{}, len(pkg.DirectFromCpfpLeavesToClaim))
	for _, job := range pkg.DirectFromCpfpLeavesToClaim {
		dfcLeafIDs[job.LeafId] = struct{}{}
	}
	for _, job := range pkg.LeavesToClaim {
		if _, ok := dfcLeafIDs[job.LeafId]; !ok {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("missing direct from CPFP refund transaction for leaf %s", job.LeafId))
		}
	}
	return nil
}

// validateClaimLeafKeyTweak runs the structural + cryptographic checks every
// SO must perform on its decrypted slice of a claim key tweak. Critically
// this also runs on non-signing SOs during Prepare — claimLeafTweakKey
// (Phase-2 apply, Commit-time) is the only other place ValidateShare /
// ValidatePubkeySharesTweak run, and a non-signing SO never reaches it until
// commit gossip arrives. Without these up-front checks a malformed tweak
// would pass Prepare on a non-signer, the coordinator would proceed to
// commit, and the participant would fail permanently in Commit with an
// "invalid secret share" or "inconsistent pubkey_shares_tweak" error that
// the engine can't recover from cleanly.
func validateClaimLeafKeyTweak(config *so.Config, leafTweak *pb.ClaimLeafKeyTweak) error {
	if leafTweak.SecretShareTweak == nil {
		return fmt.Errorf("missing secret share tweak for leaf %s", leafTweak.LeafId)
	}
	if len(leafTweak.SecretShareTweak.SecretShare) == 0 {
		return fmt.Errorf("empty secret share for leaf %s", leafTweak.LeafId)
	}
	if uint64(len(leafTweak.SecretShareTweak.Proofs)) != config.Threshold {
		return fmt.Errorf("expected %d proofs for leaf %s, got %d", config.Threshold, leafTweak.LeafId, len(leafTweak.SecretShareTweak.Proofs))
	}
	if err := secretsharing.ValidateShare(
		&secretsharing.VerifiableSecretShare{
			SecretShare: secretsharing.SecretShare{
				FieldModulus: secp256k1.S256().N,
				Threshold:    int(config.Threshold),
				Index:        big.NewInt(int64(config.Index + 1)),
				Share:        new(big.Int).SetBytes(leafTweak.SecretShareTweak.SecretShare),
			},
			Proofs: leafTweak.SecretShareTweak.Proofs,
		},
	); err != nil {
		return fmt.Errorf("invalid secret share tweak for leaf %s: %w", leafTweak.LeafId, err)
	}
	pubKeySharesTweak, err := keys.ParsePublicKeyMap(leafTweak.PubkeySharesTweak)
	if err != nil {
		return fmt.Errorf("unable to parse pubkey_shares_tweak for leaf %s: %w", leafTweak.LeafId, err)
	}
	if err := helper.ValidatePubkeySharesTweak(config, leafTweak.SecretShareTweak.Proofs, pubKeySharesTweak); err != nil {
		return fmt.Errorf("invalid pubkey_shares_tweak for leaf %s: %w", leafTweak.LeafId, err)
	}
	return nil
}

// decryptClaimKeyTweaks decrypts this SO's slice of the claim package's key
// tweaks and returns the full ClaimLeafKeyTweak (secret share + proofs +
// per-operator public share tweaks) keyed by leaf ID. Consensus Prepare uses
// the full payload to (a) apply the tweak in-memory during FROST signing and
// (b) compute the predicted post-tweak owner pubkey for refund-tx validation,
// without mutating the persisted keyshare or tree node — the durable apply
// runs in Commit.
func decryptClaimKeyTweaks(config *so.Config, pkg *pb.ClaimPackage) (map[string]*pb.ClaimLeafKeyTweak, error) {
	if config.Threshold < 1 {
		return nil, fmt.Errorf("invalid SO config: threshold must be >= 1, got %d", config.Threshold)
	}
	coordinatorCipher := pkg.KeyTweakPackage[config.Identifier]
	if len(coordinatorCipher) == 0 {
		return nil, fmt.Errorf("no encrypted claim key tweaks found for SO %s", config.Identifier)
	}
	decryptionPrivateKey := eciesgo.NewPrivateKeyFromBytes(config.IdentityPrivateKey.Serialize())
	decrypted, err := eciesgo.Decrypt(decryptionPrivateKey, coordinatorCipher)
	if err != nil {
		return nil, fmt.Errorf("unable to decrypt coordinator claim key tweaks: %w", err)
	}
	claimKeyTweaks := &pb.ClaimLeafKeyTweaks{}
	if err := proto.Unmarshal(decrypted, claimKeyTweaks); err != nil {
		return nil, fmt.Errorf("unable to unmarshal coordinator claim key tweaks: %w", err)
	}
	out := make(map[string]*pb.ClaimLeafKeyTweak, len(claimKeyTweaks.LeavesToReceive))
	for _, leafTweak := range claimKeyTweaks.LeavesToReceive {
		if err := validateClaimLeafKeyTweak(config, leafTweak); err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(err)
		}
		out[leafTweak.LeafId] = leafTweak
	}
	return out, nil
}

// decryptStoredClaimKeyTweaks reads the per-SO ClaimLeafKeyTweak previously
// persisted on transfer_leaf.KeyTweak by Phase-1 settle. Used by the
// useStoredKeyTweaks retry path where the fresh claim package's ciphertext
// would not match what's already locked on disk.
func decryptStoredClaimKeyTweaks(config *so.Config, transferLeaves []*ent.TransferLeaf) (map[string]*pb.ClaimLeafKeyTweak, error) {
	if config.Threshold < 1 {
		return nil, fmt.Errorf("invalid SO config: threshold must be >= 1, got %d", config.Threshold)
	}
	out := make(map[string]*pb.ClaimLeafKeyTweak, len(transferLeaves))
	for _, leaf := range transferLeaves {
		if leaf.Edges.Leaf == nil {
			return nil, fmt.Errorf("transfer leaf %s missing tree node edge", leaf.ID)
		}
		treeNodeID := leaf.Edges.Leaf.ID.String()
		if len(leaf.KeyTweak) == 0 {
			return nil, fmt.Errorf("transfer leaf %s has no stored key tweak", leaf.ID)
		}
		leafKeyTweak := &pb.ClaimLeafKeyTweak{}
		if err := proto.Unmarshal(leaf.KeyTweak, leafKeyTweak); err != nil {
			return nil, fmt.Errorf("unable to unmarshal stored key tweak for leaf %s: %w", treeNodeID, err)
		}
		// LeafId on the stored proto echoes the original tree-node ID; tolerate
		// a missing field by populating from the row before validation reports
		// "for leaf <empty>".
		if leafKeyTweak.LeafId == "" {
			leafKeyTweak.LeafId = treeNodeID
		}
		if err := validateClaimLeafKeyTweak(config, leafKeyTweak); err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(err)
		}
		out[treeNodeID] = leafKeyTweak
	}
	return out, nil
}

// loadClaimReceiverLeaves loads the receiver's TransferLeaf rows along with
// their associated TreeNode (keyed by tree-node ID). Returns both so callers
// can peek at TransferLeaf.KeyTweak (stored encrypted-then-decrypted bytes)
// for the useStoredKeyTweaks retry path while still getting the TreeNode map
// downstream callers need.
func loadClaimReceiverLeaves(ctx context.Context, transferEnt *ent.Transfer, receiver *ent.TransferReceiver) ([]*ent.TransferLeaf, map[string]*ent.TreeNode, error) {
	transferLeaves, err := getTransferLeavesForReceiverQuery(ctx, transferEnt, receiver).WithLeaf(func(tnq *ent.TreeNodeQuery) {
		tnq.WithTree().WithSigningKeyshare()
	}).All(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to load transfer leaves: %w", err)
	}
	leavesByID := make(map[string]*ent.TreeNode, len(transferLeaves))
	for _, leaf := range transferLeaves {
		if leaf.Edges.Leaf == nil {
			return nil, nil, fmt.Errorf("transfer leaf %s missing tree node edge", leaf.ID)
		}
		leavesByID[leaf.Edges.Leaf.ID.String()] = leaf.Edges.Leaf
	}
	if len(leavesByID) == 0 {
		return nil, nil, fmt.Errorf("transfer %s has no leaves to claim", transferEnt.ID)
	}
	return transferLeaves, leavesByID, nil
}

// shouldUseStoredKeyTweaks reports whether this SO should reuse the receiver
// key tweaks already stored on transfer_leaf rows (from a prior Phase-1
// commit) instead of decrypting a fresh claim package. Triggered once the
// transfer/receiver status has advanced past the Phase-1 lock; before that
// there's nothing on disk to reuse.
func shouldUseStoredKeyTweaks(ctx context.Context, transferEnt *ent.Transfer, receiver *ent.TransferReceiver) bool {
	// Statuses where leaf.KeyTweak is durably anchored and a retry must
	// reuse it instead of installing a fresh polynomial:
	//
	//   - ReceiverKeyTweaked (RKT): legacy persistCoordinatorClaimKeyTweak
	//     committed the coordinator's slice in its own tx before the 2PC ran.
	//     If a transfer was started under the legacy path and got stranded
	//     at RKT, a retry via the consensus path must reuse the anchored
	//     proofs — a fresh claim package would mismatch the stored tweak
	//     in InitiateSettleReceiverKeyTweak's already-locked validation,
	//     and the next user retry would keep failing.
	//
	//   - ReceiverKeyTweakLocked (RKL): Phase 1 lock committed but Phase 2
	//     hasn't fired. Identical reasoning — the cluster's polynomial is
	//     anchored across peers; a fresh polynomial would diverge.
	//
	// Post-apply statuses (KeyTweakApplied / RefundSigned) are deliberately
	// excluded: Phase-2 settleReceiverKeyTweakLocal clears leaf.KeyTweak,
	// so decryptStoredClaimKeyTweaks would fail with "no stored key tweak".
	// Reaching this function at RKA/RRS isn't expected in the consensus
	// flow (Phase 1 + Phase 2 + Completed run inside one engine request
	// tx); if it happens, fall through to the fresh-package path —
	// settleReceiverKeyTweakLocal short-circuits on already-applied status.
	if isMimoReceiveEnabled(ctx, receiver) {
		switch receiver.Status { //nolint:exhaustive // only the pre-apply locked statuses gate the stored-tweak path; everything else falls through
		case st.TransferReceiverStatusKeyTweaked,
			st.TransferReceiverStatusKeyTweakLocked:
			return true
		}
		return false
	}
	switch transferEnt.Status { //nolint:exhaustive // only the pre-apply locked statuses gate the stored-tweak path; everything else falls through
	case st.TransferStatusReceiverKeyTweaked,
		st.TransferStatusReceiverKeyTweakLocked:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Deterministic signing-job construction
// ---------------------------------------------------------------------------

// claimTransferSigningJobNamespace is a fixed UUIDv4 mixed into NewSHA1 to
// produce deterministic per-leaf-per-tx-variant job IDs that don't collide
// with other 2PC flows.
var claimTransferSigningJobNamespace = uuid.MustParse("5e2f4a1d-6c3b-4e8f-9a7d-2b5c8e1f3a6d")

// Tx-variant tags mixed into deterministic job IDs. Constants so the producer
// (claimTransferJobID call sites in buildClaimTransferAggregationJobs) and the
// consumer (marshalClaimSigningJobsForFrost) can't drift on a typo — a
// mismatch silently produces wrong job IDs and surfaces only at FROST
// aggregation as "missing signature shares".
const (
	claimTxKindCPFP           = "cpfp"
	claimTxKindDirect         = "direct"
	claimTxKindDirectFromCPFP = "directFromCpfp"
)

// claimTransferJobID returns a deterministic UUID identifying the FROST signing
// job for (transferID, leafID, txKind). Valid txKind values are the
// claimTxKind* constants above.
func claimTransferJobID(transferID uuid.UUID, leafID string, txKind string) uuid.UUID {
	return uuid.NewSHA1(claimTransferSigningJobNamespace, fmt.Appendf(nil, "%s:%s:%s", transferID.String(), leafID, txKind))
}

// claimTransferLeafSigningJobs holds the pre-built signing-job helpers for one
// leaf's three refund tx variants, plus the user's signature shares.
type claimTransferLeafSigningJobs struct {
	leaf          *ent.TreeNode
	cpfp          *helper.SigningJobWithPregeneratedNonce
	cpfpUserSig   []byte
	direct        *helper.SigningJobWithPregeneratedNonce
	directUserSig []byte
	dfc           *helper.SigningJobWithPregeneratedNonce
	dfcUserSig    []byte
}

// buildClaimTransferAggregationJobs constructs the per-leaf signing-job helpers
// the coordinator uses for FROST aggregation. Deterministic job IDs mirror the
// ones SOs generate locally in Prepare.
func buildClaimTransferAggregationJobs(
	ctx context.Context,
	transferID uuid.UUID,
	pkg *pb.ClaimPackage,
	leafMap map[string]*ent.TreeNode,
) (map[string]*claimTransferLeafSigningJobs, error) {
	// Seed only the leaves that appear in the claim package's cpfp list — every
	// claimed leaf is required to have a cpfp job, and Prepare's leaf-count
	// guard (len(leavesByID) == len(LeavesToClaim)) enforces 1:1 with the
	// receiver's transfer leaves. Seeding from leafMap directly would leak
	// nil-job entries for any DB leaf the package didn't cover, which then
	// flow into BuildCommitPayload's loop as empty signature records and into
	// updateNode as all-nil sigs.
	out := make(map[string]*claimTransferLeafSigningJobs, len(pkg.GetLeavesToClaim()))
	for _, req := range pkg.GetLeavesToClaim() {
		leaf, ok := leafMap[req.LeafId]
		if !ok {
			return nil, fmt.Errorf("cpfp leaf %s not found in leaf map", req.LeafId)
		}
		job, err := buildClaimRefundSigningJob(ctx, req, leaf, leaf.RawTx, claimTransferJobID(transferID, leaf.ID.String(), claimTxKindCPFP))
		if err != nil {
			return nil, fmt.Errorf("build cpfp signing job for leaf %s: %w", req.LeafId, err)
		}
		out[req.LeafId] = &claimTransferLeafSigningJobs{leaf: leaf, cpfp: job, cpfpUserSig: req.UserSignature}
	}
	for _, req := range pkg.GetDirectLeavesToClaim() {
		entry, ok := out[req.LeafId]
		if !ok {
			return nil, fmt.Errorf("direct leaf %s missing cpfp entry in claim package", req.LeafId)
		}
		job, err := buildClaimRefundSigningJob(ctx, req, entry.leaf, entry.leaf.DirectTx, claimTransferJobID(transferID, entry.leaf.ID.String(), claimTxKindDirect))
		if err != nil {
			return nil, fmt.Errorf("build direct signing job for leaf %s: %w", req.LeafId, err)
		}
		entry.direct = job
		entry.directUserSig = req.UserSignature
	}
	for _, req := range pkg.GetDirectFromCpfpLeavesToClaim() {
		entry, ok := out[req.LeafId]
		if !ok {
			return nil, fmt.Errorf("direct-from-cpfp leaf %s missing cpfp entry in claim package", req.LeafId)
		}
		job, err := buildClaimRefundSigningJob(ctx, req, entry.leaf, entry.leaf.RawTx, claimTransferJobID(transferID, entry.leaf.ID.String(), claimTxKindDirectFromCPFP))
		if err != nil {
			return nil, fmt.Errorf("build direct-from-cpfp signing job for leaf %s: %w", req.LeafId, err)
		}
		entry.dfc = job
		entry.dfcUserSig = req.UserSignature
	}
	return out, nil
}

// buildClaimRefundSigningJob builds a single FROST signing-job helper for one
// refund variant. parentTxBytes is the tx whose vout 0 is being spent.
// Mirrors the send-transfer flow's buildSigningJobForRefund but kept local to
// avoid coupling claim_transfer PRs to send-transfer's unmerged stack.
func buildClaimRefundSigningJob(
	ctx context.Context,
	req *pb.UserSignedTxSigningJob,
	leaf *ent.TreeNode,
	parentTxBytes []byte,
	jobID uuid.UUID,
) (*helper.SigningJobWithPregeneratedNonce, error) {
	refundTx, err := common.TxFromRawTxBytes(req.GetRawTx())
	if err != nil {
		return nil, fmt.Errorf("unable to parse refund tx: %w", err)
	}
	parentTx, err := common.TxFromRawTxBytes(parentTxBytes)
	if err != nil {
		return nil, fmt.Errorf("unable to parse parent tx: %w", err)
	}
	if len(parentTx.TxOut) == 0 {
		return nil, fmt.Errorf("parent tx has no outputs")
	}
	sigHash, err := common.SigHashFromTx(refundTx, 0, parentTx.TxOut[0])
	if err != nil {
		return nil, fmt.Errorf("compute sighash: %w", err)
	}

	userCommitment := frost.SigningCommitment{}
	if err := userCommitment.UnmarshalProto(req.GetSigningNonceCommitment()); err != nil {
		return nil, fmt.Errorf("unmarshal user nonce commitment: %w", err)
	}

	round1 := make(map[string]frost.SigningCommitment)
	signingCommitments := req.GetSigningCommitments()
	if signingCommitments == nil {
		return nil, fmt.Errorf("missing signing_commitments")
	}
	for opID, commitment := range signingCommitments.GetSigningCommitments() {
		c := frost.SigningCommitment{}
		if err := c.UnmarshalProto(commitment); err != nil {
			return nil, fmt.Errorf("unmarshal round1 commitment for %s: %w", opID, err)
		}
		if c.IsZero() {
			return nil, fmt.Errorf("round1 commitment for %s is zero", opID)
		}
		round1[opID] = c
	}

	// loadClaimReceiverLeaves eager-loads the keyshare via .WithSigningKeyshare;
	// fall back to a query only if the caller didn't populate the edge.
	signingKeyshare := leaf.Edges.SigningKeyshare
	if signingKeyshare == nil {
		ks, err := leaf.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to load signing keyshare for leaf %s: %w", leaf.ID, err)
		}
		signingKeyshare = ks
	}
	return &helper.SigningJobWithPregeneratedNonce{
		SigningJob: helper.SigningJob{
			JobID:             jobID,
			SigningKeyshareID: signingKeyshare.ID,
			Message:           sigHash,
			VerifyingKey:      new(leaf.VerifyingPubkey),
			UserCommitment:    &userCommitment,
		},
		Round1Packages: round1,
	}, nil
}

// marshalClaimSigningJobsForFrost converts the helper.SigningJobWithPregeneratedNonce
// list produced by prepareClaimRefundSigningJobs into the *pbinternal.SigningJob
// list each SO feeds into its local FrostRound2 handler during Prepare.
//
// prepareClaimRefundSigningJobs uses uuid.New() job IDs, which differ on every
// SO. Under the engine flow we need deterministic IDs so the coordinator can
// correlate shares without sending the ID over the wire. Rewrite each job's
// ID via the (transferID, leafID, txKind) tuple before marshaling.
func marshalClaimSigningJobsForFrost(transferID uuid.UUID, result *claimRefundSigningJobsResult) ([]*pbinternal.SigningJob, error) {
	jobs := make([]*pbinternal.SigningJob, 0, len(result.signingJobs))
	for _, job := range result.signingJobs {
		leaf, ok := result.leafJobMap[job.JobID]
		if !ok {
			return nil, fmt.Errorf("signing job %s missing leaf mapping", job.JobID)
		}
		var txKind string
		switch {
		case result.jobIsDirectRefund[job.JobID]:
			txKind = claimTxKindDirect
		case result.jobIsDirectFromCpfpRefund[job.JobID]:
			txKind = claimTxKindDirectFromCPFP
		default:
			txKind = claimTxKindCPFP
		}
		job.JobID = claimTransferJobID(transferID, leaf.ID.String(), txKind)
		marshalled, err := marshalSigningJobHelper(job)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, marshalled)
	}
	return jobs, nil
}

// filterClaimJobsForThisOperator drops jobs whose round1 commitments don't
// include this SO's identifier. Threshold signing only requires t-of-n SOs to
// participate; the rest skip local FROST round-2 and contribute nil to the
// engine's collected results.
//
// Local copy of send-transfer's filterJobsForThisOperator to keep PR2
// independent of the unmerged send-transfer stack. When send-transfer lands
// this can be deduped to a shared helper.
func filterClaimJobsForThisOperator(jobs []*pbinternal.SigningJob, identifier string) []*pbinternal.SigningJob {
	filtered := make([]*pbinternal.SigningJob, 0, len(jobs))
	for _, job := range jobs {
		if _, ok := job.Commitments[identifier]; ok {
			filtered = append(filtered, job)
		}
	}
	return filtered
}

// marshalClaimRoundCommitments converts the in-memory round1 commitments map to
// the AggregateFrost RPC's proto shape. Errors are returned rather than
// silently dropped — a missing entry would produce a malformed Commitments
// map and a cryptic FROST-side failure later.
//
// Local copy of send-transfer's marshalRoundCommitments; see comment on
// filterClaimJobsForThisOperator.
func marshalClaimRoundCommitments(round1 map[string]frost.SigningCommitment) (map[string]*pbcommon.SigningCommitment, error) {
	out := make(map[string]*pbcommon.SigningCommitment, len(round1))
	for id, c := range round1 {
		m, err := c.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("marshal round1 commitment for %s: %w", id, err)
		}
		out[id] = m
	}
	return out, nil
}
