package handler

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/objects"
)

// ExtendLeafHandler is a handler for extending a leaf node.
type ExtendLeafHandler struct {
	config *so.Config
}

// NewExtendLeafHandler creates a new ExtendLeafHandler.
func NewExtendLeafHandler(config *so.Config) *ExtendLeafHandler {
	return &ExtendLeafHandler{
		config: config,
	}
}

func (h *ExtendLeafHandler) ExtendLeaf(ctx context.Context, req *pb.ExtendLeafRequest) (*pb.ExtendLeafResponse, error) {
	return h.extendLeaf(ctx, req, false)
}

func (h *ExtendLeafHandler) ExtendLeafV2(ctx context.Context, req *pb.ExtendLeafRequest) (*pb.ExtendLeafResponse, error) {
	return h.extendLeaf(ctx, req, true)
}

func (h *ExtendLeafHandler) extendLeaf(ctx context.Context, req *pb.ExtendLeafRequest, requireDirectTx bool) (*pb.ExtendLeafResponse, error) {
	reqOwnerIDPubKey, err := keys.ParsePublicKey(req.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqOwnerIDPubKey); err != nil {
		return nil, fmt.Errorf("failed to enforce session identity public key matches: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	leaf, err := getLeafById(ctx, req.LeafId, db)
	if err != nil {
		return nil, fmt.Errorf("failed to get leaf by id: %w", err)
	}

	// Existing flow
	cpfpNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse leaf node tx: %w", err)
	}

	cpfpRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse leaf refund tx: %w", err)
	}

	newCpfpNodeTx, err := common.TxFromRawTxBytes(req.NodeTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse new node tx: %w", err)
	}

	newCpfpRefundTx, err := common.TxFromRawTxBytes(req.RefundTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse new refund tx: %w", err)
	}

	if newCpfpNodeTx.TxIn[0].Sequence >= cpfpRefundTx.TxIn[0].Sequence {
		return nil, fmt.Errorf("new node tx sequence must be less than the CPFP refund tx sequence %d, got %d", cpfpRefundTx.TxIn[0].Sequence, newCpfpNodeTx.TxIn[0].Sequence)
	}

	newCpfpNodeSigningJob, err := createSigningJob(ctx, newCpfpNodeTx, cpfpNodeTx.TxOut[0], req.NodeTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to create new cpfp node signing job: %w", err)
	}

	cpfpRefundSigningJob, err := createSigningJob(ctx, newCpfpRefundTx, newCpfpNodeTx.TxOut[0], req.RefundTxSigningJob, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to create refund signing job: %w", err)
	}

	signingJobs := []*helper.SigningJob{newCpfpNodeSigningJob, cpfpRefundSigningJob}
	directNodeSigningJob := req.GetDirectNodeTxSigningJob()
	directRefundSigningJob := req.GetDirectRefundTxSigningJob()
	directFromCpfpRefundSigningJob := req.GetDirectFromCpfpRefundTxSigningJob()

	if directNodeSigningJob != nil && directRefundSigningJob != nil && directFromCpfpRefundSigningJob != nil {
		directRefundTx, err := common.TxFromRawTxBytes(leaf.DirectRefundTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse leaf refund tx: %w", err)
		}

		directFromCpfpRefundTx, err := common.TxFromRawTxBytes(leaf.DirectFromCpfpRefundTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse leaf direct from cpfp refund tx: %w", err)
		}

		newDirectNodeTx, err := common.TxFromRawTxBytes(req.DirectNodeTxSigningJob.RawTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse new node tx: %w", err)
		}

		newDirectRefundTx, err := common.TxFromRawTxBytes(req.DirectRefundTxSigningJob.RawTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse new refund tx: %w", err)
		}

		newDirectFromCpfpRefundTx, err := common.TxFromRawTxBytes(req.DirectFromCpfpRefundTxSigningJob.RawTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse new direct from cpfp refund tx: %w", err)
		}

		// Validate new transactions
		// TODO: make some shared validation across different handlers
		if err = validateNewTxSequence(newCpfpNodeTx, newDirectNodeTx, directRefundTx, directFromCpfpRefundTx, cpfpRefundTx); err != nil {
			return nil, fmt.Errorf("invalid new node tx sequence: %w", err)
		}

		if err = validateNewTxOutput(leaf, newCpfpNodeTx, newDirectNodeTx, cpfpRefundTx, directRefundTx, directFromCpfpRefundTx); err != nil {
			return nil, fmt.Errorf("invalid new node tx output: %w", err)
		}

		newCpfpNodeOutPoint := newCpfpNodeTx.TxIn[0].PreviousOutPoint
		cpfpRefundOutPoint := cpfpRefundTx.TxIn[0].PreviousOutPoint

		if !newCpfpNodeOutPoint.Hash.IsEqual(&cpfpRefundOutPoint.Hash) || newCpfpNodeOutPoint.Index != cpfpRefundOutPoint.Index {
			return nil, fmt.Errorf("new cpfp node tx must spend old node tx, expected %s:%d, got %s:%d", cpfpRefundOutPoint.Hash, cpfpRefundOutPoint.Index, newCpfpNodeOutPoint.Hash, newCpfpNodeOutPoint.Index)
		}

		newDirectNodeOutPoint := newDirectNodeTx.TxIn[0].PreviousOutPoint
		if !newDirectNodeOutPoint.Hash.IsEqual(&cpfpRefundOutPoint.Hash) || newDirectNodeOutPoint.Index != cpfpRefundOutPoint.Index {
			return nil, fmt.Errorf("new direct node tx must spend old node tx, expected %s:%d, got %s:%d", cpfpRefundOutPoint.Hash, cpfpRefundOutPoint.Index, newDirectNodeOutPoint.Hash, newDirectNodeOutPoint.Index)
		}

		if uint64(newCpfpNodeTx.TxOut[0].Value) != leaf.Value {
			return nil, fmt.Errorf("new cpfp node tx output value must match leaf value, expected %d, got %d", leaf.Value, newCpfpNodeTx.TxOut[0].Value)
		}
		if uint64(newDirectNodeTx.TxOut[0].Value) > leaf.Value {
			return nil, fmt.Errorf("new direct node tx output value must be less than or equal to leaf value, leaf value: %d, direct node tx value: %d", leaf.Value, newDirectNodeTx.TxOut[0].Value)
		}
		if uint64(cpfpRefundTx.TxOut[0].Value) != leaf.Value {
			return nil, fmt.Errorf("cpfp refund tx output value must match leaf value, expected %d, got %d", leaf.Value, cpfpRefundTx.TxOut[0].Value)
		}
		if uint64(directRefundTx.TxOut[0].Value) > leaf.Value {
			return nil, fmt.Errorf("direct refund tx output value must be less than or equal leaf value, leaf value %d, direct refund tx value: %d", leaf.Value, cpfpRefundTx.TxOut[0].Value)
		}
		if uint64(directFromCpfpRefundTx.TxOut[0].Value) > leaf.Value {
			return nil, fmt.Errorf("direct from cpfp refund tx output value must be less than or equal leaf value, leaf value %d, direct from cpfp refund tx value: %d", leaf.Value, directFromCpfpRefundTx.TxOut[0].Value)
		}

		newDirectNodeSigningJob, err := createSigningJob(ctx, newDirectNodeTx, cpfpNodeTx.TxOut[0], req.DirectNodeTxSigningJob, leaf)
		if err != nil {
			return nil, fmt.Errorf("failed to create new node signing job: %w", err)
		}

		directRefundSigningJob, err := createSigningJob(ctx, newDirectRefundTx, newDirectNodeTx.TxOut[0], req.DirectRefundTxSigningJob, leaf)
		if err != nil {
			return nil, fmt.Errorf("failed to create refund signing job: %w", err)
		}

		newDirectFromCpfpRefundSigningJob, err := createSigningJob(ctx, newDirectFromCpfpRefundTx, newCpfpNodeTx.TxOut[0], req.DirectFromCpfpRefundTxSigningJob, leaf)
		if err != nil {
			return nil, fmt.Errorf("failed to create direct from cpfp refund signing job: %w", err)
		}

		signingJobs = append(signingJobs, newDirectNodeSigningJob, directRefundSigningJob, newDirectFromCpfpRefundSigningJob)
	} else if directNodeSigningJob != nil || directRefundSigningJob != nil || directFromCpfpRefundSigningJob != nil {

		return nil, fmt.Errorf("direct node tx signing job, direct refund tx signing job, and direct from cpfp refund tx signing job must all be provided or none of them")
	} else if requireDirectTx && len(leaf.DirectTx) > 0 {

		return nil, fmt.Errorf("DirectRefundTxSigningJob and DirectFromCpfpRefundTxSigningJob are required. Please upgrade to the latest SDK version")
	}

	treeID, err := leaf.QueryTree().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get tree id: %w", err)
	}
	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
	}
	// Update the nodes in the DB
	// TODO: how to get the tree and keyshare id without a query?
	// TODO: we probably need to sync this state between the SOs
	var directTx, directRefundTx, directFromCpfpRefundTx []byte

	if req.DirectNodeTxSigningJob != nil {
		directTx = req.DirectNodeTxSigningJob.RawTx
	} else if requireDirectTx && len(leaf.DirectTx) > 0 {
		return nil, fmt.Errorf("DirectNodeTxSigningJob is required. Please upgrade to the latest SDK version")
	}

	if req.DirectRefundTxSigningJob != nil {
		directRefundTx = req.DirectRefundTxSigningJob.RawTx
	} else if requireDirectTx && len(leaf.DirectTx) > 0 {
		return nil, fmt.Errorf("DirectRefundTxSigningJob is required. Please upgrade to the latest SDK version")
	}

	if req.DirectFromCpfpRefundTxSigningJob != nil {
		directFromCpfpRefundTx = req.DirectFromCpfpRefundTxSigningJob.RawTx
	} else if requireDirectTx && len(leaf.DirectTx) > 0 {
		return nil, fmt.Errorf("DirectFromCpfpRefundTxSigningJob is required. Please upgrade to the latest SDK version")
	}

	newNode, err := db.
		TreeNode.
		Create().
		SetTreeID(treeID.ID).
		SetStatus(st.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(req.OwnerIdentityPublicKey).
		SetOwnerSigningPubkey(leaf.OwnerSigningPubkey).
		SetValue(leaf.Value).
		SetVerifyingPubkey(leaf.VerifyingPubkey).
		SetSigningKeyshareID(signingKeyshare.ID).
		SetRawTx(req.NodeTxSigningJob.RawTx).
		SetDirectTx(directTx).
		SetRawRefundTx(req.RefundTxSigningJob.RawTx).
		SetDirectRefundTx(directRefundTx).
		SetDirectFromCpfpRefundTx(directFromCpfpRefundTx).
		SetVout(int16(0)).
		SetParentID(leaf.ID).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create new node: %w", err)
	}

	_, err = db.
		TreeNode.
		UpdateOneID(leaf.ID).
		SetStatus(st.TreeNodeStatusSplitLocked).
		SetRawRefundTx(nil).
		SetDirectRefundTx(nil).
		SetDirectFromCpfpRefundTx(nil).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update the node to extend: %w", err)
	}

	// Sign frost
	signingResults, err := helper.SignFrost(ctx, h.config, signingJobs)
	if err != nil {
		return nil, fmt.Errorf("failed to sign frost: %w", err)
	}
	if len(signingResults) != len(signingJobs) {
		return nil, fmt.Errorf("expected %d signing results, got %d", len(signingJobs), len(signingResults))
	}

	cpfpNodeSigningResult, cpfpRefundSigningResult, err := getLeafSigningResults(signingResults, leaf)
	if err != nil {
		return nil, fmt.Errorf("failed to get leaf signing results: %w", err)
	}

	var directNodeSigningResult, directRefundSigningResult, directFromCpfpRefundSigningResult *pb.ExtendLeafSigningResult
	if len(signingJobs) > 2 {
		directNodeSigningResult, directRefundSigningResult, directFromCpfpRefundSigningResult, err = getDirectSigningResults(signingResults, leaf)
		if err != nil {
			return nil, fmt.Errorf("failed to get direct signing results: %w", err)
		}
	}

	return &pb.ExtendLeafResponse{
		LeafId:                              newNode.ID.String(),
		NodeTxSigningResult:                 cpfpNodeSigningResult,
		RefundTxSigningResult:               cpfpRefundSigningResult,
		DirectNodeTxSigningResult:           directNodeSigningResult,
		DirectRefundTxSigningResult:         directRefundSigningResult,
		DirectFromCpfpRefundTxSigningResult: directFromCpfpRefundSigningResult,
	}, nil
}

func getLeafById(ctx context.Context, id string, db *ent.Tx) (*ent.TreeNode, error) {
	leafUUID, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("failed to parse leaf id: %w", err)
	}

	leaf, err := db.TreeNode.
		Query().
		Where(enttreenode.ID(leafUUID)).
		ForUpdate().
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get leaf node: %w", err)
	}

	if leaf.Status != st.TreeNodeStatusAvailable {
		return nil, fmt.Errorf("leaf %s is not available, status: %s", leafUUID, leaf.Status)
	}

	return leaf, nil
}

func validateNewTxSequence(newCpfpNodeTx, newDirectNodeTx, directRefundTx, directFromCpfpRefundTx,
	cpfpRefundTx *wire.MsgTx) error {

	if len(newCpfpNodeTx.TxIn) == 0 {
		return fmt.Errorf("new cpfp node txIn is empty")
	}

	if len(newDirectNodeTx.TxIn) == 0 {
		return fmt.Errorf("new direct node txIn is empty")
	}

	if newCpfpNodeTx.TxIn[0].Sequence >= directRefundTx.TxIn[0].Sequence {
		return fmt.Errorf("new node tx sequence must be less than the Direct refund tx sequence %d, got %d", directRefundTx.TxIn[0].Sequence, newCpfpNodeTx.TxIn[0].Sequence)
	}
	if newCpfpNodeTx.TxIn[0].Sequence >= directFromCpfpRefundTx.TxIn[0].Sequence {
		return fmt.Errorf("new node tx sequence must be less than the Direct from cpfp refund tx sequence %d, got %d", directFromCpfpRefundTx.TxIn[0].Sequence, newCpfpNodeTx.TxIn[0].Sequence)
	}
	if newDirectNodeTx.TxIn[0].Sequence >= cpfpRefundTx.TxIn[0].Sequence {
		return fmt.Errorf("new node tx sequence must be less than the CPFP refund tx sequence %d, got %d", cpfpRefundTx.TxIn[0].Sequence, newCpfpNodeTx.TxIn[0].Sequence)
	}
	if newDirectNodeTx.TxIn[0].Sequence >= directRefundTx.TxIn[0].Sequence {
		return fmt.Errorf("new node tx sequence must be less than the Direct refund tx sequence %d, got %d", directRefundTx.TxIn[0].Sequence, newCpfpNodeTx.TxIn[0].Sequence)
	}
	if newDirectNodeTx.TxIn[0].Sequence >= directFromCpfpRefundTx.TxIn[0].Sequence {
		return fmt.Errorf("new direct node tx sequence must be less than the Direct from cpfp refund tx sequence %d, got %d", directFromCpfpRefundTx.TxIn[0].Sequence, newDirectNodeTx.TxIn[0].Sequence)
	}

	return nil
}

func validateNewTxOutput(leaf *ent.TreeNode,
	newCpfpNodeTx, newDirectNodeTx, cpfpRefundTx, directRefundTx, directFromCpfpRefundTx *wire.MsgTx) error {

	if len(newDirectNodeTx.TxOut) == 0 {
		return fmt.Errorf("new cpfp node tx output is empty")
	}
	if uint64(newCpfpNodeTx.TxOut[0].Value) != leaf.Value {
		return fmt.Errorf("new cpfp node tx output value must match leaf value, expected %d, got %d", leaf.Value, newCpfpNodeTx.TxOut[0].Value)
	}

	if len(newDirectNodeTx.TxOut) == 0 {
		return fmt.Errorf("new direct node tx output is empty")
	}
	if uint64(newDirectNodeTx.TxOut[0].Value) >= leaf.Value {
		return fmt.Errorf("new direct node tx output value must be less than leaf value, leaf value: %d, direct node tx value: %d", leaf.Value, newDirectNodeTx.TxOut[0].Value)
	}

	if len(cpfpRefundTx.TxOut) == 0 {
		return fmt.Errorf("cpfp refund tx output is empty")
	}
	if uint64(cpfpRefundTx.TxOut[0].Value) != leaf.Value {
		return fmt.Errorf("cpfp refund tx output value must match leaf value, expected %d, got %d", leaf.Value, cpfpRefundTx.TxOut[0].Value)
	}

	if len(directRefundTx.TxOut) == 0 {
		return fmt.Errorf("direct refund tx output is empty")
	}
	if uint64(directRefundTx.TxOut[0].Value) >= leaf.Value {
		return fmt.Errorf("direct refund tx output value must be less than leaf value, leaf value %d, direct refund tx value: %d", leaf.Value, cpfpRefundTx.TxOut[0].Value)
	}

	if len(directFromCpfpRefundTx.TxOut) == 0 {
		return fmt.Errorf("direct from cpfp refund tx output is empty")
	}
	if uint64(directFromCpfpRefundTx.TxOut[0].Value) >= leaf.Value {
		return fmt.Errorf("direct from cpfp refund tx output value must be less than leaf value, leaf value %d, direct from cpfp refund tx value: %d", leaf.Value, directFromCpfpRefundTx.TxOut[0].Value)
	}
	return nil
}

func getLeafSigningResults(signingResults []*helper.SigningResult, leaf *ent.TreeNode) (cpfpNodeSigningResult, cpfpRefundSigningResult *pb.ExtendLeafSigningResult, err error) {
	cpfpNodeFrostResult := signingResults[0]
	cpfpRefundFrostResult := signingResults[1]
	verifyingPubkey := leaf.VerifyingPubkey

	// Prepare response
	cpfpNodeSigningResultProto, err := cpfpNodeFrostResult.MarshalProto()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal node signing result: %w", err)
	}

	cpfpNodeSigningResult = &pb.ExtendLeafSigningResult{
		SigningResult: cpfpNodeSigningResultProto,
		VerifyingKey:  verifyingPubkey,
	}

	cpfpRefundSigningResultProto, err := cpfpRefundFrostResult.MarshalProto()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal refund signing result: %w", err)

	}

	cpfpRefundSigningResult = &pb.ExtendLeafSigningResult{
		SigningResult: cpfpRefundSigningResultProto,
		VerifyingKey:  verifyingPubkey,
	}

	return cpfpNodeSigningResult, cpfpRefundSigningResult, nil
}

func getDirectSigningResults(signingResults []*helper.SigningResult,
	leaf *ent.TreeNode) (directNodeSigningResult, directRefundSigningResult, directFromCpfpRefundSigningResult *pb.ExtendLeafSigningResult, err error) {
	directNodeFrostResult := signingResults[2]
	directRefundFrostResult := signingResults[3]
	directFromCpfpRefundFrostResult := signingResults[4]

	directNodeSigningResultProto, err := directNodeFrostResult.MarshalProto()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal node signing result: %w", err)
	}
	directRefundSigningResultProto, err := directRefundFrostResult.MarshalProto()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal refund signing result: %w", err)
	}
	directFromCpfpRefundSigningResultProto, err := directFromCpfpRefundFrostResult.MarshalProto()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal refund signing result: %w", err)
	}

	directNodeSigningResult = &pb.ExtendLeafSigningResult{
		SigningResult: directNodeSigningResultProto,
		VerifyingKey:  leaf.VerifyingPubkey,
	}
	directRefundSigningResult = &pb.ExtendLeafSigningResult{
		SigningResult: directRefundSigningResultProto,
		VerifyingKey:  leaf.VerifyingPubkey,
	}
	directFromCpfpRefundSigningResult = &pb.ExtendLeafSigningResult{
		SigningResult: directFromCpfpRefundSigningResultProto,
		VerifyingKey:  leaf.VerifyingPubkey,
	}

	return directNodeSigningResult, directRefundSigningResult, directFromCpfpRefundSigningResult, nil
}

func createSigningJob(
	ctx context.Context,
	tx *wire.MsgTx,
	parentTxOut *wire.TxOut,
	signingJob *pb.SigningJob,
	leaf *ent.TreeNode,
) (*helper.SigningJob, error) {
	sigHash, err := common.SigHashFromTx(tx, 0, parentTxOut)
	if err != nil {
		return nil, fmt.Errorf("failed to get sig hash for new node tx: %w", err)
	}
	newNodeUserNonceCommitment, err := objects.NewSigningCommitment(signingJob.SigningNonceCommitment.Binding, signingJob.SigningNonceCommitment.Hiding)
	if err != nil {
		return nil, fmt.Errorf("failed to create new node user nonce commitment: %w", err)
	}
	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
	}
	verifyingPubKey, err := keys.ParsePublicKey(leaf.VerifyingPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse verifying public key: %w", err)
	}
	return &helper.SigningJob{
		JobID:             uuid.New().String(),
		SigningKeyshareID: signingKeyshare.ID,
		Message:           sigHash,
		VerifyingKey:      &verifyingPubKey,
		UserCommitment:    newNodeUserNonceCommitment,
	}, nil
}
