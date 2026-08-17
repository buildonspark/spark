package handler

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/sighash"
	"github.com/lightsparkdev/spark/so/ent"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	transferpkg "github.com/lightsparkdev/spark/so/transfer"
)

// verifyMpcAuthorization checks a multiparty submission's group-signed authorization against this operator's own
// state: first the threshold signature over the recomputed whole-submission payload, then every named fact against
// what the operator holds or rebuilds — leaf ownership, availability, value, owner signing key, receiver-bound
// refund outputs, and the refund-sighashes digest recomputed from prevouts taken from the operator's own leaf rows,
// never from submitted bytes. Read-only: leaves are loaded without locks (the consensus prepare re-loads FOR UPDATE
// before any state change), and the atomic transfer-id guard remains the insert's uniqueness constraint — the
// freshness check here only converts a reused id into a precise early error. Returns the leaf rows it verified
// against, so callers consuming leaf state (the tweak combination) read the same rows the facts were checked on.
func verifyMpcAuthorization(ctx context.Context, submission *transferpkg.MpcSubmission) (map[uuid.UUID]*ent.TreeNode, error) {
	if err := common.VerifySignatureWithScheme(
		submission.SenderIdentityPublicKey(),
		submission.AuthSignatureScheme(),
		submission.AuthSignature(),
		submission.AuthorizationPayload(),
	); err != nil {
		return nil, sparkerrors.InvalidArgumentMpcAuthorizationSignatureInvalid(fmt.Errorf("transfer authorization signature does not verify: %w", err))
	}
	if expiry := submission.ExpiryTime(); !expiry.After(time.Now()) {
		return nil, sparkerrors.FailedPreconditionMpcAuthorizationMismatch(fmt.Errorf("authorization expiry %s is not in the future", expiry))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	exists, err := db.Transfer.Query().Where(enttransfer.IDEQ(submission.TransferID())).Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to check for an existing transfer %s: %w", submission.TransferID(), err)
	}
	if exists {
		return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer %s already exists", submission.TransferID()))
	}

	leafIDMap := make(map[string][]byte, len(submission.Leaves()))
	for _, leaf := range submission.Leaves() {
		leafIDMap[leaf.LeafID().String()] = nil
	}
	dbLeaves, _, err := loadLeaves(ctx, db, leafIDMap, false)
	if err != nil {
		return nil, sparkerrors.FailedPreconditionMpcAuthorizationMismatch(fmt.Errorf("authorized leaves do not match operator state: %w", err))
	}
	leavesByID := make(map[uuid.UUID]*ent.TreeNode, len(dbLeaves))
	for _, dbLeaf := range dbLeaves {
		leavesByID[dbLeaf.ID] = dbLeaf
	}

	cpfpJobs := mpcJobsByLeafID(submission.LeavesToSend())
	directJobs := mpcJobsByLeafID(submission.DirectLeavesToSend())
	directFromCPFPJobs := mpcJobsByLeafID(submission.DirectFromCPFPLeavesToSend())

	sighashes := make([]transferpkg.MpcLeafRefundSighashes, 0, len(submission.Leaves()))
	for _, leaf := range submission.Leaves() {
		dbLeaf := leavesByID[leaf.LeafID()]
		if err := leafAvailableStatus(dbLeaf); err != nil {
			return nil, err
		}
		if !dbLeaf.OwnerIdentityPubkey.Equals(submission.SenderIdentityPublicKey()) {
			return nil, sparkerrors.FailedPreconditionMpcAuthorizationMismatch(fmt.Errorf("leaf %s is not owned by the sender", leaf.LeafID()))
		}
		if dbLeaf.Value != leaf.AmountSats() {
			return nil, sparkerrors.FailedPreconditionMpcAuthorizationMismatch(fmt.Errorf("authorized amount %d for leaf %s does not match its on-file value %d", leaf.AmountSats(), leaf.LeafID(), dbLeaf.Value))
		}
		if !dbLeaf.OwnerSigningPubkey.Equals(leaf.OwnerSigningPubKey()) {
			return nil, sparkerrors.FailedPreconditionMpcAuthorizationMismatch(fmt.Errorf("authorized owner signing key for leaf %s does not match the on-file key", leaf.LeafID()))
		}

		cpfp, err := mpcRefundSighash(cpfpJobs[leaf.LeafID()], dbLeaf.RawTx, leaf.ReceiverIdentityPubKey())
		if err != nil {
			return nil, sparkerrors.FailedPreconditionMpcAuthorizationMismatch(fmt.Errorf("cpfp refund for leaf %s: %w", leaf.LeafID(), err))
		}
		direct, err := mpcRefundSighash(directJobs[leaf.LeafID()], dbLeaf.DirectTx, leaf.ReceiverIdentityPubKey())
		if err != nil {
			return nil, sparkerrors.FailedPreconditionMpcAuthorizationMismatch(fmt.Errorf("direct refund for leaf %s: %w", leaf.LeafID(), err))
		}
		directFromCPFP, err := mpcRefundSighash(directFromCPFPJobs[leaf.LeafID()], dbLeaf.RawTx, leaf.ReceiverIdentityPubKey())
		if err != nil {
			return nil, sparkerrors.FailedPreconditionMpcAuthorizationMismatch(fmt.Errorf("direct-from-cpfp refund for leaf %s: %w", leaf.LeafID(), err))
		}
		sighashes = append(sighashes, transferpkg.MpcLeafRefundSighashes{
			LeafID:         leaf.LeafID(),
			CPFP:           cpfp,
			Direct:         direct,
			DirectFromCPFP: directFromCPFP,
		})
	}

	if digest := transferpkg.MpcRefundSighashesDigest(sighashes); !bytes.Equal(digest, submission.RefundSighashesDigest()) {
		return nil, sparkerrors.FailedPreconditionMpcAuthorizationMismatch(fmt.Errorf("refund sighashes digest does not match the refund transactions verified against operator state"))
	}
	return leavesByID, nil
}

func mpcJobsByLeafID(jobs []*transferpkg.MpcRefundSigningJob) map[uuid.UUID]*transferpkg.MpcRefundSigningJob {
	byID := make(map[uuid.UUID]*transferpkg.MpcRefundSigningJob, len(jobs))
	for _, job := range jobs {
		byID[job.LeafID()] = job
	}
	return byID
}

// mpcRefundSighash checks that one refund transaction spends the leaf's on-file output and pays the authorized
// receiver, then computes its BIP-341 sighash against that on-file prevout — the message the group's signing
// contributions sign, recomputed rather than taken from the submission.
func mpcRefundSighash(job *transferpkg.MpcRefundSigningJob, sourceTxBytes []byte, receiver keys.Public) ([]byte, error) {
	if len(sourceTxBytes) == 0 {
		return nil, fmt.Errorf("the leaf has no on-file source transaction for this refund flavour")
	}
	sourceTx, err := common.TxFromRawTxBytes(sourceTxBytes)
	if err != nil {
		return nil, fmt.Errorf("unable to parse the leaf's on-file source transaction: %w", err)
	}
	if len(sourceTx.TxOut) == 0 {
		return nil, fmt.Errorf("the leaf's on-file source transaction has no outputs")
	}
	refundTx := job.RefundTx()
	if expected := (wire.OutPoint{Hash: sourceTx.TxHash(), Index: 0}); refundTx.TxIn[0].PreviousOutPoint != expected {
		return nil, fmt.Errorf("refund tx does not spend the leaf's on-file output")
	}
	if err := validateLeafRefundTxOutput(refundTx, receiver); err != nil {
		return nil, err
	}
	hash, err := sighash.FromTx(refundTx, 0, sourceTx.TxOut[0])
	if err != nil {
		return nil, err
	}
	return hash[:], nil
}
