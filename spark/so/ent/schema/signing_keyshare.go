package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type SigningKeyshareStatus string

const (
	KeyshareStatusAvailable SigningKeyshareStatus = "AVAILABLE"
	KeyshareStatusInUse     SigningKeyshareStatus = "IN_USE"
)

func (SigningKeyshareStatus) Values() []string {
	return []string{
		string(KeyshareStatusAvailable),
		string(KeyshareStatusInUse),
	}
}

// SigningKeyshare holds the schema definition for the SigningKeyshare entity.
type SigningKeyshare struct {
	ent.Schema
}

func (SigningKeyshare) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (SigningKeyshare) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("coordinator_index"),
	}
}

// Fields of the SigningKeyshare.
func (SigningKeyshare) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("status").
			GoType(SigningKeyshareStatus("")),
		field.Bytes("secret_share").Immutable(),
		field.JSON("public_shares", map[string][]byte{}).Immutable(),
		field.Bytes("public_key").Immutable().Unique(),
		field.Uint32("min_signers").Immutable(),
		field.Uint64("coordinator_index").Immutable(),
	}
}

// Edges of the SigningKeyshare.
func (SigningKeyshare) Edges() []ent.Edge {
	return nil
}
