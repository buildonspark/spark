package handler

import (
	"bytes"
	"context"
	"fmt"

	"entgo.io/ent/dialect/sql/sqlgraph"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/errors"
	"go.uber.org/zap"
)

// rawTxTimelock extracts the relative-timelock value (BIP68 nSequence
// masked) from the first input of a serialized Bitcoin transaction.
// Returns an error wrapping the field name so the caller can produce a
// useful diagnostic without re-pasting the parse code at every site.
func rawTxTimelock(rawTx []byte, fieldName string) (uint32, error) {
	tx, err := common.TxFromRawTxBytes(rawTx)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", fieldName, err)
	}
	if len(tx.TxIn) == 0 {
		return 0, fmt.Errorf("%s has no inputs", fieldName)
	}
	return bitcointransaction.GetTimelockFromSequence(tx.TxIn[0].Sequence), nil
}

// InternalRenewLeafHandler is the extend leaf handler for so internal.
type InternalRenewLeafHandler struct {
	config *so.Config
}

// NewInternalRenewLeafHandler creates a new InternalExtendLeafHandler.
func NewInternalRenewLeafHandler(config *so.Config) *InternalRenewLeafHandler {
	return &InternalRenewLeafHandler{
		config: config,
	}
}

// FinalizeRenewNodeTimelock finalizes a renew leaf operation.
// This creates the new split node and updates the extended leaf.
func (h *InternalRenewLeafHandler) FinalizeRenewNodeTimelock(ctx context.Context, req *pbinternal.FinalizeRenewNodeTimelockRequest) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	// Validate extended leaf status before any database writes
	extendedLeafID, err := uuid.Parse(req.Node.Id)
	if err != nil {
		return fmt.Errorf("failed to parse extended leaf id %v: %w", req.Node.Id, err)
	}

	extendedLeafNode, err := db.TreeNode.Query().Where(treenode.ID(extendedLeafID)).ForUpdate().Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to query extended leaf node %s: %w", extendedLeafID, err)
	}

	// Accept both Available (legacy gossip path where no locking occurs) and
	// RenewLocked (2PC consensus path where Prepare locks the node before Commit).
	if extendedLeafNode.Status != st.TreeNodeStatusAvailable && extendedLeafNode.Status != st.TreeNodeStatusRenewLocked {
		return fmt.Errorf("extended leaf node %s must have status Available or RenewLocked, but has status %s", extendedLeafID, extendedLeafNode.Status)
	}

	logger := logging.GetLoggerFromContext(ctx)

	// Stale-replay guards. The split node insert below is unique-constrained
	// and will catch replay of the same gossip payload, but only AFTER side
	// effects (and not at all if a stale replay arrives carrying a
	// fresh splitNode UUID). Add two cheaper, earlier checks:
	//
	//   1. Byte-equality on the extended leaf's tx fields. If the leaf is
	//      already in the target state of this finalize, the original
	//      Commit landed and this is a legitimate redelivery — return nil
	//      so dispatch marks the FlowExecution row COMMITTED.
	//   2. Pre-state precondition. Renew-node finalizes are only legitimate
	//      when the existing leaf's RawTx timelock is at or below the renew
	//      threshold (validateAndConstructNodeTimelock requires <= 300).
	//      A leaf whose current timelock is well above that means another
	//      renew-node has happened since this payload was generated; the
	//      payload is stale and must not be applied.
	if leafFieldsMatchNodeFinalize(extendedLeafNode, req.Node) {
		logger.Info("FinalizeRenewNodeTimelock: leaf already at target state, treating as idempotent",
			zap.String("extended_leaf_id", extendedLeafID.String()))
		return nil
	}
	if err := checkNodeRenewPrecondition(extendedLeafNode.RawTx, extendedLeafID); err != nil {
		return err
	}

	// Process the split node (newly created node) - first node
	splitNode := req.SplitNode
	splitNodeID, err := uuid.Parse(splitNode.Id)
	if err != nil {
		return fmt.Errorf("failed to parse split node %s: %w", splitNode.Id, err)
	}
	splitTreeID, err := uuid.Parse(splitNode.TreeId)
	if err != nil {
		return fmt.Errorf("failed to parse split tree %s: %w", splitNode.Id, err)
	}
	splitSigningKeyshareID, err := uuid.Parse(splitNode.SigningKeyshareId)
	if err != nil {
		return fmt.Errorf("failed to parse split signing keyshare id: %w", err)
	}
	splitParentID := uuid.Nil
	if splitNode.ParentNodeId != nil {
		parentID, err := uuid.Parse(splitNode.GetParentNodeId())
		if err != nil {
			return fmt.Errorf("failed to parse split parent node id: %w", err)
		}
		splitParentID = parentID
	}

	ownerIdentityPubKey, err := keys.ParsePublicKey(splitNode.GetOwnerIdentityPubkey())
	if err != nil {
		return fmt.Errorf("failed to parse owner identity pubkey: %w", err)
	}
	ownerSigningPubKey, err := keys.ParsePublicKey(splitNode.GetOwnerSigningPubkey())
	if err != nil {
		return fmt.Errorf("failed to parse owner signing pubkey: %w", err)
	}
	verifyingPubKey, err := keys.ParsePublicKey(splitNode.GetVerifyingPubkey())
	if err != nil {
		return fmt.Errorf("failed to parse verifying pubkey: %w", err)
	}

	// TODO(mhr): Remove this when the transfer proto has Network and it has been backfilled.
	treeEnt, err := db.Tree.Query().Where(tree.IDEQ(splitTreeID)).Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to query tree %s: %w", splitTreeID, err)
	}

	// Create the split node
	splitNodeMut := db.TreeNode.Create().
		SetID(splitNodeID).
		SetTreeID(splitTreeID).
		SetNetwork(treeEnt.Network).
		SetStatus(st.TreeNodeStatusSplitLocked).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetOwnerSigningPubkey(ownerSigningPubKey).
		SetValue(splitNode.Value).
		SetVerifyingPubkey(verifyingPubKey).
		SetSigningKeyshareID(splitSigningKeyshareID).
		SetRawTx(splitNode.RawTx).
		SetDirectTx(splitNode.DirectTx).
		SetVout(int16(splitNode.Vout))
	if splitParentID != uuid.Nil {
		splitNodeMut.SetParentID(splitParentID)
	}
	_, err = splitNodeMut.Save(ctx)
	if err != nil {
		if sqlgraph.IsUniqueConstraintError(err) {
			return errors.AlreadyExistsDuplicateOperation(
				fmt.Errorf("split node %s already exists: %w", splitNodeID, err))
		}
		if sqlgraph.IsForeignKeyConstraintError(err) {
			return errors.NotFoundMissingEntity(
				fmt.Errorf("referenced entity not found for split node %s: %w", splitNodeID, err))
		}
		return fmt.Errorf("failed to create split node %s: %w", splitNodeID, err)
	}
	logger.Info("Created split node", zap.String("split_node_id", splitNodeID.String()))

	extendedLeaf := req.Node
	_, err = extendedLeafNode.Update().
		SetRawTx(extendedLeaf.RawTx).
		SetRawRefundTx(extendedLeaf.RawRefundTx).
		SetDirectTx(extendedLeaf.DirectTx).
		SetDirectRefundTx(extendedLeaf.DirectRefundTx).
		SetDirectFromCpfpRefundTx(extendedLeaf.DirectFromCpfpRefundTx).
		SetParentID(splitNodeID).
		SetStatus(st.TreeNodeStatusAvailable).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update extended leaf %s: %w", extendedLeafID, err)
	}
	logger.Info("Updated extended leaf",
		zap.String("extended_leaf_id", extendedLeafID.String()),
		zap.String("split_node_id", splitNodeID.String()))
	return nil
}

// leafFieldsMatchNodeFinalize reports whether every tx field a renew-node
// finalize would write is already byte-identical on the extended leaf.
// Used to short-circuit legitimate gossip redeliveries before any side
// effects.
func leafFieldsMatchNodeFinalize(leafNode *ent.TreeNode, target *pbinternal.TreeNode) bool {
	return bytes.Equal(leafNode.RawTx, target.RawTx) &&
		bytes.Equal(leafNode.RawRefundTx, target.RawRefundTx) &&
		bytes.Equal(leafNode.DirectTx, target.DirectTx) &&
		bytes.Equal(leafNode.DirectRefundTx, target.DirectRefundTx) &&
		bytes.Equal(leafNode.DirectFromCpfpRefundTx, target.DirectFromCpfpRefundTx)
}

// checkNodeRenewPrecondition rejects a renew-node finalize whose target
// leaf is not in a renew-eligible state. validateAndConstructNodeTimelock
// only produces a renew-node payload when the existing leaf's RawTx
// timelock is at or below spark.RenewTimelockThreshold (300, including 0
// for the node-zero variant). A current timelock above the threshold
// means another renew-node has happened since the payload was generated
// and applying this old payload would clobber the leaf's newer state.
// Takes raw tx bytes for unit-test friendliness.
func checkNodeRenewPrecondition(currentRawTx []byte, leafID uuid.UUID) error {
	currentTimelock, err := rawTxTimelock(currentRawTx, "current leaf RawTx")
	if err != nil {
		return fmt.Errorf("leaf %s: %w", leafID, err)
	}
	if currentTimelock > spark.RenewTimelockThreshold {
		return errors.AlreadyExistsDuplicateOperation(fmt.Errorf(
			"stale node renew finalize for leaf %s: current RawTx timelock %d > renew threshold %d (leaf has been renewed since this payload was generated)",
			leafID, currentTimelock, spark.RenewTimelockThreshold))
	}
	return nil
}

// FinalizeRenewRefundTimelock finalizes a renew refund timelock operation.
// This only updates the existing leaf node without creating a split node.
func (h *InternalRenewLeafHandler) FinalizeRenewRefundTimelock(ctx context.Context, req *pbinternal.FinalizeRenewRefundTimelockRequest) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	// Validate leaf status before any database writes
	leafID, err := uuid.Parse(req.GetNode().GetId())
	if err != nil {
		return fmt.Errorf("failed to parse leaf %s: %w", req.GetNode().GetId(), err)
	}

	leafNode, err := db.TreeNode.Query().Where(treenode.ID(leafID)).ForUpdate().Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to query leaf %v: %w", leafID, err)
	}

	// Accept both Available (legacy gossip path) and RenewLocked (2PC consensus path).
	if leafNode.Status != st.TreeNodeStatusAvailable && leafNode.Status != st.TreeNodeStatusRenewLocked {
		return fmt.Errorf("leaf node %s must have status Available or RenewLocked, but has status %s", leafID, leafNode.Status)
	}

	leaf := req.Node

	logger := logging.GetLoggerFromContext(ctx)

	// Stale-replay guards. A renew-refund finalize that arrives after a
	// later finalize has already advanced the leaf would silently
	// overwrite newer tx fields. Two separate guards:
	//
	//   1. Byte-equality: the leaf's tx fields already match the request's
	//      target state. This is a legitimate gossip redelivery — return
	//      nil so dispatch marks the FlowExecution row COMMITTED via the
	//      normal success path.
	//   2. Timelock monotonicity: NextSequence (bitcoin_transaction/
	//      validation.go) only ever decrements timelocks within an epoch.
	//      An incoming finalize whose RawTx timelock is >= the current
	//      leaf's timelock is from before a newer renew-refund landed and
	//      must not be applied. Return AlreadyExists so dispatch (with the
	//      AlreadyExists-as-success rule) still marks the row COMMITTED.
	if leafFieldsMatchRefundFinalize(leafNode, leaf) {
		logger.Info("FinalizeRenewRefundTimelock: leaf already at target state, treating as idempotent",
			zap.String("leaf_id", leafID.String()))
		return nil
	}
	if err := checkRefundTimelockMonotonicity(leafNode.RawTx, leaf.RawTx, leafID); err != nil {
		return err
	}

	_, err = leafNode.Update().
		SetRawTx(leaf.RawTx).
		SetRawRefundTx(leaf.RawRefundTx).
		SetDirectTx(leaf.DirectTx).
		SetDirectRefundTx(leaf.DirectRefundTx).
		SetDirectFromCpfpRefundTx(leaf.DirectFromCpfpRefundTx).
		SetStatus(st.TreeNodeStatusAvailable).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update leaf %s: %w", leafID, err)
	}

	logger.Info("Updated leaf for refund timelock renewal", zap.String("leaf_id", leafID.String()))
	return nil
}

// leafFieldsMatchRefundFinalize reports whether every tx field a refund
// finalize would write is already byte-identical on the leaf. Used to
// short-circuit legitimate gossip redeliveries.
func leafFieldsMatchRefundFinalize(leafNode *ent.TreeNode, target *pbinternal.TreeNode) bool {
	return bytes.Equal(leafNode.RawTx, target.RawTx) &&
		bytes.Equal(leafNode.RawRefundTx, target.RawRefundTx) &&
		bytes.Equal(leafNode.DirectTx, target.DirectTx) &&
		bytes.Equal(leafNode.DirectRefundTx, target.DirectRefundTx) &&
		bytes.Equal(leafNode.DirectFromCpfpRefundTx, target.DirectFromCpfpRefundTx)
}

// checkRefundTimelockMonotonicity verifies that the refund-finalize
// payload's RawTx timelock is strictly lower than the current leaf's
// RawTx timelock. The byte-equality short-circuit above already handles
// the legitimate-redelivery case, so any leftover finalize whose timelock
// is >= the current leaf's timelock is from an older state and must be
// rejected to prevent silently overwriting newer state. Takes raw tx
// bytes (rather than *ent.TreeNode) so the check is unit-testable
// without DB setup.
func checkRefundTimelockMonotonicity(currentRawTx, incomingRawTx []byte, leafID uuid.UUID) error {
	currentTimelock, err := rawTxTimelock(currentRawTx, "current leaf RawTx")
	if err != nil {
		return fmt.Errorf("leaf %s: %w", leafID, err)
	}
	incomingTimelock, err := rawTxTimelock(incomingRawTx, "incoming RawTx")
	if err != nil {
		return fmt.Errorf("leaf %s: %w", leafID, err)
	}
	if incomingTimelock >= currentTimelock {
		return errors.AlreadyExistsDuplicateOperation(fmt.Errorf(
			"stale refund finalize for leaf %s: incoming RawTx timelock %d >= current %d",
			leafID, incomingTimelock, currentTimelock))
	}
	return nil
}
