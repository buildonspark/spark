package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

// TokenAllowanceSpend records the value and fee metered against an allowance
// for a single delegated token transaction.
type TokenAllowanceSpend struct {
	ent.Schema
}

// Mixin is the mixin for the token allowance spends table.
func (TokenAllowanceSpend) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the token allowance spends table.
func (TokenAllowanceSpend) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("token_allowance_id", uuid.UUID{}).
			Immutable().
			Comment("Foreign key referencing the allowance this spend is metered against.").
			Annotations(entexample.Default("019a0ef8-5794-7677-af5f-d3948d691114")),
		field.Bytes("metered_amount").
			Immutable().
			Comment("Value counted against the allowance total for this spend, as a uint128 big-endian.").
			Annotations(entexample.Default("00000000000000000000000000002710")),
		field.Enum("status").
			GoType(st.TokenAllowanceSpendStatus("")).
			Default(string(st.TokenAllowanceSpendStatusReserved)).
			Comment("Whether the metered amount is still RESERVED against the allowance or RELEASED."),
	}
}

// Edges are the edges for the token allowance spends table.
func (TokenAllowanceSpend) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("token_allowance", TokenAllowance.Type).
			Ref("token_allowance_spend").
			Field("token_allowance_id").
			Unique().
			Immutable().
			Required().
			Comment("The allowance this spend is metered against."),
		edge.To("token_transaction", TokenTransaction.Type).
			Unique().
			Immutable().
			Required().
			Comment("The token transaction whose delegated spend this row meters."),
	}
}

// Indexes are the indexes for the token allowance spends table.
func (TokenAllowanceSpend) Indexes() []ent.Index {
	return []ent.Index{
		// At most one allowance spend per token transaction.
		index.Edges("token_transaction").Unique().
			StorageKey("tokenallowancespend_unique_token_transaction"),
		// Lazy release scans an allowance's RESERVED spends on every delegated prepare.
		index.Fields("token_allowance_id", "status").
			StorageKey("tokenallowancespend_token_allowance_id_status"),
	}
}
