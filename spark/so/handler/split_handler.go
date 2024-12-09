package handler

import (
	"context"
	"fmt"

	"github.com/btcsuite/btcd/wire"
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

func (h *SplitHandler) prepareKeys(ctx context.Context, config *so.Config, req *pb.SplitNodeRequest) ([]*ent.SigningKeyshare, error) {
	nodeID, err := uuid.Parse(req.NodeId)
	if err != nil {
		return nil, err
	}
	err = entutils.MarkNodeAsLocked(ctx, nodeID, schema.TreeNodeStatusSplitLocked)
	if err != nil {
		return nil, err
	}

	keyshares, err := entutils.GetUnusedSigningKeyshares(ctx, config, len(req.Splits)-1)
	if err != nil {
		return nil, err
	}

	keyshareIDs := make([]uuid.UUID, len(keyshares))
	keyshareIDStrings := make([]string, len(keyshares))
	for i, keyshare := range keyshares {
		keyshareIDs[i] = keyshare.ID
		keyshareIDStrings[i] = keyshare.ID.String()
	}

	targetKeyshare, err := entutils.GetNodeKeyshare(ctx, config, nodeID)
	if err != nil {
		return nil, err
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
		return nil, err
	}

	err = entutils.MarkSigningKeysharesAsUsed(ctx, config, keyshareIDs)
	if err != nil {
		return nil, err
	}

	lastKeyshare, err := entutils.CalculateAndStoreLastKey(ctx, config, targetKeyshare, keyshares, lastKeyshareID)
	if err != nil {
		return nil, err
	}

	keyshares = append(keyshares, lastKeyshare)
	return keyshares, nil
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

func prepareSigningJob(split *pb.Split, keyshare *ent.SigningKeyshare, prevOutput *wire.TxOut) (*helper.SigningJob, *helper.SigningJob, error) {
	nodeSigningJob, nodeTx, err := helper.NewSigningJob(keyshare, split.NodeSigningJob, prevOutput)
	if err != nil {
		return nil, nil, err
	}
	refundSigningJob, _, err := helper.NewSigningJob(keyshare, split.RefundSigningJob, nodeTx.TxOut[0])
	if err != nil {
		return nil, nil, err
	}
	return nodeSigningJob, refundSigningJob, nil
}

func (h *SplitHandler) prepareSigningJobs(ctx context.Context, config *so.Config, req *pb.SplitNodeRequest, keyshares []*ent.SigningKeyshare) ([]*helper.SigningJob, error) {
	signingJobs := make([]*helper.SigningJob, 0)
	for i, split := range req.Splits {
		// TODO(zhenlu): Use the previous output of the parent node after #72 is merged.
		nodeTxSigningJob, refundTxSigningJob, err := prepareSigningJob(split, keyshares[i], nil)
		if err != nil {
			return nil, err
		}
		signingJobs = append(signingJobs, nodeTxSigningJob, refundTxSigningJob)
	}
	return signingJobs, nil
}

func (h *SplitHandler) finalizeSplitResults(ctx context.Context, config *so.Config, req *pb.SplitNodeRequest, signingResults []*helper.SigningResult, splitResults []*pb.SplitResult) ([]*pb.SplitResult, error) {
	if len(signingResults) != 2*len(splitResults) {
		return nil, fmt.Errorf("number of signing results does not match number of split results")
	}

	for i, splitResult := range splitResults {
		nodeTxIndex := i * 2
		refundTxIndex := i*2 + 1
		nodeTxSigningCommitments, err := common.ConvertObjectMapToProtoMap(signingResults[nodeTxIndex].SigningCommitments)
		if err != nil {
			return nil, err
		}
		refundTxSigningCommitments, err := common.ConvertObjectMapToProtoMap(signingResults[refundTxIndex].SigningCommitments)
		if err != nil {
			return nil, err
		}
		splitResult.NodeSignatures = &pb.NodeSignatureShares{
			NodeId: splitResult.NodeId,
			NodeTxSigningResult: &pb.SigningResult{
				PublicKeys:              signingResults[nodeTxIndex].PublicKeys,
				SigningNonceCommitments: nodeTxSigningCommitments,
				SignatureShares:         signingResults[nodeTxIndex].SignatureShares,
			},
			RefundTxSigningResult: &pb.SigningResult{
				PublicKeys:              signingResults[refundTxIndex].PublicKeys,
				SigningNonceCommitments: refundTxSigningCommitments,
				SignatureShares:         signingResults[refundTxIndex].SignatureShares,
			},
		}
	}

	return splitResults, nil
}

// SplitNode handles the split node request.
func (h *SplitHandler) SplitNode(ctx context.Context, config *so.Config, req *pb.SplitNodeRequest) (*pb.SplitNodeResponse, error) {
	if err := h.verifySplitRequest(req); err != nil {
		return nil, err
	}

	keyshares, err := h.prepareKeys(ctx, config, req)
	if err != nil {
		return nil, err
	}

	splitResults, err := h.prepareSplitResults(ctx, config, req, keyshares)
	if err != nil {
		return nil, err
	}

	signingJobs, err := h.prepareSigningJobs(ctx, config, req, keyshares)
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
		SplitResults: splitResults,
	}, nil
}
