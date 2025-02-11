package tree

import (
	"context"
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
	FetchPolarityScores() (*pb.FetchPolarityScoreResponse, error)
}

type TestScorer struct{}

func (s *TestScorer) Score(_ uuid.UUID, _ []byte, _ []byte) float32 {
	return 0
}

func (s *TestScorer) FetchPolarityScores() (*pb.FetchPolarityScoreResponse, error) {
	return &pb.FetchPolarityScoreResponse{}, nil
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
	lastUpdated := time.Now()
	for {
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
			time.Sleep(10 * time.Second)
		}
	}
}

// UpdateLeaves updates the polarity score for all the leaves under the given node.
func (s *PolarityScorer) UpdateLeaves(node *ent.TreeNode) {
	// Helper function to recursively build the helper tree
	var buildHelperTree func(*ent.TreeNode) *HelperNode
	buildHelperTree = func(n *ent.TreeNode) *HelperNode {
		helperNode := NewHelperNode(string(n.OwnerSigningPubkey), n.ID)

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

func (s *PolarityScorer) FetchPolarityScores() (*pb.FetchPolarityScoreResponse, error) {
	scores := pb.FetchPolarityScoreResponse{}

	for leafID, leafScores := range s.probPubKeyCanClaim {
		for pubKey, score := range leafScores {
			scores.Scores = append(scores.Scores, &pb.PolarityScores{
				LeafId:        leafID.String(),
				PublicKey:     []byte(pubKey),
				PolarityScore: score,
			})
		}
	}
	return &scores, nil
}
