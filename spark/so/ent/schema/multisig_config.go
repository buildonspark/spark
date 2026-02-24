package schema

import (
	"context"
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	entgen "github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/hook"
	"github.com/lightsparkdev/spark/so/entexample"
)

type MultisigConfig struct {
	ent.Schema
}

func (MultisigConfig) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (MultisigConfig) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("multisig_identifier").
			Comment("SHA256 hash of the canonical MultisigConfig proto (keys sorted lexicographically)").
			NotEmpty().
			Unique().
			Immutable().
			Annotations(entexample.Default(
				"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			)),
		field.Uint32("num_signers_threshold").
			Comment("Number of signatures required to authorize an action").
			Min(1).
			Immutable().
			Annotations(entexample.Default(2)),
		field.Uint32("num_signers_total").
			Comment("Expected number of signers; enforced by MultisigMember creation hook").
			Min(2).
			Immutable().
			Annotations(entexample.Default(3)),
	}
}

func (MultisigConfig) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("members", MultisigMember.Type).
			Immutable(),
	}
}

func (MultisigConfig) Indexes() []ent.Index {
	return nil
}

func (MultisigConfig) Hooks() []ent.Hook {
	return []ent.Hook{
		func(next ent.Mutator) ent.Mutator {
			return hook.MultisigConfigFunc(func(ctx context.Context, m *entgen.MultisigConfigMutation) (ent.Value, error) {
				if !m.Op().Is(ent.OpCreate) {
					return next.Mutate(ctx, m)
				}
				threshold, thresholdExists := m.NumSignersThreshold()
				total, totalExists := m.NumSignersTotal()
				if thresholdExists && totalExists && threshold > total {
					return nil, fmt.Errorf("num_signers_threshold (%d) cannot exceed num_signers_total (%d)", threshold, total)
				}
				return next.Mutate(ctx, m)
			})
		},
	}
}
