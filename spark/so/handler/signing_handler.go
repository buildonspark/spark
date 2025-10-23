package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
)

const (
	// Private constants to avoid conflicts with lightning_handler.go
	defaultMaxSigningCommitmentNodes = 1000
	defaultMaxSigningCommitmentCount = 10
)

// The SigningHandler is responsible for handling signing commitment related requests.
type SigningHandler struct {
	config *so.Config
}

// NewSigningHandler creates a new SigningHandler.
func NewSigningHandler(config *so.Config) *SigningHandler {
	return &SigningHandler{
		config: config,
	}
}

// validateHasSession validates that the context has a valid session when authz is enforced.
func (h *SigningHandler) validateHasSession(ctx context.Context) error {
	if h.config.IsAuthzEnforced() {
		_, err := authn.GetSessionFromContext(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}

// validateNodeOwnership validates that all nodes are owned by the authenticated identity.
func (h *SigningHandler) validateNodeOwnership(ctx context.Context, nodes []*ent.TreeNode) error {
	if !h.config.IsAuthzEnforced() {
		return nil
	}
	session, err := authn.GetSessionFromContext(ctx)
	if err != nil {
		return err
	}
	sessionIdentityPubkeyBytes := session.IdentityPublicKey().Serialize()
	var mismatchedNodes []string
	for _, node := range nodes {
		if !node.OwnerIdentityPubkey.Equals(session.IdentityPublicKey()) {
			mismatchedNodes = append(mismatchedNodes, node.ID.String())
		}
	}
	if len(mismatchedNodes) > 0 {
		return &authz.Error{
			Code: authz.ErrorCodeIdentityMismatch,
			Message: fmt.Sprintf("nodes [%s] are not owned by the authenticated identity public key %x",
				strings.Join(mismatchedNodes, ", "),
				sessionIdentityPubkeyBytes),
			Cause: nil,
		}
	}
	return nil
}

// GetSigningCommitments gets the signing commitments for the given node ids.
func (h *SigningHandler) GetSigningCommitments(ctx context.Context, req *pb.GetSigningCommitmentsRequest) (*pb.GetSigningCommitmentsResponse, error) {
	if err := h.validateHasSession(ctx); err != nil {
		return nil, err
	}

	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	nodeIDs := make([]uuid.UUID, len(req.NodeIds))
	for i, nodeID := range req.NodeIds {
		nodeID, err := uuid.Parse(nodeID)
		if err != nil {
			return nil, fmt.Errorf("unable to parse node id: %w", err)
		}
		nodeIDs[i] = nodeID
	}

	knobsService := knobs.GetKnobsService(ctx)

	maxNodeIDs := int(knobsService.GetValue(
		knobs.KnobSoSigningCommitmentNodeLimit,
		defaultMaxSigningCommitmentNodes,
	))

	if len(nodeIDs) > maxNodeIDs {
		return nil, errors.InvalidArgumentOutOfRange(fmt.Errorf("too many node ids: %d", len(nodeIDs)))
	}

	maxCount := uint32(knobsService.GetValue(knobs.KnobSoSigningCommitmentCountLimit, defaultMaxSigningCommitmentCount))
	count := req.Count
	if count == 0 {
		count = 1
	}

	if count > maxCount {
		return nil, errors.InvalidArgumentOutOfRange(fmt.Errorf("count too large: %d", count))
	}

	nodes, err := tx.TreeNode.Query().WithSigningKeyshare().Where(treenode.IDIn(nodeIDs...)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get nodes: %w", err)
	}

	if err := h.validateNodeOwnership(ctx, nodes); err != nil {
		return nil, err
	}

	keyshareIDs := make([]uuid.UUID, len(nodes))
	for i, node := range nodes {
		if node.Edges.SigningKeyshare == nil {
			return nil, fmt.Errorf("node %s has no keyshare", node.ID)
		}
		keyshareIDs[i] = node.Edges.SigningKeyshare.ID
	}

	commitments, err := helper.GetSigningCommitments(ctx, h.config, keyshareIDs, count)
	if err != nil {
		return nil, fmt.Errorf("unable to get signing commitments: %w", err)
	}

	commitmentsArray := common.MapOfArrayToArrayOfMap(commitments)

	requestedCommitments := make([]*pb.RequestedSigningCommitments, len(commitmentsArray))

	for i, commitment := range commitmentsArray {
		commitmentMapProto, err := common.ConvertObjectMapToProtoMap(commitment)
		if err != nil {
			return nil, fmt.Errorf("unable to convert signing commitment to proto: %w", err)
		}
		requestedCommitments[i] = &pb.RequestedSigningCommitments{
			SigningNonceCommitments: commitmentMapProto,
		}
	}

	return &pb.GetSigningCommitmentsResponse{SigningCommitments: requestedCommitments}, nil
}
