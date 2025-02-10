package tree

import (
	"log"

	"github.com/google/uuid"
)

// HelperNode is a helper struct for the polarity score computation.
type HelperNode struct {
	pubKey   string
	leafID   uuid.UUID
	parent   *HelperNode
	children []*HelperNode
}

// NewHelperNode creates a new HelperNode.
func NewHelperNode(pubKey string, leafID uuid.UUID) *HelperNode {
	return &HelperNode{
		pubKey:   pubKey,
		leafID:   leafID,
		children: []*HelperNode{},
	}
}

func (h *HelperNode) AddChild(child *HelperNode) {
	child.parent = h
	h.children = append(h.children, child)
}

func (h *HelperNode) IsLeaf() bool {
	return len(h.children) == 0
}

// Owners returns a mapping from pubkeys to the number of leaves that each owns.
func (h *HelperNode) Owners() map[string]int {
	if h.IsLeaf() {
		counts := make(map[string]int)
		counts[h.pubKey] = 1
		return counts
	}
	owners := make(map[string]int)
	for _, child := range h.children {
		for owner, count := range child.Owners() {
			owners[owner] += count
		}
	}
	return owners
}

func (h *HelperNode) Leaves() []*HelperNode {
	if h.IsLeaf() {
		return []*HelperNode{h}
	}
	leaves := []*HelperNode{}
	for _, child := range h.children {
		leaves = append(leaves, child.Leaves()...)
	}
	return leaves
}

// Score returns a mapping from pubkeys to the ownership score.
func (h *HelperNode) Score() map[string]float32 {
	depth := 0
	node := h
	scores := make(map[string]float32)
	multiplier := float32(1.0)
	for node != nil && depth < PolarityScoreDepth {
		owners := node.Owners()
		if len(owners) == 1 {
			// The current node is fully owned by one public key.
			for owner := range owners {
				scores[owner] += float32(10.0 * multiplier)
				break
			}
		} else {
			// The current node is owned by multiple public keys. Assign scores proportionally.
			totalOwners := 0
			for _, count := range owners {
				// TODO: Weight the count based on the probability of the user coming online.
				totalOwners += count
			}
			for owner, count := range owners {
				scores[owner] += float32(float32(count) / float32(totalOwners) * multiplier)
			}
		}

		multiplier *= PolarityScoreGamma
		node = node.parent
		depth++
		log.Printf("depth: %d, multiplier: %f, scores: %v", depth, multiplier, scores)
	}

	return scores
}
