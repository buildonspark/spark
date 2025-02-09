package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TokenLeafStatus is the status of a token leaf.
type TokenLeafStatus string

const (
	// TokenLeafStatusCreating is the initial status of leaf that is being created.
	TokenLeafStatusCreatedUnsigned TokenLeafStatus = "CREATED_UNSIGNED"
	// TokenLeafStatusSigned is the status after a leaf has been signed by the operator
	// but before the transaction has been finalized.
	TokenLeafStatusCreatedSigned TokenLeafStatus = "CREATED_SIGNED"
	// TokenLeafStatusFinalized is the status after a leaf has been finalized by the
	// operator and is ready for spending.
	TokenLeafStatusCreatedFinalized TokenLeafStatus = "CREATED_FINALIZED"
	// TokenLeafStatusSpent is the status of a leaf after a tx has come in to spend it but
	// before the transaction has been signed.
	TokenLeafStatusSpentUnsigned TokenLeafStatus = "SPENT_UNSIGNED"
	// TokenLeafStatusSpent is the status of a leaf after the tx has been signed by the
	// operator to spend it but before it is finalized.
	TokenLeafStatusSpentSigned TokenLeafStatus = "SPENT_SIGNED"
	// TokenLeafStatusSpentKeyshareReleased is the status of a leaf after the keyshare
	// hash been released but before the private key has been provided by the wallet.
	TokenLeafStatusSpentKeyshareReleased TokenLeafStatus = "SPENT_KEYSHARE_RELEASED"
	// TokenLeafStatusSpentFinalized is the status of a leaf after the tx has been signed
	// by the operator to spend it but before it is finalized.
	TokenLeafStatusSpentFinalized TokenLeafStatus = "SPENT_FINALIZED"
)

// Values returns the values of the token leaf status.
func (TokenLeafStatus) Values() []string {
	return []string{
		string(TokenLeafStatusCreatedUnsigned),
		string(TokenLeafStatusCreatedSigned),
		string(TokenLeafStatusCreatedFinalized),
		string(TokenLeafStatusSpentUnsigned),
		string(TokenLeafStatusSpentSigned),
		string(TokenLeafStatusSpentKeyshareReleased),
		string(TokenLeafStatusSpentFinalized),
	}
}

// TokenLeaf is the schema for the token leafs table.
type TokenLeaf struct {
	ent.Schema
}

// Mixin is the mixin for the token leafs table.
func (TokenLeaf) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the token leafs table.
func (TokenLeaf) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("status").GoType(TokenLeafStatus("")),
		field.Bytes("owner_public_key").NotEmpty().Immutable(),
		field.Uint64("withdrawal_bond_sats").Immutable(),
		field.Uint64("withdrawal_locktime").Immutable(),
		field.Bytes("withdrawal_revocation_public_key").Immutable(),
		field.Bytes("token_public_key").NotEmpty().Immutable(),
		field.Bytes("token_amount").NotEmpty().Immutable(),
		field.Uint32("leaf_created_transaction_ouput_vout").Immutable(),
		field.Bytes("leaf_spent_ownership_signature").Optional(),
		field.Uint32("leaf_spent_transaction_input_vout").Optional(),
		field.Bytes("leaf_spent_revocation_private_key").Optional(),
	}
}

// Edges are the edges for the token leafs table.
func (TokenLeaf) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("revocation_keyshare", SigningKeyshare.Type).
			Unique().
			Required().
			Immutable(),
		edge.To("leaf_created_token_transaction_receipt", TokenTransactionReceipt.Type).
			Unique(),
		// Not required because these are only set once the leaf has been spent.
		edge.To("leaf_spent_token_transaction_receipt", TokenTransactionReceipt.Type).
			Unique(),
	}
}

// Indexes are the indexes for the token leafs table.
func (TokenLeaf) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("owner_public_key"),
	}
}
