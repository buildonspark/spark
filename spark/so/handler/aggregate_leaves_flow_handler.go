package handler

import (
	"bytes"
	"context"
	"fmt"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/sighash"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/handler/signing_handler"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ---------------------------------------------------------------------------
// AggregateLeavesFlowHandler — participant side (Prepare / Commit / Rollback)
// ---------------------------------------------------------------------------

// AggregateLeavesFlowHandler implements consensus.FlowHandler for
// CONSENSUS_OPERATION_TYPE_AGGREGATE_LEAVES. The flow retires every node
// strictly below a target node (the LCA of the leaves through one-child renew
// chains) and gives the target a two-transaction exit package spending the
// target's defining outpoint, co-signed under the aggregated key
// sum(leaf keys) == target verifying key.
//
// Signing follows the renew-leaf shape: the user fetches SO round-1
// commitments up front and carries them in the signing jobs, every SO
// validates independently and produces round-2 shares in Prepare (the
// engine's fan-out IS the round-2 trip), and the coordinator aggregates the
// final signatures — including the user's share — in BuildCommitPayload. The
// SO-side signing key is the in-memory sum of the leaves' keyshares; the
// target's keyshare row is only rotated to that sum at Commit.
type AggregateLeavesFlowHandler struct {
	config *so.Config
}

var (
	_ consensus.FlowHandler             = (*AggregateLeavesFlowHandler)(nil)
	_ consensus.PrepareBoundFlowHandler = (*AggregateLeavesFlowHandler)(nil)
)

func NewAggregateLeavesFlowHandler(config *so.Config) *AggregateLeavesFlowHandler {
	return &AggregateLeavesFlowHandler{config: config}
}

// aggregateLeavesJobNamespace is a fixed UUID v5 namespace for deterministic
// signing-job IDs, so every SO and the coordinator correlate round-2 shares
// without sending job IDs over the wire.
var aggregateLeavesJobNamespace = uuid.MustParse("3f8a2b1c-9d4e-4f6a-8b0c-5e7d1a9c2f4b")

const (
	aggregateLeavesRefundSlot     = "refund"
	aggregateLeavesWatchtowerSlot = "watchtower"

	// maxAggregateLeavesLeafCount bounds the leaves one call may aggregate.
	maxAggregateLeavesLeafCount = 256
)

func aggregateLeavesJobID(targetID uuid.UUID, slot string) uuid.UUID {
	return uuid.NewSHA1(aggregateLeavesJobNamespace, fmt.Appendf(nil, "%s-%s", targetID, slot))
}

// aggregateLeavesSubtree is the validated shape of the subtree being
// aggregated: the branching target, the one-child (renew-chain) intermediates
// below it, and the leaves in request order.
type aggregateLeavesSubtree struct {
	target        *ent.TreeNode
	intermediates []*ent.TreeNode
	leaves        []*ent.TreeNode
}

func (s *aggregateLeavesSubtree) descendants() []*ent.TreeNode {
	out := make([]*ent.TreeNode, 0, len(s.intermediates)+len(s.leaves))
	out = append(out, s.intermediates...)
	return append(out, s.leaves...)
}

// loadAggregateLeavesSubtree loads the target and every node below it and
// validates the v1 shape: the target branches into two or more child chains,
// each chain passes only one-child intermediate nodes, and each chain ends in
// exactly one node from leafIDs — so leafIDs is provably the complete leaf
// set of the target's subtree. The returned leaves are ordered as in leafIDs.
func loadAggregateLeavesSubtree(ctx context.Context, targetID uuid.UUID, leafIDs []uuid.UUID, forUpdate bool) (*aggregateLeavesSubtree, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	load := func(pred func(*ent.TreeNodeQuery) *ent.TreeNodeQuery) ([]*ent.TreeNode, error) {
		q := pred(db.TreeNode.Query())
		if forUpdate {
			q = q.ForUpdate()
		}
		return q.All(ctx)
	}

	targets, err := load(func(q *ent.TreeNodeQuery) *ent.TreeNodeQuery {
		return q.Where(enttreenode.ID(targetID))
	})
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to query target node %s: %w", targetID, err))
	}
	if len(targets) == 0 {
		return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("target node %s not found", targetID))
	}
	target := targets[0]

	leafIndexByID := make(map[uuid.UUID]int, len(leafIDs))
	for i, id := range leafIDs {
		if _, ok := leafIndexByID[id]; ok {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("duplicate leaf id %s", id))
		}
		leafIndexByID[id] = i
	}

	subtree := &aggregateLeavesSubtree{target: target, leaves: make([]*ent.TreeNode, len(leafIDs))}
	frontier := []*ent.TreeNode{target}
	sawLeaves := 0
	for len(frontier) > 0 {
		node := frontier[0]
		frontier = frontier[1:]
		children, err := load(func(q *ent.TreeNodeQuery) *ent.TreeNodeQuery {
			return q.Where(enttreenode.HasParentWith(enttreenode.ID(node.ID)))
		})
		if err != nil {
			return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to query children of node %s: %w", node.ID, err))
		}
		switch {
		case node.ID == target.ID:
			if len(children) < 2 {
				return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("target node %s has %d children; the aggregation target must be a branching node", node.ID, len(children)))
			}
		case len(children) == 0:
			idx, ok := leafIndexByID[node.ID]
			if !ok {
				return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("node %s is a leaf of the target's subtree but is not in the request; leaf_ids must be the complete leaf set", node.ID))
			}
			subtree.leaves[idx] = node
			sawLeaves++
			continue
		default:
			// A requested leaf keeps children when it already absorbed a
			// subtree, across all three states of this flow: CONSOLIDATED
			// before it, AGGREGATE_LOCK during it, AGGREGATED after it.
			// AGGREGATED is what keeps a retry resolvable — without it, a
			// recursive aggregation can never reach the idempotency
			// short-circuits, and a redelivered gossip commit fails here and is
			// retried forever.
			//
			// Any other with-children node must be descended through, or the
			// walk could truncate at an internal node, retiring it while its
			// live descendants stay independently spendable. This only decides
			// resolvability; Prepare still pins leaf status to AVAILABLE or
			// CONSOLIDATED before anything is signed, so an AGGREGATED leaf is
			// rejected there rather than aggregated twice.
			if idx, ok := leafIndexByID[node.ID]; ok {
				if node.Status != st.TreeNodeStatusConsolidated &&
					node.Status != st.TreeNodeStatusAggregateLock &&
					node.Status != st.TreeNodeStatusAggregated {
					return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("node %s has %d children and status %s; only a %s node may be aggregated as a leaf", node.ID, len(children), node.Status, st.TreeNodeStatusConsolidated))
				}
				subtree.leaves[idx] = node
				sawLeaves++
				continue
			}
			if len(children) != 1 {
				return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("node %s below target %s has %d children; only one branching level can be aggregated per call", node.ID, target.ID, len(children)))
			}
			subtree.intermediates = append(subtree.intermediates, node)
		}
		frontier = append(frontier, children...)
	}
	// Every requested leaf must have been reached by the walk. Duplicates are
	// already rejected above, so a count mismatch means some slot went unfilled;
	// the loop names it. The trailing error is a fail-safe: it keeps a future
	// change to the counting from turning a detected mismatch into a silent
	// success.
	if sawLeaves != len(leafIDs) {
		for id, idx := range leafIndexByID {
			if subtree.leaves[idx] == nil {
				return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("leaf %s is not part of target %s's subtree", id, target.ID))
			}
		}
		return nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("aggregate leaves walk matched %d of %d requested leaves for target %s", sawLeaves, len(leafIDs), target.ID))
	}
	return subtree, nil
}

// validateAggregateLeavesSubtree runs the status, ownership, and key-sum
// checks every SO must agree on before signing.
func validateAggregateLeavesSubtree(ctx context.Context, subtree *aggregateLeavesSubtree, ownerIdentityPubKey keys.Public) (aggregatedUserKey keys.Public, err error) {
	if subtree.target.Status != st.TreeNodeStatusSplitted {
		return keys.Public{}, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("target node %s status is %s, expected %s", subtree.target.ID, subtree.target.Status, st.TreeNodeStatusSplitted))
	}
	for _, node := range subtree.intermediates {
		if node.Status != st.TreeNodeStatusSplitted && node.Status != st.TreeNodeStatusSplitLocked {
			return keys.Public{}, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("intermediate node %s status is %s, expected %s or %s", node.ID, node.Status, st.TreeNodeStatusSplitted, st.TreeNodeStatusSplitLocked))
		}
	}

	verifyingSum := keys.Public{}
	userKeySum := keys.Public{}
	keyshareSum := keys.Public{}
	for i, leaf := range subtree.leaves {
		if leaf.Status != st.TreeNodeStatusAvailable && leaf.Status != st.TreeNodeStatusConsolidated {
			return keys.Public{}, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("leaf %s status is %s, expected %s or %s", leaf.ID, leaf.Status, st.TreeNodeStatusAvailable, st.TreeNodeStatusConsolidated))
		}
		if !leaf.OwnerIdentityPubkey.Equals(ownerIdentityPubKey) {
			return keys.Public{}, sparkerrors.PermissionDeniedNoReadAccess(fmt.Errorf("leaf %s is not owned by the initiator", leaf.ID))
		}
		if len(leaf.RawRefundTx) == 0 {
			return keys.Public{}, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("leaf %s has no refund tx", leaf.ID))
		}
		keyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return keys.Public{}, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get signing keyshare for leaf %s: %w", leaf.ID, err))
		}
		if i == 0 {
			verifyingSum, userKeySum, keyshareSum = leaf.VerifyingPubkey, leaf.OwnerSigningPubkey, keyshare.PublicKey
			continue
		}
		verifyingSum = verifyingSum.Add(leaf.VerifyingPubkey)
		userKeySum = userKeySum.Add(leaf.OwnerSigningPubkey)
		keyshareSum = keyshareSum.Add(keyshare.PublicKey)
	}

	if !verifyingSum.Equals(subtree.target.VerifyingPubkey) {
		return keys.Public{}, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("sum of leaf verifying keys does not equal target %s verifying key", subtree.target.ID))
	}
	if !userKeySum.Add(keyshareSum).Equals(subtree.target.VerifyingPubkey) {
		return keys.Public{}, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("sum of leaf user keys and keyshares does not equal target %s verifying key", subtree.target.ID))
	}
	return userKeySum, nil
}

// AggregateLeavesTransactions holds the server-constructed exit package.
type AggregateLeavesTransactions struct {
	// RefundTx spends the target's defining outpoint with no timelock, pays
	// the full prevout value to P2TR(aggregated user key), and carries an
	// ephemeral anchor for CPFP. Stored as the target's raw_refund_tx.
	RefundTx *wire.MsgTx
	// WatchtowerRefundTx spends the same outpoint with a relative timelock of
	// DirectTimelockOffset and the default fee deducted, so the watchtower
	// can broadcast it without CPFP. Stored as direct_from_cpfp_refund_tx.
	WatchtowerRefundTx *wire.MsgTx
	// PrevOut is the output both transactions spend (pays the target's
	// verifying key).
	PrevOut *wire.TxOut
}

// aggregateLeavesPrevOutpoint resolves the outpoint that defines the target
// node: the output of its parent's node tx at the target's vout, or the
// on-chain deposit outpoint when the target is the tree root.
func aggregateLeavesPrevOutpoint(ctx context.Context, target *ent.TreeNode) (wire.OutPoint, *wire.TxOut, error) {
	outpoint, prevOut, err := aggregateLeavesDerivePrevOutpoint(ctx, target)
	if err != nil {
		return wire.OutPoint{}, nil, err
	}

	// The exit package double-spends whatever the target's own node tx spends,
	// so the two must name the same outpoint. Every operator derives the
	// prevout identically, so a disagreement between them yields a package
	// that passes every local check and is still unspendable on-chain.
	//
	// This rejects a multi-UTXO deposit root: its node tx has several inputs
	// while tree.base_txid/vout names only the primary UTXO, and the root's
	// value is their sum, so the derived prevout would claim more value than
	// the outpoint holds.
	targetTx, err := common.TxFromRawTxBytes(target.RawTx)
	if err != nil {
		return wire.OutPoint{}, nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("failed to parse target %s node tx: %w", target.ID, err))
	}
	if len(targetTx.TxIn) != 1 {
		return wire.OutPoint{}, nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("target %s node tx has %d inputs; leaf aggregation requires a single-input node tx", target.ID, len(targetTx.TxIn)))
	}
	if targetTx.TxIn[0].PreviousOutPoint != outpoint {
		return wire.OutPoint{}, nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("target %s node tx spends %s but the derived exit outpoint is %s", target.ID, targetTx.TxIn[0].PreviousOutPoint, outpoint))
	}
	return outpoint, prevOut, nil
}

// aggregateLeavesDerivePrevOutpoint derives the outpoint from row state. Callers
// should use aggregateLeavesPrevOutpoint, which additionally proves the result
// against the target's own node tx.
func aggregateLeavesDerivePrevOutpoint(ctx context.Context, target *ent.TreeNode) (wire.OutPoint, *wire.TxOut, error) {
	// The parent is read without a row lock even though its RawTx defines the
	// outpoint the exit package spends. RawTx is not globally immutable — renew
	// rewrites it (renew_leaf_handler.go) — but only ever for a leaf, and an
	// aggregation target branches, so its parent is an internal SPLITTED node
	// that no user flow rewrites. Should that stop holding, the failure is
	// closed rather than silent: each operator's FROST share is bound to the
	// sighash over its own snapshot, so a divergent prevout fails signature
	// aggregation instead of producing a package over the wrong outpoint.
	parent, err := target.QueryParent().Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return wire.OutPoint{}, nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to query parent of target %s: %w", target.ID, err))
	}
	if parent != nil {
		parentTx, err := common.TxFromRawTxBytes(parent.RawTx)
		if err != nil {
			return wire.OutPoint{}, nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("failed to parse parent node tx for target %s: %w", target.ID, err))
		}
		if target.Vout < 0 || int(target.Vout) >= len(parentTx.TxOut) {
			return wire.OutPoint{}, nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("target %s vout %d out of range for parent tx with %d outputs", target.ID, target.Vout, len(parentTx.TxOut)))
		}
		return wire.OutPoint{Hash: parentTx.TxHash(), Index: uint32(target.Vout)}, parentTx.TxOut[target.Vout], nil
	}

	tree, err := target.QueryTree().Only(ctx)
	if err != nil {
		return wire.OutPoint{}, nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to query tree for root target %s: %w", target.ID, err))
	}
	if tree.Vout < 0 {
		return wire.OutPoint{}, nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("tree %s has negative vout %d", tree.ID, tree.Vout))
	}
	if tree.BaseTxid.Hash() == (chainhash.Hash{}) {
		return wire.OutPoint{}, nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("tree %s has no base txid; the deposit is not yet recorded", tree.ID))
	}
	pkScript, err := common.P2TRScriptFromPubKey(target.VerifyingPubkey)
	if err != nil {
		return wire.OutPoint{}, nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("failed to construct root prevout script: %w", err))
	}
	return wire.OutPoint{Hash: tree.BaseTxid.Hash(), Index: uint32(tree.Vout)},
		&wire.TxOut{Value: int64(target.Value), PkScript: pkScript}, nil
}

// constructAggregateLeavesTransactions builds the expected exit package from
// local state and the user-supplied sequences (validated to carry exactly the
// expected timelocks), following the never-trust-client discipline: the user
// bytes are only accepted if they match these constructions exactly.
func constructAggregateLeavesTransactions(
	ctx context.Context,
	target *ent.TreeNode,
	aggregatedUserKey keys.Public,
	refundJob *pbspark.UserSignedTxSigningJob,
	watchtowerJob *pbspark.UserSignedTxSigningJob,
) (*AggregateLeavesTransactions, error) {
	if refundJob == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("refund_tx_signing_job is required"))
	}
	if watchtowerJob == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("watchtower_refund_tx_signing_job is required"))
	}

	prevOutpoint, prevOut, err := aggregateLeavesPrevOutpoint(ctx, target)
	if err != nil {
		return nil, err
	}

	refundSequence, err := bitcointransaction.GetAndValidateUserSequence(refundJob.GetRawTx())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to validate refund tx sequence: %w", err))
	}
	if err := bitcointransaction.ValidateSequenceTimelock(refundSequence, spark.ZeroTimelock); err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("aggregated refund tx must carry no timelock: %w", err))
	}
	watchtowerSequence, err := bitcointransaction.GetAndValidateUserSequence(watchtowerJob.GetRawTx())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to validate watchtower refund tx sequence: %w", err))
	}
	if err := bitcointransaction.ValidateSequenceTimelock(watchtowerSequence, spark.DirectTimelockOffset); err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("watchtower refund tx must carry timelock %d: %w", spark.DirectTimelockOffset, err))
	}

	exitPkScript, err := common.P2TRScriptFromPubKey(aggregatedUserKey)
	if err != nil {
		return nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("failed to construct exit script: %w", err))
	}

	refundTx := wire.NewMsgTx(3)
	refundTx.AddTxIn(&wire.TxIn{PreviousOutPoint: prevOutpoint, Sequence: refundSequence})
	refundTx.AddTxOut(&wire.TxOut{Value: prevOut.Value, PkScript: exitPkScript})
	refundTx.AddTxOut(common.EphemeralAnchorOutput())

	watchtowerTx := wire.NewMsgTx(3)
	watchtowerTx.AddTxIn(&wire.TxIn{PreviousOutPoint: prevOutpoint, Sequence: watchtowerSequence})
	watchtowerTx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(prevOut.Value), PkScript: exitPkScript})

	userRawTxs := [][]byte{refundJob.GetRawTx(), watchtowerJob.GetRawTx()}
	expectedTxs := []*wire.MsgTx{refundTx, watchtowerTx}
	if err := validateUserTransactions(userRawTxs, expectedTxs); err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("user transaction validation failed: %w", err))
	}

	return &AggregateLeavesTransactions{RefundTx: refundTx, WatchtowerRefundTx: watchtowerTx, PrevOut: prevOut}, nil
}

// buildAggregateLeavesSigningJobs constructs the two deterministic-ID signing
// jobs signed under the in-memory sum of the leaves' keyshares.
func buildAggregateLeavesSigningJobs(
	ctx context.Context,
	subtree *aggregateLeavesSubtree,
	sumKeyshare *ent.SigningKeyshare,
	txs *AggregateLeavesTransactions,
	refundJob *pbspark.UserSignedTxSigningJob,
	watchtowerJob *pbspark.UserSignedTxSigningJob,
) ([]*helper.SigningJobWithPregeneratedNonce, error) {
	entries := []struct {
		slot    string
		userJob *pbspark.UserSignedTxSigningJob
		tx      *wire.MsgTx
	}{
		{aggregateLeavesRefundSlot, refundJob, txs.RefundTx},
		{aggregateLeavesWatchtowerSlot, watchtowerJob, txs.WatchtowerRefundTx},
	}
	jobs := make([]*helper.SigningJobWithPregeneratedNonce, len(entries))
	for i, e := range entries {
		job, err := helper.NewSigningJobWithDeterministicID(
			aggregateLeavesJobID(subtree.target.ID, e.slot),
			e.userJob, sumKeyshare, subtree.target.VerifyingPubkey, e.tx, txs.PrevOut,
		)
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("signing job construction failed for %s tx: %w", e.slot, err))
		}
		jobs[i] = job
	}
	return jobs, nil
}

// validatedLeafKeyshares loads the leaves' keyshares and proves they can be
// summed into a coherent one: identical thresholds and identical operator
// share sets. Both matter because sumOfSigningKeyshares iterates the first
// keyshare's operator set and a missing entry sums as the identity point — a
// silently wrong public share rather than an error — and because the summed
// secret is only a valid t-of-n share when every input shares the same t.
//
// Used by both the in-memory signing path and the durable rotation at Commit,
// so the row that ends up carrying the summed material is written under the
// same proof that produced the signatures over it.
func validatedLeafKeyshares(ctx context.Context, leaves []*ent.TreeNode) ([]*ent.SigningKeyshare, int32, error) {
	keyshares := make([]*ent.SigningKeyshare, len(leaves))
	var minSigners int32
	for i, leaf := range leaves {
		keyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, 0, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get signing keyshare for leaf %s: %w", leaf.ID, err))
		}
		if i == 0 {
			minSigners = keyshare.MinSigners
		} else {
			if keyshare.MinSigners != minSigners {
				return nil, 0, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("leaf keyshares disagree on min_signers (%d vs %d)", minSigners, keyshare.MinSigners))
			}
			if len(keyshare.PublicShares) != len(keyshares[0].PublicShares) {
				return nil, 0, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("leaf keyshares disagree on operator set size (%d vs %d)", len(keyshares[0].PublicShares), len(keyshare.PublicShares)))
			}
			for opID := range keyshares[0].PublicShares {
				if _, ok := keyshare.PublicShares[opID]; !ok {
					return nil, 0, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("leaf keyshare %s is missing a public share for operator %s", keyshare.ID, opID))
				}
			}
		}
		keyshares[i] = keyshare
	}
	return keyshares, minSigners, nil
}

// sumKeyPackageForLeaves builds the in-memory summed keyshare and the FROST
// key package this SO signs with. Nothing is persisted; the target's keyshare
// row is only rotated to this sum at Commit.
func (h *AggregateLeavesFlowHandler) sumKeyPackageForLeaves(ctx context.Context, leaves []*ent.TreeNode) (*ent.SigningKeyshare, *pbfrost.KeyPackage, error) {
	keyshares, minSigners, err := validatedLeafKeyshares(ctx, leaves)
	if err != nil {
		return nil, nil, err
	}
	sum, err := ent.SumOfSigningKeyshares(ctx, keyshares)
	if err != nil {
		return nil, nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("failed to sum leaf keyshares: %w", err))
	}
	sum.MinSigners = minSigners
	keyPackage := &pbfrost.KeyPackage{
		Identifier:   h.config.Identifier,
		SecretShare:  sum.SecretShare.Serialize(),
		PublicShares: keys.ToBytesMap(sum.PublicShares),
		PublicKey:    sum.PublicKey.Serialize(),
		MinSigners:   uint32(minSigners),
	}
	return sum, keyPackage, nil
}

// Prepare runs on every SO: validate the subtree independently, lock every
// node in it, and produce this SO's round-2 shares over both transactions.
func (h *AggregateLeavesFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	req, ok := op.(*pbinternal.AggregateLeavesPrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for aggregate leaves prepare", op)
	}
	targetID, leafIDs, ownerKey, err := parseAggregateLeavesIdentifiers(req.GetTargetNodeId(), req.GetLeafIds(), req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, err
	}

	subtree, err := loadAggregateLeavesSubtree(ctx, targetID, leafIDs, true)
	if err != nil {
		return nil, err
	}
	aggregatedUserKey, err := validateAggregateLeavesSubtree(ctx, subtree, ownerKey)
	if err != nil {
		return nil, err
	}
	if err := validateAggregateLeavesUserJobs(aggregatedUserKey, req.GetRefundTxSigningJob(), req.GetWatchtowerRefundTxSigningJob()); err != nil {
		return nil, err
	}
	txs, err := constructAggregateLeavesTransactions(ctx, subtree.target, aggregatedUserKey, req.GetRefundTxSigningJob(), req.GetWatchtowerRefundTxSigningJob())
	if err != nil {
		return nil, err
	}

	sumKeyshare, keyPackage, err := h.sumKeyPackageForLeaves(ctx, subtree.leaves)
	if err != nil {
		return nil, err
	}
	jobs, err := buildAggregateLeavesSigningJobs(ctx, subtree, sumKeyshare, txs, req.GetRefundTxSigningJob(), req.GetWatchtowerRefundTxSigningJob())
	if err != nil {
		return nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	// Lock the leaves only. They are what a competing flow must not take, and
	// their pre-lock status is recoverable from local state alone (children =>
	// CONSOLIDATED, none => AVAILABLE), so rollback never has to trust a
	// status observed on another operator. The target and the one-child
	// intermediates keep their SPLITTED / SPLIT_LOCKED status: they are held
	// for the duration of the request by the row locks above, and a competing
	// flow reaching them still stops at these locked leaves.
	for _, leaf := range subtree.leaves {
		if _, err := db.TreeNode.UpdateOne(leaf).SetStatus(st.TreeNodeStatusAggregateLock).Save(ctx); err != nil {
			return nil, sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to lock leaf %s for aggregation: %w", leaf.ID, err))
		}
	}

	// Only SOs in the user's round-1 commitment set produce round-2 shares;
	// the rest have validated and locked, which is all Prepare requires.
	if _, inSigningSet := req.GetRefundTxSigningJob().GetSigningCommitments().GetSigningCommitments()[h.config.Identifier]; !inSigningSet {
		return nil, nil
	}
	internalJobs := make([]*pbinternal.SigningJob, len(jobs))
	for i, job := range jobs {
		internalJobs[i], err = marshalSigningJobHelper(job)
		if err != nil {
			return nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("failed to marshal signing job %d: %w", i, err))
		}
	}
	frostResp, err := signing_handler.NewFrostSigningHandler(h.config).FrostRound2WithKeyPackages(
		ctx,
		&pbinternal.FrostRound2Request{SigningJobs: internalJobs},
		map[uuid.UUID]*pbfrost.KeyPackage{sumKeyshare.ID: keyPackage},
	)
	if err != nil {
		return nil, fmt.Errorf("local frost round 2 failed during aggregate leaves prepare: %w", err)
	}
	return frostResp, nil
}

// validateAggregateLeavesUserJobs enforces the shared per-job requirements:
// the declared signing key is the aggregated user key, the user contributed a
// nonce commitment and signature share, and both jobs carry the same SO
// round-1 commitment set (they are signed by the same selection).
func validateAggregateLeavesUserJobs(aggregatedUserKey keys.Public, refundJob, watchtowerJob *pbspark.UserSignedTxSigningJob) error {
	for slot, job := range map[string]*pbspark.UserSignedTxSigningJob{
		aggregateLeavesRefundSlot:     refundJob,
		aggregateLeavesWatchtowerSlot: watchtowerJob,
	} {
		if job == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("%s tx signing job is required", slot))
		}
		jobKey, err := keys.ParsePublicKey(job.GetSigningPublicKey())
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid signing public key on %s tx signing job: %w", slot, err))
		}
		if !jobKey.Equals(aggregatedUserKey) {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("%s tx signing job key does not equal the sum of the leaf signing keys", slot))
		}
		if job.GetSigningNonceCommitment() == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("%s tx signing job is missing the user nonce commitment", slot))
		}
		if len(job.GetUserSignature()) == 0 {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("%s tx signing job is missing the user signature share", slot))
		}
		if len(job.GetSigningCommitments().GetSigningCommitments()) == 0 {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("%s tx signing job is missing operator round-1 commitments", slot))
		}
	}
	refundOps := refundJob.GetSigningCommitments().GetSigningCommitments()
	watchtowerOps := watchtowerJob.GetSigningCommitments().GetSigningCommitments()
	if len(refundOps) != len(watchtowerOps) {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("signing jobs carry different operator commitment sets"))
	}
	for opID := range refundOps {
		if _, ok := watchtowerOps[opID]; !ok {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("signing jobs carry different operator commitment sets"))
		}
	}
	return nil
}

func parseAggregateLeavesIdentifiers(targetNodeID string, leafIDStrs []string, ownerKeyBytes []byte) (uuid.UUID, []uuid.UUID, keys.Public, error) {
	targetID, err := uuid.Parse(targetNodeID)
	if err != nil {
		return uuid.Nil, nil, keys.Public{}, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid target node id %q: %w", targetNodeID, err))
	}
	if len(leafIDStrs) < 2 {
		return uuid.Nil, nil, keys.Public{}, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("at least two leaf ids are required"))
	}
	// Bound the work one call can commission. Every leaf costs a keyshare read
	// and a summed point on each operator, all inside the row-locked Prepare
	// transaction that the whole 2PC round waits on, so an unbounded leaf set
	// turns one request into cluster-wide lock-hold time. The cap sits far above
	// any real branching factor.
	if len(leafIDStrs) > maxAggregateLeavesLeafCount {
		return uuid.Nil, nil, keys.Public{}, sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("cannot aggregate %d leaves in one call; the maximum is %d", len(leafIDStrs), maxAggregateLeavesLeafCount))
	}
	leafIDs := make([]uuid.UUID, len(leafIDStrs))
	for i, s := range leafIDStrs {
		leafIDs[i], err = uuid.Parse(s)
		if err != nil {
			return uuid.Nil, nil, keys.Public{}, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid leaf id %q: %w", s, err))
		}
	}
	ownerKey, err := keys.ParsePublicKey(ownerKeyBytes)
	if err != nil {
		return uuid.Nil, nil, keys.Public{}, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid owner identity public key: %w", err))
	}
	return targetID, leafIDs, ownerKey, nil
}

// Commit applies the finalized aggregation on this SO.
func (h *AggregateLeavesFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.AggregateLeavesCommitRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for aggregate leaves commit", op)
	}
	return applyAggregateLeavesCommit(ctx, h.config, req)
}

// applyAggregateLeavesCommit is shared by the coordinator (inside
// BuildCommitPayload, same request tx as the COMMITTED decision) and by
// participants (gossip commit dispatch). It verifies the signed package
// against locally reconstructed prevouts before persisting anything, rotates
// the target's keyshare to the sum of the leaves' keyshares, installs the
// exit package on the target, and retires everything below it.
func applyAggregateLeavesCommit(ctx context.Context, config *so.Config, req *pbinternal.AggregateLeavesCommitRequest) error {
	targetID, leafIDs, ownerKey, err := parseAggregateLeavesIdentifiers(req.GetTargetNodeId(), req.GetLeafIds(), req.GetOwnerIdentityPublicKey())
	if err != nil {
		return err
	}
	aggregatedUserKey, err := keys.ParsePublicKey(req.GetAggregatedOwnerSigningPublicKey())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid aggregated owner signing public key: %w", err))
	}

	signedRefundTx, err := common.TxFromRawTxBytes(req.GetSignedRefundTx())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid signed refund tx: %w", err))
	}
	signedWatchtowerTx, err := common.TxFromRawTxBytes(req.GetSignedWatchtowerRefundTx())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid signed watchtower refund tx: %w", err))
	}

	subtree, err := loadAggregateLeavesSubtree(ctx, targetID, leafIDs, true)
	if err != nil {
		return err
	}
	target := subtree.target

	if target.Status == st.TreeNodeStatusConsolidated {
		// Both slots must match to call this a redelivery of the same decision.
		// Matching on the refund tx alone would silently keep a stored
		// watchtower tx that differs from the one the rest of the cluster
		// committed, leaving this SO's exit package permanently divergent.
		if target.RawRefundTxid.Hash() == signedRefundTx.TxHash() &&
			target.DirectFromCpfpRefundTxid.Hash() == signedWatchtowerTx.TxHash() {
			logging.GetLoggerFromContext(ctx).Sugar().Infof("aggregate leaves commit: target %s already CONSOLIDATED, idempotent retry", target.ID)
			return nil
		}
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("target %s is CONSOLIDATED with a different exit package", target.ID))
	}
	// Any node in this subtree recording an on-chain spend means the exit
	// package is already dead: it spends the same outpoint the target's own node
	// tx spends, and a descendant can only have confirmed if that node tx (or
	// something above it) already won that spend. Consolidating anyway would
	// install an unspendable package, retire the leaves, and clear the direct tx
	// that is the actual remaining way out.
	//
	// Prepare rejects all of these statuses, so reaching here means the chain
	// watcher recorded a confirmation after Prepare and before a delayed gossip
	// commit. AlreadyExists rather than a bare error because gossip treats it as
	// success and marks the participant row terminal; anything else would be
	// redelivered forever.
	if node := firstAggregateLeavesOnChainNode(subtree); node != nil {
		return sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("aggregate leaves commit: declining to consolidate target %s, node %s is %s so the exit package's input is already spent", target.ID, node.ID, node.Status))
	}

	if target.Status != st.TreeNodeStatusSplitted {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("target %s status is %s, expected %s", target.ID, target.Status, st.TreeNodeStatusSplitted))
	}
	for _, leaf := range subtree.leaves {
		if leaf.Status != st.TreeNodeStatusAggregateLock {
			return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("leaf %s status is %s, expected %s", leaf.ID, leaf.Status, st.TreeNodeStatusAggregateLock))
		}
	}

	// Re-derive the aggregated owner key rather than trusting the payload: it
	// becomes the target's owner_signing_pubkey, and the next level's
	// aggregation depends on owner + rotated keyshare summing to the
	// verifying key.
	expectedUserKey, err := aggregateLeavesUserKey(subtree.leaves)
	if err != nil {
		return err
	}
	if !expectedUserKey.Equals(aggregatedUserKey) {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("commit aggregated owner signing key does not equal the sum of the leaf signing keys"))
	}

	// Check both signatures AND both transaction shapes against locally
	// derived state. A signature check alone would accept any transaction
	// ever signed under the target's verifying key at this outpoint — the
	// target's own node tx among them — and would not catch the two slots
	// being swapped.
	outpoint, prevOut, err := aggregateLeavesPrevOutpoint(ctx, target)
	if err != nil {
		return err
	}
	pkg := []struct {
		tx       *wire.MsgTx
		slot     string
		value    int64
		timelock uint32
		anchor   bool
	}{
		{signedRefundTx, aggregateLeavesRefundSlot, prevOut.Value, spark.ZeroTimelock, true},
		{signedWatchtowerTx, aggregateLeavesWatchtowerSlot, common.MaybeApplyFee(prevOut.Value), spark.DirectTimelockOffset, false},
	}
	exitScript, err := common.P2TRScriptFromPubKey(expectedUserKey)
	if err != nil {
		return sparkerrors.InternalDataInconsistency(fmt.Errorf("failed to build exit script: %w", err))
	}
	for _, p := range pkg {
		if err := validateAggregateLeavesSignedTx(p.tx, p.slot, outpoint, exitScript, p.value, p.timelock, p.anchor); err != nil {
			return err
		}
		if err := common.VerifySignatureSingleInput(p.tx, 0, prevOut); err != nil {
			return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("signed %s tx failed signature verification: %w", p.slot, err))
		}
	}

	targetKeyshare, err := target.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get target keyshare: %w", err))
	}
	leafKeyshares, leafMinSigners, err := validatedLeafKeyshares(ctx, subtree.leaves)
	if err != nil {
		return err
	}
	if _, err := ent.AggregateKeyshares(ctx, config, leafKeyshares, targetKeyshare.ID); err != nil {
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to rotate target keyshare to the leaf sum: %w", err))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	// AggregateKeyshares rotates the secret and public material but leaves
	// min_signers as the target's own pre-rotation value. Every later signing
	// operation on this node reads the threshold off this row (ent.GetKeyPackage),
	// so leaving a stale one behind would describe the summed material under an
	// unrelated threshold — making the node either unsignable or signable below
	// its real threshold.
	if targetKeyshare.MinSigners != leafMinSigners {
		if _, err := db.SigningKeyshare.UpdateOneID(targetKeyshare.ID).SetMinSigners(leafMinSigners).Save(ctx); err != nil {
			return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to set target keyshare min_signers to %d: %w", leafMinSigners, err))
		}
	}
	if _, err := db.TreeNode.UpdateOne(target).
		SetStatus(st.TreeNodeStatusConsolidated).
		SetRawRefundTx(req.GetSignedRefundTx()).
		SetDirectFromCpfpRefundTx(req.GetSignedWatchtowerRefundTx()).
		ClearDirectRefundTx().
		ClearDirectTx().
		SetOwnerSigningPubkey(aggregatedUserKey).
		SetOwnerIdentityPubkey(ownerKey).
		Save(ctx); err != nil {
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to consolidate target %s: %w", target.ID, err))
	}
	// Retire the descendants by status only, keeping their transactions.
	//
	// AGGREGATED is in the watchtower's terminal set, so nothing here is
	// broadcast automatically — the consolidated package is the live exit path
	// and these transactions must not race it. They are retained for the case
	// where that path dies: the target's own node tx is still a valid
	// double-spend of the outpoint the package spends, and if an earlier
	// holder gets it confirmed, the subtree becomes the only way out. Keeping
	// the bytes means recovery is an operational decision rather than an
	// impossibility.
	for _, node := range subtree.descendants() {
		if _, err := db.TreeNode.UpdateOne(node).
			SetStatus(st.TreeNodeStatusAggregated).
			Save(ctx); err != nil {
			return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to retire node %s: %w", node.ID, err))
		}
	}
	return nil
}

// aggregateLeavesOnChainStatuses are the statuses the chain watcher sets from
// observed chain state.
var aggregateLeavesOnChainStatuses = map[st.TreeNodeStatus]bool{
	st.TreeNodeStatusOnChain:      true,
	st.TreeNodeStatusExited:       true,
	st.TreeNodeStatusParentExited: true,
}

// firstAggregateLeavesOnChainNode returns any node in the subtree whose status
// records an observed on-chain spend, or nil. The target counts too: it is the
// node whose outpoint the exit package spends.
func firstAggregateLeavesOnChainNode(subtree *aggregateLeavesSubtree) *ent.TreeNode {
	if aggregateLeavesOnChainStatuses[subtree.target.Status] {
		return subtree.target
	}
	for _, node := range subtree.descendants() {
		if aggregateLeavesOnChainStatuses[node.Status] {
			return node
		}
	}
	return nil
}

// Rollback unlocks the leaves this flow locked in Prepare. Accepts both the
// canonical rollback payload and the prepare op echoed by the participant
// reconciler's presumed-abort path — they are equivalent here, because the
// status to restore is derived entirely from this operator's own rows rather
// than carried over the wire. Idempotent: a leaf not in AGGREGATE_LOCK is
// skipped, which also means a rollback can never disturb a node locked by a
// different flow.
func (h *AggregateLeavesFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	var leafIDStrs []string
	var targetNodeID string
	switch r := op.(type) {
	case *pbinternal.AggregateLeavesRollbackRequest:
		leafIDStrs, targetNodeID = r.GetLeafIds(), r.GetTargetNodeId()
	case *pbinternal.AggregateLeavesPrepareRequest:
		leafIDStrs, targetNodeID = r.GetLeafIds(), r.GetTargetNodeId()
	default:
		return fmt.Errorf("unexpected operation type %T for aggregate leaves rollback", op)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	logger := logging.GetLoggerFromContext(ctx)
	restored := 0
	for _, idStr := range leafIDStrs {
		leafID, err := uuid.Parse(idStr)
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid leaf id %q in rollback: %w", idStr, err))
		}
		leaf, err := db.TreeNode.Query().Where(enttreenode.ID(leafID)).ForUpdate().Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				continue
			}
			return sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to query leaf %s: %w", leafID, err))
		}
		if leaf.Status != st.TreeNodeStatusAggregateLock {
			logger.Sugar().Infof("aggregate leaves rollback: leaf %s is %s, not %s, leaving it alone", leaf.ID, leaf.Status, st.TreeNodeStatusAggregateLock)
			continue
		}
		prior, err := aggregateLeavesPriorLeafStatus(ctx, db, leaf)
		if err != nil {
			return err
		}
		if _, err := db.TreeNode.UpdateOne(leaf).SetStatus(prior).Save(ctx); err != nil {
			return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to restore leaf %s to %s: %w", leaf.ID, prior, err))
		}
		logger.Sugar().Infof("aggregate leaves rollback: restored leaf %s to %s", leaf.ID, prior)
		restored++
	}
	logger.Sugar().Infof("aggregate leaves rollback: target %s, restored %d of %d named leaves", targetNodeID, restored, len(leafIDStrs))
	return nil
}

// aggregateLeavesPriorLeafStatus recovers a locked leaf's pre-lock status from
// this operator's own rows. Prepare only accepts AVAILABLE or CONSOLIDATED
// leaves, and those two are distinguishable after the fact: a CONSOLIDATED
// node is one that already absorbed a subtree, so it has children, while an
// ordinary available leaf has none.
func aggregateLeavesPriorLeafStatus(ctx context.Context, db *ent.Client, leaf *ent.TreeNode) (st.TreeNodeStatus, error) {
	children, err := db.TreeNode.Query().Where(enttreenode.HasParentWith(enttreenode.ID(leaf.ID))).Count(ctx)
	if err != nil {
		return "", sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to count children of leaf %s: %w", leaf.ID, err))
	}
	if children > 0 {
		return st.TreeNodeStatusConsolidated, nil
	}
	return st.TreeNodeStatusAvailable, nil
}

// ValidateDecisionAgainstPrepare binds a gossip-delivered commit/rollback to
// what this SO actually prepared. Beyond the target and leaf set, a commit
// must carry the very transactions this SO byte-validated at Prepare and the
// same owner identity: a signature check alone would accept any tx ever
// signed under the target's verifying key at the same outpoint — including
// the target's own node tx — and would not notice the two slots being
// swapped.
func (h *AggregateLeavesFlowHandler) ValidateDecisionAgainstPrepare(prepareOp, decisionOp proto.Message) error {
	prepare, ok := prepareOp.(*pbinternal.AggregateLeavesPrepareRequest)
	if !ok {
		return fmt.Errorf("unexpected prepare operation type %T for aggregate leaves", prepareOp)
	}
	var targetNodeID string
	var leafIDs []string
	commit, isCommit := decisionOp.(*pbinternal.AggregateLeavesCommitRequest)
	switch d := decisionOp.(type) {
	case *pbinternal.AggregateLeavesCommitRequest:
		targetNodeID, leafIDs = d.GetTargetNodeId(), d.GetLeafIds()
	case *pbinternal.AggregateLeavesRollbackRequest:
		targetNodeID, leafIDs = d.GetTargetNodeId(), d.GetLeafIds()
	case *pbinternal.AggregateLeavesPrepareRequest:
		targetNodeID, leafIDs = d.GetTargetNodeId(), d.GetLeafIds()
	default:
		return fmt.Errorf("unexpected decision operation type %T for aggregate leaves", decisionOp)
	}
	if targetNodeID != prepare.GetTargetNodeId() {
		return fmt.Errorf("decision target node %s does not match prepared target node %s", targetNodeID, prepare.GetTargetNodeId())
	}
	if isCommit {
		if !bytes.Equal(commit.GetOwnerIdentityPublicKey(), prepare.GetOwnerIdentityPublicKey()) {
			return fmt.Errorf("commit owner identity key does not match the prepared owner")
		}
		if err := decisionTxMatchesPrepared(commit.GetSignedRefundTx(), prepare.GetRefundTxSigningJob().GetRawTx(), aggregateLeavesRefundSlot); err != nil {
			return err
		}
		if err := decisionTxMatchesPrepared(commit.GetSignedWatchtowerRefundTx(), prepare.GetWatchtowerRefundTxSigningJob().GetRawTx(), aggregateLeavesWatchtowerSlot); err != nil {
			return err
		}
	}
	// Exact set equality, not length plus membership: a decision naming
	// [A, A] against a prepared [A, B] would otherwise pass, and a rollback
	// built that way would restore only A and strand B in AGGREGATE_LOCK.
	if len(leafIDs) != len(prepare.GetLeafIds()) {
		return fmt.Errorf("decision names %d leaves, prepared %d", len(leafIDs), len(prepare.GetLeafIds()))
	}
	remaining := make(map[string]struct{}, len(prepare.GetLeafIds()))
	for _, id := range prepare.GetLeafIds() {
		remaining[id] = struct{}{}
	}
	for _, id := range leafIDs {
		if _, ok := remaining[id]; !ok {
			return fmt.Errorf("decision leaf %s was not part of the prepared leaf set, or is named more than once", id)
		}
		delete(remaining, id)
	}
	return nil
}

// aggregateLeavesUserKey returns the aggregated user signing key,
// sum(leaf.OwnerSigningPubkey).
func aggregateLeavesUserKey(leaves []*ent.TreeNode) (keys.Public, error) {
	if len(leaves) == 0 {
		return keys.Public{}, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("no leaves to aggregate"))
	}
	sum := leaves[0].OwnerSigningPubkey
	for _, leaf := range leaves[1:] {
		sum = sum.Add(leaf.OwnerSigningPubkey)
	}
	return sum, nil
}

// validateAggregateLeavesSignedTx checks a signed exit transaction against
// the shape this flow is allowed to persist: one input spending the target's
// defining outpoint at the expected timelock, paying the expected value to
// the aggregated user key, with the anchor rule for its slot.
func validateAggregateLeavesSignedTx(tx *wire.MsgTx, slot string, outpoint wire.OutPoint, exitScript []byte, value int64, timelock uint32, hasAnchorOutput bool) error {
	if len(tx.TxIn) != 1 {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("signed %s tx has %d inputs, expected 1", slot, len(tx.TxIn)))
	}
	if tx.TxIn[0].PreviousOutPoint != outpoint {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("signed %s tx spends %s, expected the target's defining outpoint %s", slot, tx.TxIn[0].PreviousOutPoint, outpoint))
	}
	if err := bitcointransaction.ValidateSequenceTimelock(tx.TxIn[0].Sequence, timelock); err != nil {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("signed %s tx timelock: %w", slot, err))
	}
	expectedOutputs := 1
	if hasAnchorOutput {
		expectedOutputs = 2
	}
	if len(tx.TxOut) != expectedOutputs {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("signed %s tx has %d outputs, expected %d", slot, len(tx.TxOut), expectedOutputs))
	}
	if tx.TxOut[0].Value != value {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("signed %s tx pays %d sats, expected %d", slot, tx.TxOut[0].Value, value))
	}
	if !bytes.Equal(tx.TxOut[0].PkScript, exitScript) {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("signed %s tx does not pay the aggregated user key", slot))
	}
	if hasAnchorOutput && !bytes.Equal(tx.TxOut[1].PkScript, common.EphemeralAnchorOutput().PkScript) {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("signed %s tx is missing the ephemeral anchor output", slot))
	}
	return nil
}

// decisionTxMatchesPrepared reports whether a signed transaction from the
// commit payload is the prepared one. CompareTransactions ignores witnesses,
// which is exactly right: the coordinator adds the aggregated signature to
// bytes that are otherwise identical to what Prepare validated.
func decisionTxMatchesPrepared(signedRaw, preparedRaw []byte, slot string) error {
	signedTx, err := common.TxFromRawTxBytes(signedRaw)
	if err != nil {
		return fmt.Errorf("invalid signed %s tx in decision: %w", slot, err)
	}
	preparedTx, err := common.TxFromRawTxBytes(preparedRaw)
	if err != nil {
		return fmt.Errorf("invalid prepared %s tx: %w", slot, err)
	}
	if err := common.CompareTransactions(preparedTx, signedTx); err != nil {
		return fmt.Errorf("decision %s tx does not match the prepared one: %w", slot, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// aggregateLeavesCoordinatorFlow — coordinator side
// ---------------------------------------------------------------------------

type aggregateLeavesCoordinatorFlow struct {
	*AggregateLeavesFlowHandler

	prepareOp *pbinternal.AggregateLeavesPrepareRequest

	subtree           *aggregateLeavesSubtree
	aggregatedUserKey keys.Public
	txs               *AggregateLeavesTransactions

	// Populated in BuildCommitPayload for the entrypoint's response.
	signedRefundTx     []byte
	signedWatchtowerTx []byte
}

var _ consensus.CoordinatorFlow = (*aggregateLeavesCoordinatorFlow)(nil)

func (f *aggregateLeavesCoordinatorFlow) PrepareOp() proto.Message {
	return f.prepareOp
}

func (f *aggregateLeavesCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.AggregateLeavesRollbackRequest{
		TargetNodeId: f.prepareOp.GetTargetNodeId(),
		LeafIds:      f.prepareOp.GetLeafIds(),
	}
}

// resolveCommitInputs re-derives everything the commit signs against from a
// single locked read, replacing the snapshot the entrypoint took before the
// engine ran. That earlier read was unlocked; the coordinator's own Prepare
// has since reloaded and locked these rows in this same transaction.
//
// Subtree, aggregated user key, and both transactions are refreshed together
// on purpose: refreshing only some of them would sign a package derived half
// from locked state and half from a snapshot Prepare never validated.
func (f *aggregateLeavesCoordinatorFlow) resolveCommitInputs(ctx context.Context) error {
	targetID, leafIDs, _, err := parseAggregateLeavesIdentifiers(f.prepareOp.GetTargetNodeId(), f.prepareOp.GetLeafIds(), f.prepareOp.GetOwnerIdentityPublicKey())
	if err != nil {
		return err
	}
	subtree, err := loadAggregateLeavesSubtree(ctx, targetID, leafIDs, true)
	if err != nil {
		return err
	}
	aggregatedUserKey, err := aggregateLeavesUserKey(subtree.leaves)
	if err != nil {
		return err
	}
	txs, err := constructAggregateLeavesTransactions(ctx, subtree.target, aggregatedUserKey, f.prepareOp.GetRefundTxSigningJob(), f.prepareOp.GetWatchtowerRefundTxSigningJob())
	if err != nil {
		return err
	}
	f.subtree = subtree
	f.aggregatedUserKey = aggregatedUserKey
	f.txs = txs
	return nil
}

// BuildCommitPayload aggregates the SO round-2 shares with the user's shares
// into final signatures, verifies them, applies the commit on the coordinator
// (same request tx as the COMMITTED decision), and returns the commit op
// carrying the fully signed package.
func (f *aggregateLeavesCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	allShares, participantIDs, err := collectSignatureShares(results)
	if err != nil {
		return nil, fmt.Errorf("failed to collect signature shares: %w", err)
	}

	if err := f.resolveCommitInputs(ctx); err != nil {
		return nil, err
	}

	// The aggregation needs the summed public shares; recompute them from the
	// coordinator's rows (identical on every SO by construction).
	sumKeyshare, _, err := f.sumKeyPackageForLeaves(ctx, f.subtree.leaves)
	if err != nil {
		return nil, err
	}
	publicKeys := make(map[string][]byte, len(participantIDs))
	for _, id := range participantIDs {
		share, ok := sumKeyshare.PublicShares[id]
		if !ok {
			return nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("missing summed public share for operator %s", id))
		}
		publicKeys[id] = share.Serialize()
	}

	userJobs := []*pbspark.UserSignedTxSigningJob{
		f.prepareOp.GetRefundTxSigningJob(),
		f.prepareOp.GetWatchtowerRefundTxSigningJob(),
	}
	slots := []string{aggregateLeavesRefundSlot, aggregateLeavesWatchtowerSlot}
	expectedTxs := []*wire.MsgTx{f.txs.RefundTx, f.txs.WatchtowerRefundTx}

	frostConn, err := f.config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("unable to connect to frost: %w", err)
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	batch := newFrostAggregationBatch(f.config)
	jobIDs := make([]string, len(slots))
	for i, slot := range slots {
		jobID := aggregateLeavesJobID(f.subtree.target.ID, slot).String()
		jobIDs[i] = jobID
		shares, ok := allShares[jobID]
		if !ok {
			return nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("missing signature shares for %s job %s", slot, jobID))
		}
		sigHash, err := sighash.FromTx(expectedTxs[i], 0, f.txs.PrevOut)
		if err != nil {
			return nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("failed to compute sighash for %s tx: %w", slot, err))
		}
		if err := batch.addRequest(jobID, &pbfrost.AggregateFrostRequest{
			Message:            sigHash.Serialize(),
			SignatureShares:    shares,
			PublicShares:       publicKeys,
			VerifyingKey:       f.subtree.target.VerifyingPubkey.Serialize(),
			Commitments:        userJobs[i].GetSigningCommitments().GetSigningCommitments(),
			UserCommitments:    userJobs[i].GetSigningNonceCommitment(),
			UserPublicKey:      f.aggregatedUserKey.Serialize(),
			UserSignatureShare: userJobs[i].GetUserSignature(),
		}); err != nil {
			return nil, fmt.Errorf("failed to build aggregation for %s job: %w", slot, err)
		}
	}
	aggregated, err := batch.aggregate(ctx, frostClient)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate signatures: %w", err)
	}

	_, signedRefundBytes, err := applyAndVerifySignature(f.txs.RefundTx, aggregated[jobIDs[0]], f.txs.PrevOut, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply refund tx signature: %w", err)
	}
	_, signedWatchtowerBytes, err := applyAndVerifySignature(f.txs.WatchtowerRefundTx, aggregated[jobIDs[1]], f.txs.PrevOut, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to apply watchtower refund tx signature: %w", err)
	}

	commitReq := &pbinternal.AggregateLeavesCommitRequest{
		TargetNodeId:                    f.prepareOp.GetTargetNodeId(),
		LeafIds:                         f.prepareOp.GetLeafIds(),
		SignedRefundTx:                  signedRefundBytes,
		SignedWatchtowerRefundTx:        signedWatchtowerBytes,
		AggregatedOwnerSigningPublicKey: f.aggregatedUserKey.Serialize(),
		OwnerIdentityPublicKey:          f.prepareOp.GetOwnerIdentityPublicKey(),
	}
	if err := applyAggregateLeavesCommit(ctx, f.config, commitReq); err != nil {
		return nil, fmt.Errorf("failed to apply aggregate leaves commit on coordinator: %w", err)
	}
	f.signedRefundTx = signedRefundBytes
	f.signedWatchtowerTx = signedWatchtowerBytes
	return commitReq, nil
}
