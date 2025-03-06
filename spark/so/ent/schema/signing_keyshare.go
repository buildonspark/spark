package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SigningKeyshareStatus is the status of a signing keyshare.
type SigningKeyshareStatus string

const (
	// KeyshareStatusAvailable is the status of a signing keyshare that is available.
	KeyshareStatusAvailable SigningKeyshareStatus = "AVAILABLE"
	// KeyshareStatusInUse is the status of a signing keyshare that is in use.
	KeyshareStatusInUse SigningKeyshareStatus = "IN_USE"
)

// Values returns the values of the signing keyshare status.
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

// Mixin is the mixin for the signing keyshares table.
func (SigningKeyshare) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Indexes are the indexes for the signing keyshares table.
func (SigningKeyshare) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("coordinator_index"),
	}
}

// Fields are the fields for the signing keyshares table.
func (SigningKeyshare) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("status").
			GoType(SigningKeyshareStatus("")),
		field.Bytes("secret_share"),
		field.JSON("public_shares", map[string][]byte{}),
		field.Bytes("public_key").Unique(),
		field.Uint32("min_signers"),
		field.Uint64("coordinator_index"),
	}
}

// Edges are the edges for the signing keyshares table.
func (SigningKeyshare) Edges() []ent.Edge {
	return nil
}
