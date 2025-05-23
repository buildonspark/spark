// Code generated by ent, DO NOT EDIT.

package ent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql/sqlgraph"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
)

// TreeCreate is the builder for creating a Tree entity.
type TreeCreate struct {
	config
	mutation *TreeMutation
	hooks    []Hook
}

// SetCreateTime sets the "create_time" field.
func (tc *TreeCreate) SetCreateTime(t time.Time) *TreeCreate {
	tc.mutation.SetCreateTime(t)
	return tc
}

// SetNillableCreateTime sets the "create_time" field if the given value is not nil.
func (tc *TreeCreate) SetNillableCreateTime(t *time.Time) *TreeCreate {
	if t != nil {
		tc.SetCreateTime(*t)
	}
	return tc
}

// SetUpdateTime sets the "update_time" field.
func (tc *TreeCreate) SetUpdateTime(t time.Time) *TreeCreate {
	tc.mutation.SetUpdateTime(t)
	return tc
}

// SetNillableUpdateTime sets the "update_time" field if the given value is not nil.
func (tc *TreeCreate) SetNillableUpdateTime(t *time.Time) *TreeCreate {
	if t != nil {
		tc.SetUpdateTime(*t)
	}
	return tc
}

// SetOwnerIdentityPubkey sets the "owner_identity_pubkey" field.
func (tc *TreeCreate) SetOwnerIdentityPubkey(b []byte) *TreeCreate {
	tc.mutation.SetOwnerIdentityPubkey(b)
	return tc
}

// SetStatus sets the "status" field.
func (tc *TreeCreate) SetStatus(ss schema.TreeStatus) *TreeCreate {
	tc.mutation.SetStatus(ss)
	return tc
}

// SetNetwork sets the "network" field.
func (tc *TreeCreate) SetNetwork(s schema.Network) *TreeCreate {
	tc.mutation.SetNetwork(s)
	return tc
}

// SetBaseTxid sets the "base_txid" field.
func (tc *TreeCreate) SetBaseTxid(b []byte) *TreeCreate {
	tc.mutation.SetBaseTxid(b)
	return tc
}

// SetVout sets the "vout" field.
func (tc *TreeCreate) SetVout(i int16) *TreeCreate {
	tc.mutation.SetVout(i)
	return tc
}

// SetID sets the "id" field.
func (tc *TreeCreate) SetID(u uuid.UUID) *TreeCreate {
	tc.mutation.SetID(u)
	return tc
}

// SetNillableID sets the "id" field if the given value is not nil.
func (tc *TreeCreate) SetNillableID(u *uuid.UUID) *TreeCreate {
	if u != nil {
		tc.SetID(*u)
	}
	return tc
}

// SetRootID sets the "root" edge to the TreeNode entity by ID.
func (tc *TreeCreate) SetRootID(id uuid.UUID) *TreeCreate {
	tc.mutation.SetRootID(id)
	return tc
}

// SetNillableRootID sets the "root" edge to the TreeNode entity by ID if the given value is not nil.
func (tc *TreeCreate) SetNillableRootID(id *uuid.UUID) *TreeCreate {
	if id != nil {
		tc = tc.SetRootID(*id)
	}
	return tc
}

// SetRoot sets the "root" edge to the TreeNode entity.
func (tc *TreeCreate) SetRoot(t *TreeNode) *TreeCreate {
	return tc.SetRootID(t.ID)
}

// AddNodeIDs adds the "nodes" edge to the TreeNode entity by IDs.
func (tc *TreeCreate) AddNodeIDs(ids ...uuid.UUID) *TreeCreate {
	tc.mutation.AddNodeIDs(ids...)
	return tc
}

// AddNodes adds the "nodes" edges to the TreeNode entity.
func (tc *TreeCreate) AddNodes(t ...*TreeNode) *TreeCreate {
	ids := make([]uuid.UUID, len(t))
	for i := range t {
		ids[i] = t[i].ID
	}
	return tc.AddNodeIDs(ids...)
}

// Mutation returns the TreeMutation object of the builder.
func (tc *TreeCreate) Mutation() *TreeMutation {
	return tc.mutation
}

// Save creates the Tree in the database.
func (tc *TreeCreate) Save(ctx context.Context) (*Tree, error) {
	tc.defaults()
	return withHooks(ctx, tc.sqlSave, tc.mutation, tc.hooks)
}

// SaveX calls Save and panics if Save returns an error.
func (tc *TreeCreate) SaveX(ctx context.Context) *Tree {
	v, err := tc.Save(ctx)
	if err != nil {
		panic(err)
	}
	return v
}

// Exec executes the query.
func (tc *TreeCreate) Exec(ctx context.Context) error {
	_, err := tc.Save(ctx)
	return err
}

// ExecX is like Exec, but panics if an error occurs.
func (tc *TreeCreate) ExecX(ctx context.Context) {
	if err := tc.Exec(ctx); err != nil {
		panic(err)
	}
}

// defaults sets the default values of the builder before save.
func (tc *TreeCreate) defaults() {
	if _, ok := tc.mutation.CreateTime(); !ok {
		v := tree.DefaultCreateTime()
		tc.mutation.SetCreateTime(v)
	}
	if _, ok := tc.mutation.UpdateTime(); !ok {
		v := tree.DefaultUpdateTime()
		tc.mutation.SetUpdateTime(v)
	}
	if _, ok := tc.mutation.ID(); !ok {
		v := tree.DefaultID()
		tc.mutation.SetID(v)
	}
}

// check runs all checks and user-defined validators on the builder.
func (tc *TreeCreate) check() error {
	if _, ok := tc.mutation.CreateTime(); !ok {
		return &ValidationError{Name: "create_time", err: errors.New(`ent: missing required field "Tree.create_time"`)}
	}
	if _, ok := tc.mutation.UpdateTime(); !ok {
		return &ValidationError{Name: "update_time", err: errors.New(`ent: missing required field "Tree.update_time"`)}
	}
	if _, ok := tc.mutation.OwnerIdentityPubkey(); !ok {
		return &ValidationError{Name: "owner_identity_pubkey", err: errors.New(`ent: missing required field "Tree.owner_identity_pubkey"`)}
	}
	if v, ok := tc.mutation.OwnerIdentityPubkey(); ok {
		if err := tree.OwnerIdentityPubkeyValidator(v); err != nil {
			return &ValidationError{Name: "owner_identity_pubkey", err: fmt.Errorf(`ent: validator failed for field "Tree.owner_identity_pubkey": %w`, err)}
		}
	}
	if _, ok := tc.mutation.Status(); !ok {
		return &ValidationError{Name: "status", err: errors.New(`ent: missing required field "Tree.status"`)}
	}
	if v, ok := tc.mutation.Status(); ok {
		if err := tree.StatusValidator(v); err != nil {
			return &ValidationError{Name: "status", err: fmt.Errorf(`ent: validator failed for field "Tree.status": %w`, err)}
		}
	}
	if _, ok := tc.mutation.Network(); !ok {
		return &ValidationError{Name: "network", err: errors.New(`ent: missing required field "Tree.network"`)}
	}
	if v, ok := tc.mutation.Network(); ok {
		if err := tree.NetworkValidator(v); err != nil {
			return &ValidationError{Name: "network", err: fmt.Errorf(`ent: validator failed for field "Tree.network": %w`, err)}
		}
	}
	if _, ok := tc.mutation.BaseTxid(); !ok {
		return &ValidationError{Name: "base_txid", err: errors.New(`ent: missing required field "Tree.base_txid"`)}
	}
	if v, ok := tc.mutation.BaseTxid(); ok {
		if err := tree.BaseTxidValidator(v); err != nil {
			return &ValidationError{Name: "base_txid", err: fmt.Errorf(`ent: validator failed for field "Tree.base_txid": %w`, err)}
		}
	}
	if _, ok := tc.mutation.Vout(); !ok {
		return &ValidationError{Name: "vout", err: errors.New(`ent: missing required field "Tree.vout"`)}
	}
	if v, ok := tc.mutation.Vout(); ok {
		if err := tree.VoutValidator(v); err != nil {
			return &ValidationError{Name: "vout", err: fmt.Errorf(`ent: validator failed for field "Tree.vout": %w`, err)}
		}
	}
	return nil
}

func (tc *TreeCreate) sqlSave(ctx context.Context) (*Tree, error) {
	if err := tc.check(); err != nil {
		return nil, err
	}
	_node, _spec := tc.createSpec()
	if err := sqlgraph.CreateNode(ctx, tc.driver, _spec); err != nil {
		if sqlgraph.IsConstraintError(err) {
			err = &ConstraintError{msg: err.Error(), wrap: err}
		}
		return nil, err
	}
	if _spec.ID.Value != nil {
		if id, ok := _spec.ID.Value.(*uuid.UUID); ok {
			_node.ID = *id
		} else if err := _node.ID.Scan(_spec.ID.Value); err != nil {
			return nil, err
		}
	}
	tc.mutation.id = &_node.ID
	tc.mutation.done = true
	return _node, nil
}

func (tc *TreeCreate) createSpec() (*Tree, *sqlgraph.CreateSpec) {
	var (
		_node = &Tree{config: tc.config}
		_spec = sqlgraph.NewCreateSpec(tree.Table, sqlgraph.NewFieldSpec(tree.FieldID, field.TypeUUID))
	)
	if id, ok := tc.mutation.ID(); ok {
		_node.ID = id
		_spec.ID.Value = &id
	}
	if value, ok := tc.mutation.CreateTime(); ok {
		_spec.SetField(tree.FieldCreateTime, field.TypeTime, value)
		_node.CreateTime = value
	}
	if value, ok := tc.mutation.UpdateTime(); ok {
		_spec.SetField(tree.FieldUpdateTime, field.TypeTime, value)
		_node.UpdateTime = value
	}
	if value, ok := tc.mutation.OwnerIdentityPubkey(); ok {
		_spec.SetField(tree.FieldOwnerIdentityPubkey, field.TypeBytes, value)
		_node.OwnerIdentityPubkey = value
	}
	if value, ok := tc.mutation.Status(); ok {
		_spec.SetField(tree.FieldStatus, field.TypeEnum, value)
		_node.Status = value
	}
	if value, ok := tc.mutation.Network(); ok {
		_spec.SetField(tree.FieldNetwork, field.TypeEnum, value)
		_node.Network = value
	}
	if value, ok := tc.mutation.BaseTxid(); ok {
		_spec.SetField(tree.FieldBaseTxid, field.TypeBytes, value)
		_node.BaseTxid = value
	}
	if value, ok := tc.mutation.Vout(); ok {
		_spec.SetField(tree.FieldVout, field.TypeInt16, value)
		_node.Vout = value
	}
	if nodes := tc.mutation.RootIDs(); len(nodes) > 0 {
		edge := &sqlgraph.EdgeSpec{
			Rel:     sqlgraph.M2O,
			Inverse: false,
			Table:   tree.RootTable,
			Columns: []string{tree.RootColumn},
			Bidi:    false,
			Target: &sqlgraph.EdgeTarget{
				IDSpec: sqlgraph.NewFieldSpec(treenode.FieldID, field.TypeUUID),
			},
		}
		for _, k := range nodes {
			edge.Target.Nodes = append(edge.Target.Nodes, k)
		}
		_node.tree_root = &nodes[0]
		_spec.Edges = append(_spec.Edges, edge)
	}
	if nodes := tc.mutation.NodesIDs(); len(nodes) > 0 {
		edge := &sqlgraph.EdgeSpec{
			Rel:     sqlgraph.O2M,
			Inverse: true,
			Table:   tree.NodesTable,
			Columns: []string{tree.NodesColumn},
			Bidi:    false,
			Target: &sqlgraph.EdgeTarget{
				IDSpec: sqlgraph.NewFieldSpec(treenode.FieldID, field.TypeUUID),
			},
		}
		for _, k := range nodes {
			edge.Target.Nodes = append(edge.Target.Nodes, k)
		}
		_spec.Edges = append(_spec.Edges, edge)
	}
	return _node, _spec
}

// TreeCreateBulk is the builder for creating many Tree entities in bulk.
type TreeCreateBulk struct {
	config
	err      error
	builders []*TreeCreate
}

// Save creates the Tree entities in the database.
func (tcb *TreeCreateBulk) Save(ctx context.Context) ([]*Tree, error) {
	if tcb.err != nil {
		return nil, tcb.err
	}
	specs := make([]*sqlgraph.CreateSpec, len(tcb.builders))
	nodes := make([]*Tree, len(tcb.builders))
	mutators := make([]Mutator, len(tcb.builders))
	for i := range tcb.builders {
		func(i int, root context.Context) {
			builder := tcb.builders[i]
			builder.defaults()
			var mut Mutator = MutateFunc(func(ctx context.Context, m Mutation) (Value, error) {
				mutation, ok := m.(*TreeMutation)
				if !ok {
					return nil, fmt.Errorf("unexpected mutation type %T", m)
				}
				if err := builder.check(); err != nil {
					return nil, err
				}
				builder.mutation = mutation
				var err error
				nodes[i], specs[i] = builder.createSpec()
				if i < len(mutators)-1 {
					_, err = mutators[i+1].Mutate(root, tcb.builders[i+1].mutation)
				} else {
					spec := &sqlgraph.BatchCreateSpec{Nodes: specs}
					// Invoke the actual operation on the latest mutation in the chain.
					if err = sqlgraph.BatchCreate(ctx, tcb.driver, spec); err != nil {
						if sqlgraph.IsConstraintError(err) {
							err = &ConstraintError{msg: err.Error(), wrap: err}
						}
					}
				}
				if err != nil {
					return nil, err
				}
				mutation.id = &nodes[i].ID
				mutation.done = true
				return nodes[i], nil
			})
			for i := len(builder.hooks) - 1; i >= 0; i-- {
				mut = builder.hooks[i](mut)
			}
			mutators[i] = mut
		}(i, ctx)
	}
	if len(mutators) > 0 {
		if _, err := mutators[0].Mutate(ctx, tcb.builders[0].mutation); err != nil {
			return nil, err
		}
	}
	return nodes, nil
}

// SaveX is like Save, but panics if an error occurs.
func (tcb *TreeCreateBulk) SaveX(ctx context.Context) []*Tree {
	v, err := tcb.Save(ctx)
	if err != nil {
		panic(err)
	}
	return v
}

// Exec executes the query.
func (tcb *TreeCreateBulk) Exec(ctx context.Context) error {
	_, err := tcb.Save(ctx)
	return err
}

// ExecX is like Exec, but panics if an error occurs.
func (tcb *TreeCreateBulk) ExecX(ctx context.Context) {
	if err := tcb.Exec(ctx); err != nil {
		panic(err)
	}
}
