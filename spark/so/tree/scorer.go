package tree

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark-go/proto/spark_tree"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/treenode"
)

// PolarityScoreDepth is the depth of the tree to consider for the polarity score.
const PolarityScoreDepth = 5

// PolarityScoreAlpha is the prior probability of a user being online and swapping.
const PolarityScoreAlpha = 0.1

// PolarityScoreGamma is the exponential decay for leaves that are more distant from the candidate.
const PolarityScoreGamma = 0.5

type Scorer interface {
	Score(leafID uuid.UUID, sspPublicKey []byte, userPublicKey []byte) float32
	FetchPolarityScores(req *pb.FetchPolarityScoreRequest, stream pb.SparkTreeService_FetchPolarityScoresServer) error
}

type TestScorer struct{}

func (s *TestScorer) Score(_ uuid.UUID, _ []byte, _ []byte) float32 {
	return 0
}

func (s *TestScorer) FetchPolarityScores(_ *pb.FetchPolarityScoreRequest, _ pb.SparkTreeService_FetchPolarityScoresServer) error {
	return nil
}

type PolarityScorer struct {
	dbClient           *ent.Client
	probPubKeyCanClaim map[uuid.UUID]map[string]float32
}

func NewPolarityScorer(dbClient *ent.Client) *PolarityScorer {
	scorer := &PolarityScorer{
		dbClient:           dbClient,
		probPubKeyCanClaim: make(map[uuid.UUID]map[string]float32),
	}
	return scorer
}

func (s *PolarityScorer) Start() {
	const limit = 1000
	lastUpdated := time.Now().Add(-24 * 30 * time.Hour)
	for {
		log.Printf("checking for leaves updated after: %v", lastUpdated)
		leaves, _ := s.dbClient.TreeNode.Query().
			Where(
				treenode.StatusEQ(schema.TreeNodeStatusAvailable),
				treenode.UpdateTimeGTE(lastUpdated),
			).
			Order(
				ent.Desc(treenode.FieldUpdateTime),
			).
			Limit(limit).
			All(context.Background())
		log.Printf("found %d leaves to update", len(leaves))
		for _, leaf := range leaves {
			node := leaf
			for i := 0; i < PolarityScoreDepth; i++ {
				if node.Edges.Parent == nil {
					break
				}
				node = node.Edges.Parent
			}
			s.UpdateLeaves(node)
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
func (s *PolarityScorer) UpdateLeaves(node *ent.TreeNode) {
	// Helper function to recursively build the helper tree
	var buildHelperTree func(*ent.TreeNode) *HelperNode
	buildHelperTree = func(n *ent.TreeNode) *HelperNode {
		helperNode := NewHelperNode(string(n.OwnerIdentityPubkey), n.ID)

		// Load and process all children
		children, err := n.QueryChildren().Where().All(context.Background())
		if err != nil {
			return helperNode
		}

		for _, child := range children {
			childHelper := buildHelperTree(child)
			childHelper.parent = helperNode
			helperNode.children = append(helperNode.children, childHelper)
		}

		return helperNode
	}

	// Build the helper tree starting from the given node
	helperTree := buildHelperTree(node)
	for _, leaf := range helperTree.Leaves() {
		if _, ok := s.probPubKeyCanClaim[leaf.leafID]; !ok {
			s.probPubKeyCanClaim[leaf.leafID] = make(map[string]float32)
		}
		for owner, score := range leaf.Score() {
			s.probPubKeyCanClaim[leaf.leafID][owner] = score
		}
	}
}

// Score computes a measure of how much the SSP wants the leaf vs giving it to the user.
func (s *PolarityScorer) Score(leafID uuid.UUID, sspPublicKey []byte, userPublicKey []byte) float32 {
	// Check if leaf exists in the map
	leafScores, exists := s.probPubKeyCanClaim[leafID]
	if !exists {
		return 0
	}

	// Get probabilities, defaulting to 0 if pubkey not found
	probSspCanClaim := leafScores[string(sspPublicKey)]
	probUserCanClaim := leafScores[string(userPublicKey)]

	return probSspCanClaim - probUserCanClaim
}

func (s *PolarityScorer) FetchPolarityScores(req *pb.FetchPolarityScoreRequest, stream pb.SparkTreeService_FetchPolarityScoresServer) error {
	targetPubKeys := make(map[string]bool)
	for _, pubKey := range req.PublicKeys {
		targetPubKeys[string(pubKey)] = true
	}
	if len(targetPubKeys) > 0 {
		log.Printf("fetching polarity scores for %d target pubkeys", len(targetPubKeys))
	} else {
		log.Printf("fetching all polarity scores")
	}

	log.Printf("found %d leaves in map", len(s.probPubKeyCanClaim))
	for leafID, leafScores := range s.probPubKeyCanClaim {
		for pubKey, score := range leafScores {
			if len(targetPubKeys) > 0 && !targetPubKeys[pubKey] {
				continue
			}
			err := stream.Send(&pb.PolarityScore{
				LeafId:    leafID.String(),
				PublicKey: []byte(pubKey),
				Score:     score,
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}
