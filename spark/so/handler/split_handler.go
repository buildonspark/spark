package handler

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/depositaddress"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/helper"
)

// SplitHandler is a helper struct to handle the split node request.
type SplitHandler struct{}

func (h *SplitHandler) verifyPrepareSplitAddressRequest(req *pb.PrepareSplitAddressRequest) error {
	// TODO(zhenlu): Implement validation for sum of the split values and proof of posession on pubkeys.
	return nil
}

func (h *SplitHandler) verifySplitRequest(req *pb.SplitNodeRequest) error {
	// TODO(zhenlu): Implement validation when tree leaf is created.
	return nil
}

func (h *SplitHandler) prepareSplitAddress(
	ctx context.Context,
	config *so.Config,
	req *pb.PrepareSplitAddressRequest,
) (*pb.PrepareSplitAddressResponse, error) {
	nodeID, err := uuid.Parse(req.NodeId)
	if err != nil {
		return nil, err
	}
	node, err := ent.GetDbFromContext(ctx).TreeNode.Get(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	err = ent.MarkNodeAsLocked(ctx, nodeID, schema.TreeNodeStatusSplitLocked)
	if err != nil {
		return nil, err
	}

	keyshares, err := ent.GetUnusedSigningKeyshares(ctx, config, len(req.SigningPublicKeys)-1)
	if err != nil {
		return nil, err
	}

	keyshareIDs := make([]uuid.UUID, len(keyshares))
	keyshareIDStrings := make([]string, len(keyshares))
	for i, keyshare := range keyshares {
		keyshareIDs[i] = keyshare.ID
		keyshareIDStrings[i] = keyshare.ID.String()
	}

	targetKeyshare, err := node.QuerySigningKeyshare().First(ctx)
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
			NodeId:              nodeID.String(),
			TargetKeyshareId:    targetKeyshare.ID.String(),
			SelectedKeyshareIds: keyshareIDStrings,
			LastKeyshareId:      lastKeyshareID.String(),
		})
	})
	if err != nil {
		return nil, err
	}

	lastKeyshare, err := ent.CalculateAndStoreLastKey(ctx, config, targetKeyshare, keyshares, lastKeyshareID)
	if err != nil {
		return nil, err
	}

	keyshares = append(keyshares, lastKeyshare)

	depositAddresses := make([]*pb.Address, 0)
	for i, keyshare := range keyshares {
		userPublicKey := req.SigningPublicKeys[i]
		combinedPublicKey, err := common.AddPublicKeys(keyshare.PublicKey, userPublicKey)
		if err != nil {
			return nil, err
		}
		depositAddress, err := common.P2TRAddressFromPublicKey(combinedPublicKey, config.Network)
		if err != nil {
			log.Printf("failed to get p2tr address: %v", err)
			return nil, err
		}
		depositAddresses = append(depositAddresses, &pb.Address{
			Address:      *depositAddress,
			VerifyingKey: combinedPublicKey,
		})

		ent.GetDbFromContext(ctx).DepositAddress.Create().
			SetAddress(*depositAddress).
			SetOwnerIdentityPubkey(node.OwnerIdentityPubkey).
			SetOwnerSigningPubkey(userPublicKey).
			SetSigningKeyshareID(keyshare.ID).
			SaveX(ctx)
	}

	err = ent.MarkSigningKeysharesAsUsed(ctx, config, keyshareIDs)
	if err != nil {
		return nil, err
	}

	return &pb.PrepareSplitAddressResponse{
		Addresses: depositAddresses,
	}, nil
}

// PrepareSplitAddress is the entrypoint for the prepare split address request.
func (h *SplitHandler) PrepareSplitAddress(ctx context.Context, config *so.Config, req *pb.PrepareSplitAddressRequest) (*pb.PrepareSplitAddressResponse, error) {
	if err := h.verifyPrepareSplitAddressRequest(req); err != nil {
		return nil, err
	}

	return h.prepareSplitAddress(ctx, config, req)
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
) ([]*helper.SigningJob, []*ent.TreeNode, error) {
	signingJobs := make([]*helper.SigningJob, 0)
	nodeID, err := uuid.Parse(req.NodeId)
	if err != nil {
		return nil, nil, err
	}
	db := ent.GetDbFromContext(ctx)
	node, err := db.TreeNode.Get(ctx, nodeID)
	if err != nil {
		return nil, nil, err
	}
	parentTx, err := common.TxFromRawTxBytes(node.RawTx)
	if err != nil {
		return nil, nil, err
	}

	parentTxSigningJob, splitTx, err := helper.NewSigningJob(parentKeyshare, req.ParentTxSigningJob, parentTx.TxOut[node.Vout])
	if err != nil {
		return nil, nil, err
	}
	signingJobs = append(signingJobs, parentTxSigningJob)

	nodes := make([]*ent.TreeNode, 0)
	for i, split := range req.Splits {
		refundSigningJob, _, err := helper.NewSigningJob(childrenKeyshares[i], split.RefundSigningJob, splitTx.TxOut[split.Vout])
		if err != nil {
			return nil, nil, err
		}
		signingJobs = append(signingJobs, refundSigningJob)

		verifyingKey, err := common.AddPublicKeys(split.SigningPublicKey, childrenKeyshares[i].PublicKey)
		if err != nil {
			return nil, nil, err
		}

		tree, err := node.QueryTree().First(ctx)
		if err != nil {
			return nil, nil, err
		}

		node, err := db.TreeNode.
			Create().
			SetTree(tree).
			SetParentID(nodeID).
			SetStatus(schema.TreeNodeStatusCreating).
			SetOwnerIdentityPubkey(node.OwnerIdentityPubkey).
			SetOwnerSigningPubkey(split.SigningPublicKey).
			SetValue(uint64(split.Value)).
			SetVerifyingPubkey(verifyingKey).
			SetSigningKeyshare(childrenKeyshares[i]).
			SetRawTx(req.ParentTxSigningJob.RawTx).
			SetVout(uint16(split.Vout)).
			SetRawRefundTx(split.RefundSigningJob.RawTx).
			Save(ctx)
		if err != nil {
			return nil, nil, err
		}
		nodes = append(nodes, node)
	}
	return signingJobs, nodes, nil
}

func (h *SplitHandler) finalizeSplitResults(ctx context.Context, config *so.Config, req *pb.SplitNodeRequest, signingResults []*helper.SigningResult, nodes []*ent.TreeNode, splitResults []*pb.SplitResult) ([]*pb.SplitResult, error) {
	if len(signingResults) != len(splitResults)+1 {
		return nil, fmt.Errorf("number of signing results does not match number of split results")
	}

	for i, splitResult := range splitResults {
		refundTxIndex := i + 1
		splitResult.NodeId = nodes[i].ID.String()
		var err error
		splitResult.RefundTxSigningResult, err = signingResults[refundTxIndex].MarshalProto()
		if err != nil {
			return nil, err
		}
	}

	return splitResults, nil
}

func (h *SplitHandler) prepareKeys(ctx context.Context, config *so.Config, req *pb.SplitNodeRequest) (*ent.SigningKeyshare, []*ent.SigningKeyshare, error) {
	nodeID, err := uuid.Parse(req.NodeId)
	if err != nil {
		return nil, nil, err
	}
	node, err := ent.GetDbFromContext(ctx).TreeNode.Get(ctx, nodeID)
	if err != nil {
		return nil, nil, err
	}
	parentKeyshare, err := node.QuerySigningKeyshare().First(ctx)
	if err != nil {
		return nil, nil, err
	}
	childrenKeyshares := make([]*ent.SigningKeyshare, 0)
	for _, split := range req.Splits {
		depositAddress, err := ent.GetDbFromContext(ctx).DepositAddress.Query().Where(depositaddress.OwnerSigningPubkey(split.SigningPublicKey)).First(ctx)
		if err != nil {
			return nil, nil, err
		}
		keyshare, err := depositAddress.QuerySigningKeyshare().First(ctx)
		if err != nil {
			return nil, nil, err
		}
		childrenKeyshares = append(childrenKeyshares, keyshare)
	}
	return parentKeyshare, childrenKeyshares, nil
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

	signingJobs, nodes, err := h.prepareSigningJobs(ctx, config, req, parentKeyshare, childrenKeyshares)
	if err != nil {
		return nil, err
	}

	signingResults, err := helper.SignFrost(ctx, config, signingJobs)
	if err != nil {
		return nil, err
	}

	parentSigningResult, err := signingResults[0].MarshalProto()
	if err != nil {
		return nil, err
	}

	splitResults, err = h.finalizeSplitResults(ctx, config, req, signingResults, nodes, splitResults)
	if err != nil {
		return nil, err
	}

	return &pb.SplitNodeResponse{
		ParentNodeId:          req.NodeId,
		ParentTxSigningResult: parentSigningResult,
		SplitResults:          splitResults,
	}, nil
}
