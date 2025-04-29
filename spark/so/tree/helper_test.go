package tree

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestHelperNode(t *testing.T) {
	l1 := NewHelperNode("SSP_OWNED", uuid.New())
	l2 := NewHelperNode("USER_OWNED", uuid.New())
	l3 := NewHelperNode("SSP_OWNED", uuid.New())
	l4 := NewHelperNode("SSP_OWNED", uuid.New())

	branch1 := NewHelperNode("", uuid.New())
	branch1.AddChild(l1)
	branch1.AddChild(l2)

	branch2 := NewHelperNode("", uuid.New())
	branch2.AddChild(l3)
	branch2.AddChild(l4)

	root := NewHelperNode("test", uuid.New())
	root.AddChild(branch1)
	root.AddChild(branch2)

	scores := l1.Score()
	assert.InDelta(t, scores["SSP_OWNED"], 10.4375, 0.0001)
	assert.InDelta(t, scores["USER_OWNED"], 0.3125, 0.0001)

	scores = l2.Score()
	assert.InDelta(t, scores["SSP_OWNED"], 0.4375, 0.0001)
	assert.InDelta(t, scores["USER_OWNED"], 10.3125, 0.0001)

	scores = l3.Score()
	assert.InDelta(t, scores["SSP_OWNED"], 15.1875, 0.0001)
	assert.InDelta(t, scores["USER_OWNED"], 0.0625, 0.0001)

	scores = l4.Score()
	assert.InDelta(t, scores["SSP_OWNED"], 15.1875, 0.0001)
	assert.InDelta(t, scores["USER_OWNED"], 0.0625, 0.0001)
}
