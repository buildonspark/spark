package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/entexample"
)

type WalletSetting struct {
	ent.Schema
}

// Mixin is the mixin for the WalletSetting table.
func (WalletSetting) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the WalletSetting table.
func (WalletSetting) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("owner_identity_public_key").
			Unique().
			Immutable().
			GoType(keys.Public{}).
			Comment("Signing public key of the owner of the deposit address.").
			Annotations(entexample.Default("028c094a432d46a0ac95349d792c2e3730bd60c29188db716f56a99e39b95338b4")),
		field.Bool("private_enabled").
			Default(false).
			Comment("Indicates whether privacy features are enabled for this wallet."),
		field.Bytes("master_identity_public_key").
			Nillable().
			Optional().
			GoType(keys.Public{}).
			Comment("The master identity public key that is allowed to bypass the privacy and read the wallet."),
	}
}

// Indexes are the indexes for the WalletSetting table.
func (WalletSetting) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("owner_identity_public_key"),
		index.Fields("master_identity_public_key"),
	}
}
