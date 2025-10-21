package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
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
		field.Bytes("owner_identity_public_key").Unique().Immutable(),
		field.Bool("private_enabled").Default(false),
	}
}

// Indexes are the indexes for the WalletSetting table.
func (WalletSetting) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("owner_identity_public_key"),
	}
}
