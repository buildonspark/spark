package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/entexample"
)

// TokenMetadataMixin holds the shared fields for token creation schemas.
type TokenMetadataMixin struct {
	mixin.Schema
}

func (TokenMetadataMixin) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("issuer_public_key").
			Immutable().
			GoType(keys.Public{}).
			Annotations(
				entexample.Default("0375a9121cd7c3684ca1941978cc0dc42ce316fddf70261643f17ba3eeca6d10f2"),
			),
		field.String("token_name").
			NotEmpty().
			Immutable().
			Annotations(entexample.Default("Aura")),
		field.String("token_ticker").
			NotEmpty().
			Immutable().
			Annotations(entexample.Default("AURA")),
		field.Uint8("decimals").
			Immutable().
			Annotations(entexample.Default(8)),
		field.Bytes("max_supply").
			NotEmpty().
			Immutable().
			Annotations(entexample.Default("0000000000000000002386f26fc10000")),
		field.Bool("is_freezable").
			Immutable().
			Annotations(entexample.Default(true)),
		field.Enum("network").
			GoType(btcnetwork.Unspecified).
			Immutable().
			Annotations(entexample.Default(btcnetwork.Regtest)),
		// Token identifier is derived from the above token metadata fields.
		// Despite that, we store it explicitly to enable efficient indexed lookups.
		// The .Unique() generates an index on the token_identifier
		field.Bytes("token_identifier").
			NotEmpty().
			Immutable().
			Unique().
			Annotations(entexample.Default("3e534a8d9798fe5e20516f9b1aa05f5d78d718ece893e8af89d678c3d88f2451")),
	}
}

func (TokenMetadataMixin) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("issuer_public_key"),
	}
}
