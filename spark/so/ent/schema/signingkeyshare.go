package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
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

// Fields of the SigningKeyshare.
func (SigningKeyshare) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("status").
			GoType(SigningKeyshareStatus("")),
		field.Bytes("secret_share"),
		field.JSON("public_shares", map[string][]byte{}),
		field.Bytes("public_key"),
		field.Uint32("min_signers"),
	}
}

// Edges of the SigningKeyshare.
func (SigningKeyshare) Edges() []ent.Edge {
	return nil
}
