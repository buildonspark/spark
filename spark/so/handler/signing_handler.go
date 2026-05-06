package handler

import (
	"context"
	errs "errors"
	"fmt"
	"strings"

	"github.com/lightsparkdev/spark/common/collections"
	"github.com/lightsparkdev/spark/common/uuids"
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
	DefaultMaxSigningCommitmentNodes = 1000
	DefaultMaxSigningCommitmentCount = 10
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

	if len(req.NodeIds) > 0 && req.NodeIdCount != 0 {
		return nil, errs.New("both node_ids and node_id_count were set, but they are mutually exclusive. Provide one or the other")
	}

	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	nodeIDs, err := uuids.ParseSlice(req.GetNodeIds())
	if err != nil {
		return nil, fmt.Errorf("unable to parse node id: %w", err)
	}

	knobsService := knobs.GetKnobsService(ctx)
	maxNodeIDs := int(knobsService.GetValue(knobs.KnobSoSigningCommitmentNodeLimit, DefaultMaxSigningCommitmentNodes))

	maxSigningCommitmentCount := uint32(knobsService.GetValue(knobs.KnobSoSigningCommitmentCountLimit, DefaultMaxSigningCommitmentCount))
	signingCommitmentCount := req.Count
	if signingCommitmentCount == 0 {
		signingCommitmentCount = 1
	}

	if signingCommitmentCount > maxSigningCommitmentCount {
		return nil, errors.InvalidArgumentOutOfRange(fmt.Errorf("number of signing commitments provided was %d, but the maximum is %d", signingCommitmentCount, maxSigningCommitmentCount))
	}

	var keyshareIDcount uint32
	if len(nodeIDs) > 0 {
		if len(nodeIDs) > maxNodeIDs {
			return nil, errors.InvalidArgumentOutOfRange(fmt.Errorf("there were %d node ids provided, but the max is %d", len(nodeIDs), maxNodeIDs))
		}

		nodes, err := tx.TreeNode.Query().WithSigningKeyshare().Where(treenode.IDIn(nodeIDs...)).All(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get nodes: %w", err)
		}

		if err := h.validateNodeOwnership(ctx, nodes); err != nil {
			return nil, err
		}

		keyshareIDcount = uint32(len(nodes))
	} else {
		if req.NodeIdCount > uint32(maxNodeIDs) {
			return nil, errors.InvalidArgumentOutOfRange(fmt.Errorf("node ID count provided was %d, but the max is %d", req.NodeIdCount, maxNodeIDs))
		}
		keyshareIDcount = req.NodeIdCount
	}

	if keyshareIDcount == 0 {
		return &pb.GetSigningCommitmentsResponse{}, nil
	}

	commitments, err := helper.GetSigningCommitments(ctx, h.config, keyshareIDcount, signingCommitmentCount)
	if err != nil {
		return nil, fmt.Errorf("unable to get signing commitments: %w", err)
	}

	commitmentsArray := collections.MapOfArrayToArrayOfMap(commitments)

	requestedCommitments := make([]*pb.RequestedSigningCommitments, len(commitmentsArray))

	for i, commitment := range commitmentsArray {
		commitmentMapProto, err := collections.ConvertObjectMapToProtoMap(commitment)
		if err != nil {
			return nil, fmt.Errorf("unable to convert signing commitment to proto: %w", err)
		}
		requestedCommitments[i] = &pb.RequestedSigningCommitments{
			SigningNonceCommitments: commitmentMapProto,
		}
	}

	return &pb.GetSigningCommitmentsResponse{SigningCommitments: requestedCommitments}, nil
}
