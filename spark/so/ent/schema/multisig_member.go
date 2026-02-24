package schema

import (
	"context"
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/keys"
	entgen "github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/hook"
	"github.com/lightsparkdev/spark/so/ent/multisigconfig"
	"github.com/lightsparkdev/spark/so/entexample"
)

type MultisigMember struct {
	ent.Schema
}

func (MultisigMember) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (MultisigMember) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("public_key").
			Comment("33-byte compressed public key of this multisig participant").
			GoType(keys.Public{}).
			Immutable().
			Annotations(entexample.Default(
				"0350f07ffc21bfd59d31e0a7a600e2995273938444447cb9bc4c75b8a895dbb853",
			)),
	}
}

func (MultisigMember) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("config", MultisigConfig.Type).
			Ref("members").
			Unique().
			Required().
			Immutable(),
	}
}

func (MultisigMember) Indexes() []ent.Index {
	return []ent.Index{
		index.Edges("config"),
		index.Fields("public_key").
			Edges("config").
			Unique(),
	}
}

func (MultisigMember) Hooks() []ent.Hook {
	return []ent.Hook{
		func(next ent.Mutator) ent.Mutator {
			return hook.MultisigMemberFunc(func(ctx context.Context, m *entgen.MultisigMemberMutation) (ent.Value, error) {
				if !m.Op().Is(ent.OpCreate) {
					return next.Mutate(ctx, m)
				}
				configID, exists := m.ConfigID()
				if !exists {
					return next.Mutate(ctx, m)
				}
				config, err := m.Client().MultisigConfig.Query().
					Where(multisigconfig.ID(configID)).
					ForUpdate().
					Only(ctx)
				if err != nil {
					return nil, fmt.Errorf("failed to fetch multisig config: %w", err)
				}
				memberCount, err := config.QueryMembers().Count(ctx)
				if err != nil {
					return nil, fmt.Errorf("failed to count existing members: %w", err)
				}
				if uint32(memberCount) >= config.NumSignersTotal {
					return nil, fmt.Errorf("multisig config already has maximum number of members (%d)", config.NumSignersTotal)
				}
				return next.Mutate(ctx, m)
			})
		},
	}
}
