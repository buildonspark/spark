package tree

import (
	"context"
	"fmt"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"go.uber.org/zap"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark_tree"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/treenode"
)

// PolarityScoreDepth is the depth of the tree to consider for the polarity score.
const PolarityScoreDepth = 5

// PolarityScoreAlpha is the prior probability of a user being online and swapping.
const PolarityScoreAlpha = 0.1

// PolarityScoreGamma is the exponential decay for leaves that are more distant from the candidate.
const PolarityScoreGamma = 0.5

type Scorer interface {
	Score(leafID uuid.UUID, sspPublicKey keys.Public, userPublicKey keys.Public) float32
	FetchPolarityScores(req *pb.FetchPolarityScoreRequest, stream pb.SparkTreeService_FetchPolarityScoresServer) error
}

type PolarityScorer struct {
	logger             *zap.Logger
	dbClient           *ent.Client
	probPubKeyCanClaim map[uuid.UUID]map[keys.Public]float32
}

func NewPolarityScorer(logger *zap.Logger, dbClient *ent.Client) *PolarityScorer {
	return &PolarityScorer{
		logger:             logger.With(zap.String("component", "polarity")),
		dbClient:           dbClient,
		probPubKeyCanClaim: make(map[uuid.UUID]map[keys.Public]float32),
	}
}

func (s *PolarityScorer) Start(ctx context.Context) {
	const limit = 1000
	lastUpdated := time.Now().Add(-30 * 24 * time.Hour)
	for {
		s.logger.Sugar().Infof("Checking for leaves updated after %s", lastUpdated.Format(time.RFC3339))
		leaves, err := s.dbClient.TreeNode.Query().
			Where(
				treenode.StatusEQ(st.TreeNodeStatusAvailable),
				treenode.UpdateTimeGTE(lastUpdated),
			).
			Order(ent.Desc(treenode.FieldUpdateTime)).
			WithParent().
			Limit(limit).
			All(ctx)
		if err != nil {
			s.logger.Error("Error loading leaves", zap.Error(err))
		}

		s.logger.Sugar().Infof("Found %d leaves to update", len(leaves))
		for _, leaf := range leaves {
			node := leaf
			for i := 0; i < PolarityScoreDepth; i++ {
				if node.Edges.Parent == nil {
					break
				}

				parentNode, err := s.dbClient.TreeNode.Query().
					Where(treenode.ID(node.Edges.Parent.ID)).
					WithParent().
					Only(ctx)
				if err != nil {
					s.logger.Error("Error loading parent", zap.Error(err))
					break
				}
				node = parentNode
			}
			if node != nil {
				s.UpdateLeaves(ctx, node)
			} else {
				s.logger.Error("Node is nil")
			}
		}

		if len(leaves) > 0 {
			// Update lastUpdated to the most recent leaf's update time
			lastUpdated = leaves[0].UpdateTime
		}

		if len(leaves) == limit {
			time.Sleep(1 * time.Millisecond)
		} else {
			// Done for now, sleep for a while.
			time.Sleep(60 * time.Second)
		}
	}
}

// UpdateLeaves updates the polarity score for all the leaves under the given node.
func (s *PolarityScorer) UpdateLeaves(ctx context.Context, node *ent.TreeNode) {
	// Build the helper tree starting from the given node
	helperTree, err := buildHelperTree(ctx, node)
	if err != nil {
		s.logger.Error("Error building helper tree", zap.Error(err))
		return
	}
	leaves := helperTree.Leaves()
	s.logger.Sugar().Infof("Helper tree (root: %s, leaves: %d)", node.ID, len(leaves))
	for _, leaf := range leaves {
		if _, ok := s.probPubKeyCanClaim[leaf.leafID]; !ok {
			s.probPubKeyCanClaim[leaf.leafID] = make(map[keys.Public]float32)
		}
		for owner, score := range leaf.Score() {
			s.probPubKeyCanClaim[leaf.leafID][owner] = score
		}
	}
}

// buildHelperTree recursively builds the helper tree.
func buildHelperTree(ctx context.Context, n *ent.TreeNode) (*HelperNode, error) {
	pubKey, err := keys.ParsePublicKey(n.OwnerIdentityPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key while building helper tree: %w", err)
	}
	helperNode := NewHelperNode(pubKey, n.ID)

	// Load and process all children
	children, err := n.QueryChildren().Where().All(ctx)
	if err != nil {
		return helperNode, nil
	}

	for _, child := range children {
		childHelper, err := buildHelperTree(ctx, child)
		if err != nil {
			return nil, err
		}
		childHelper.parent = helperNode
		helperNode.children = append(helperNode.children, childHelper)
	}

	return helperNode, nil
}

// Score computes a measure of how much the SSP wants the leaf vs giving it to the user.
func (s *PolarityScorer) Score(leafID uuid.UUID, sspPublicKey keys.Public, userPublicKey keys.Public) float32 {
	// Check if leaf exists in the map
	leafScores, exists := s.probPubKeyCanClaim[leafID]
	if !exists {
		return 0
	}

	// Get probabilities, defaulting to 0 if pubkey not found
	probSspCanClaim := leafScores[sspPublicKey]
	probUserCanClaim := leafScores[userPublicKey]

	return probSspCanClaim - probUserCanClaim
}

func (s *PolarityScorer) FetchPolarityScores(req *pb.FetchPolarityScoreRequest, stream pb.SparkTreeService_FetchPolarityScoresServer) error {
	// TODO(mhr): Add stream log interceptor.
	logger := logging.GetLoggerFromContext(stream.Context()).With(zap.String("method", "/spark.SparkTreeService/FetchPolarityScores"))

	targetPubKeys := make(map[keys.Public]bool)
	for _, pubKeyBytes := range req.PublicKeys {
		pubKey, err := keys.ParsePublicKey(pubKeyBytes)
		if err != nil {
			return err
		}
		targetPubKeys[pubKey] = true
	}
	if len(targetPubKeys) > 0 {
		logger.Sugar().Infof("Fetching polarity scores for %d pubkeys", len(targetPubKeys))
	} else {
		logger.Info("Fetching all polarity scores")
	}

	logger.Sugar().Infof("Loading cache (num leaves: %d)", len(s.probPubKeyCanClaim))
	for leafID, leafScores := range s.probPubKeyCanClaim {
		for pubKey, score := range leafScores {
			if len(targetPubKeys) > 0 && !targetPubKeys[pubKey] {
				continue
			}
			err := stream.Send(&pb.PolarityScore{
				LeafId:    leafID.String(),
				PublicKey: pubKey.Serialize(),
				Score:     score,
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}
