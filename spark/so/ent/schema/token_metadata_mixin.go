package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// TokenMetadataMixin holds the shared fields for token creation schemas.
type TokenMetadataMixin struct {
	mixin.Schema
}

func (TokenMetadataMixin) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("issuer_public_key").NotEmpty().Immutable(),
		field.String("token_name").NotEmpty().Immutable(),
		field.String("token_ticker").NotEmpty().Immutable(),
		field.Uint8("decimals").Immutable(),
		field.Bytes("max_supply").NotEmpty().Immutable(),
		field.Bool("is_freezable").Immutable(),
		field.Enum("network").GoType(st.Network("")).Immutable(),
		// Token identifier is derived from the above token metadata fields.
		// Despite that, we store it explicitly to enable efficient indexed lookups.
		// The .Unique() generates an index on the token_identifier
		field.Bytes("token_identifier").NotEmpty().Immutable().Unique(),
	}
}

func (TokenMetadataMixin) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("issuer_public_key"),
	}
}
