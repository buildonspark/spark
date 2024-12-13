package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/entutils"
	"github.com/lightsparkdev/spark-go/so/helper"
)

// SplitHandler is a helper struct to handle the split node request.
type SplitHandler struct{}

func (h *SplitHandler) verifySplitRequest(req *pb.SplitNodeRequest) error {
	// TODO(zhenlu): Implement validation when tree leaf is created.
	return nil
}

func (h *SplitHandler) prepareKeys(ctx context.Context, config *so.Config, req *pb.SplitNodeRequest) (*ent.SigningKeyshare, []*ent.SigningKeyshare, error) {
	nodeID, err := uuid.Parse(req.NodeId)
	if err != nil {
		return nil, nil, err
	}
	err = entutils.MarkNodeAsLocked(ctx, nodeID, schema.TreeNodeStatusSplitLocked)
	if err != nil {
		return nil, nil, err
	}

	keyshares, err := entutils.GetUnusedSigningKeyshares(ctx, config, len(req.Splits)-1)
	if err != nil {
		return nil, nil, err
	}

	keyshareIDs := make([]uuid.UUID, len(keyshares))
	keyshareIDStrings := make([]string, len(keyshares))
	for i, keyshare := range keyshares {
		keyshareIDs[i] = keyshare.ID
		keyshareIDStrings[i] = keyshare.ID.String()
	}

	targetKeyshare, err := entutils.GetNodeKeyshare(ctx, config, nodeID)
	if err != nil {
		return nil, nil, err
	}

	lastKeyshareID := uuid.New()

	operatorSelection := &helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, config, operatorSelection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			return nil, err
		}
		client := pbinternal.NewSparkInternalServiceClient(conn)

		return client.PrepareSplitKeyshares(ctx, &pbinternal.PrepareSplitKeysharesRequest{
			TargetKeyshareId:    targetKeyshare.ID.String(),
			SelectedKeyshareIds: keyshareIDStrings,
			LastKeyshareId:      lastKeyshareID.String(),
		})
	})
	if err != nil {
		return nil, nil, err
	}

	err = entutils.MarkSigningKeysharesAsUsed(ctx, config, keyshareIDs)
	if err != nil {
		return nil, nil, err
	}

	lastKeyshare, err := entutils.CalculateAndStoreLastKey(ctx, config, targetKeyshare, keyshares, lastKeyshareID)
	if err != nil {
		return nil, nil, err
	}

	keyshares = append(keyshares, lastKeyshare)
	return targetKeyshare, keyshares, nil
}

func (h *SplitHandler) prepareSplitResults(ctx context.Context, config *so.Config, req *pb.SplitNodeRequest, keyshares []*ent.SigningKeyshare) ([]*pb.SplitResult, error) {
	splitResults := make([]*pb.SplitResult, 0)
	for i, split := range req.Splits {
		verifyingKey, err := common.AddPublicKeys(split.SigningPublicKey, keyshares[i].PublicKey)
		if err != nil {
			return nil, err
		}
		splitResults = append(splitResults, &pb.SplitResult{
			NodeId:        uuid.New().String(),
			VerifyingKey:  verifyingKey,
			UserPublicKey: split.SigningPublicKey,
		})
	}
	return splitResults, nil
}

func (h *SplitHandler) prepareSigningJobs(
	ctx context.Context,
	config *so.Config,
	req *pb.SplitNodeRequest,
	parentKeyshare *ent.SigningKeyshare,
	childrenKeyshares []*ent.SigningKeyshare,
) ([]*helper.SigningJob, error) {
	signingJobs := make([]*helper.SigningJob, 0)
	nodeID, err := uuid.Parse(req.NodeId)
	if err != nil {
		return nil, err
	}
	db := common.GetDbFromContext(ctx)
	node, err := db.TreeNode.Get(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	parentTx, err := common.TxFromRawTxBytes(node.RawTx)
	if err != nil {
		return nil, err
	}

	parentTxSigningJob, splitTx, err := helper.NewSigningJob(parentKeyshare, req.ParentTxSigningJob, parentTx.TxOut[node.Vout])
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, parentTxSigningJob)

	for i, split := range req.Splits {
		refundSigningJob, _, err := helper.NewSigningJob(childrenKeyshares[i], split.RefundSigningJob, splitTx.TxOut[split.Vout])
		if err != nil {
			return nil, err
		}
		signingJobs = append(signingJobs, refundSigningJob)

		verifyingKey, err := common.AddPublicKeys(split.SigningPublicKey, childrenKeyshares[i].PublicKey)
		if err != nil {
			return nil, err
		}

		db.TreeNode.
			Create().
			SetTree(node.Edges.Tree).
			SetStatus(schema.TreeNodeStatusCreating).
			SetOwnerIdentityPubkey(split.SigningPublicKey).
			SetOwnerSigningPubkey(split.SigningPublicKey).
			SetValue(uint64(split.Value)).
			SetVerifyingPubkey(verifyingKey).
			SetSigningKeyshare(childrenKeyshares[i]).
			SetRawTx(req.ParentTxSigningJob.RawTx).
			SetVout(uint16(split.Vout)).
			SetRawRefundTx(split.RefundSigningJob.RawTx).
			SaveX(ctx)
	}
	return signingJobs, nil
}

func (h *SplitHandler) finalizeSplitResults(ctx context.Context, config *so.Config, req *pb.SplitNodeRequest, signingResults []*helper.SigningResult, splitResults []*pb.SplitResult) ([]*pb.SplitResult, error) {
	if len(signingResults) != 2*len(splitResults) {
		return nil, fmt.Errorf("number of signing results does not match number of split results")
	}

	for i, splitResult := range splitResults {
		refundTxIndex := i + 1
		refundTxSigningCommitments, err := common.ConvertObjectMapToProtoMap(signingResults[refundTxIndex].SigningCommitments)
		if err != nil {
			return nil, err
		}
		splitResult.RefundTxSigningResult = &pb.SigningResult{
			PublicKeys:              signingResults[refundTxIndex].PublicKeys,
			SigningNonceCommitments: refundTxSigningCommitments,
			SignatureShares:         signingResults[refundTxIndex].SignatureShares,
		}
	}

	return splitResults, nil
}

// SplitNode handles the split node request.
func (h *SplitHandler) SplitNode(ctx context.Context, config *so.Config, req *pb.SplitNodeRequest) (*pb.SplitNodeResponse, error) {
	if err := h.verifySplitRequest(req); err != nil {
		return nil, err
	}

	parentKeyshare, childrenKeyshares, err := h.prepareKeys(ctx, config, req)
	if err != nil {
		return nil, err
	}

	splitResults, err := h.prepareSplitResults(ctx, config, req, childrenKeyshares)
	if err != nil {
		return nil, err
	}

	signingJobs, err := h.prepareSigningJobs(ctx, config, req, parentKeyshare, childrenKeyshares)
	if err != nil {
		return nil, err
	}

	signingResults, err := helper.SignFrost(ctx, config, signingJobs)
	if err != nil {
		return nil, err
	}

	splitResults, err = h.finalizeSplitResults(ctx, config, req, signingResults, splitResults)
	if err != nil {
		return nil, err
	}

	return &pb.SplitNodeResponse{
		ParentNodeId: req.NodeId,
		SplitResults: splitResults,
	}, nil
}
