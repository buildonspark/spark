package ent

import (
	"context"

	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
)

func (t *Tree) GetRoot(ctx context.Context) (*TreeNode, error) {
	roots, err := t.QueryRoot().All(ctx)
	if err != nil {
		return nil, err
	}
	if len(roots) == 1 {
		return roots[0], nil
	}
	db := GetDbFromContext(ctx)
	root, err := db.TreeNode.Query().Where(treenode.HasTreeWith(tree.ID(t.ID))).Where(treenode.Not(treenode.HasParent())).Only(ctx)
	if err != nil {
		return nil, err
	}
	return root, nil
}
