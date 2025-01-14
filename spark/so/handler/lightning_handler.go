package handler

import (
	"context"
	"fmt"
	"math/big"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	secretsharing "github.com/lightsparkdev/spark-go/common/secret_sharing"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/treenode"
	"github.com/lightsparkdev/spark-go/so/helper"
	"github.com/lightsparkdev/spark-go/so/objects"
)

// LightningHandler is the handler for the lightning service.
type LightningHandler struct {
	config *so.Config
}

// NewLightningHandler returns a new LightningHandler.
func NewLightningHandler(config *so.Config) *LightningHandler {
	return &LightningHandler{config: config}
}

// StorePreimageShare stores the preimage share for the given payment hash.
func (h *LightningHandler) StorePreimageShare(ctx context.Context, req *pb.StorePreimageShareRequest) error {
	err := secretsharing.ValidateShare(
		&secretsharing.VerifiableSecretShare{
			SecretShare: secretsharing.SecretShare{
				FieldModulus: secp256k1.S256().N,
				Threshold:    int(h.config.Threshold),
				Index:        big.NewInt(int64(h.config.Index + 1)),
				Share:        new(big.Int).SetBytes(req.PreimageShare.SecretShare),
			},
			Proofs: req.PreimageShare.Proofs,
		},
	)
	if err != nil {
		return fmt.Errorf("unable to validate share: %v", err)
	}

	db := ent.GetDbFromContext(ctx)
	_, err = db.PreimageShare.Create().
		SetPaymentHash(req.PaymentHash).
		SetPreimageShare(req.PreimageShare.SecretShare).
		SetThreshold(req.Threshold).
		SetOwnerIdentityPubkey(req.UserIdentityPublicKey).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to store preimage share: %v", err)
	}
	return nil
}

// GetSigningCommitments gets the signing commitments for the given node ids.
func (h *LightningHandler) GetSigningCommitments(ctx context.Context, req *pb.GetSigningCommitmentsRequest) (*pb.GetSigningCommitmentsResponse, error) {
	db := ent.GetDbFromContext(ctx)
	nodeIds := make([]uuid.UUID, len(req.NodeIds))
	for i, nodeID := range req.NodeIds {
		nodeID, err := uuid.Parse(nodeID)
		if err != nil {
			return nil, fmt.Errorf("unable to parse node id: %v", err)
		}
		nodeIds[i] = nodeID
	}
	nodes, err := db.TreeNode.Query().Where(treenode.IDIn(nodeIds...)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get nodes: %v", err)
	}

	keyshareIDs := make([]uuid.UUID, len(nodes))
	for i, node := range nodes {
		keyshareIDs[i], err = node.QuerySigningKeyshare().OnlyID(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get keyshare id: %v", err)
		}
	}

	commitments, err := helper.GetSigningCommitments(ctx, h.config, keyshareIDs)
	if err != nil {
		return nil, fmt.Errorf("unable to get signing commitments: %v", err)
	}

	commitmentsArray := common.MapOfArrayToArrayOfMap[string, objects.SigningCommitment](commitments)

	requestedCommitments := make([]*pb.RequestedSigningCommitments, len(commitmentsArray))

	for i, commitment := range commitmentsArray {
		commitmentMapProto, err := common.ConvertObjectMapToProtoMap(commitment)
		if err != nil {
			return nil, fmt.Errorf("unable to convert signing commitment to proto: %v", err)
		}
		requestedCommitments[i] = &pb.RequestedSigningCommitments{
			SigningNonceCommitments: commitmentMapProto,
		}
	}

	return &pb.GetSigningCommitmentsResponse{SigningCommitments: requestedCommitments}, nil
}
