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
	// TokenLeafStatusCreating is the status of a leaf after the creation has started
	// but before the transaction creating it has been signed.
	TokenLeafStatusCreatedStarted TokenLeafStatus = "CREATED_STARTED"
	// TokenLeafStatusSigned is the status after a leaf has been signed by the operator
	// but before the transaction has been finalized.
	TokenLeafStatusCreatedSigned TokenLeafStatus = "CREATED_SIGNED"
	// TokenLeafStatusFinalized is the status if a transaction creating this leaf was signed
	// but then cancelled due to a threshold of SOs not responding. These leaves are permanently invalid.
	TokenLeafStatusCreatedSignedCancelled TokenLeafStatus = "CREATED_SIGNED_CANCELLED"
	// TokenLeafStatusCreatedFinalized is the status after a leaf has been finalized by the
	// operator and is ready for spending.
	TokenLeafStatusCreatedFinalized TokenLeafStatus = "CREATED_FINALIZED"
	// TokenLeafStatusSpentStarted is the status of a leaf after a tx has come in to start
	// spending but before the transaction has been signed.
	TokenLeafStatusSpentStarted TokenLeafStatus = "SPENT_STARTED"
	// TokenLeafStatusSpent is the status of a leaf after the tx has been signed by the
	// operator to spend it but before it is finalized.
	TokenLeafStatusSpentSigned TokenLeafStatus = "SPENT_SIGNED"
	// TokenLeafStatusSpentFinalized is the status of a leaf after the tx has been signed
	// by the operator to spend it but before it is finalized.
	TokenLeafStatusSpentFinalized TokenLeafStatus = "SPENT_FINALIZED"
)

// Values returns the values of the token leaf status.
func (TokenLeafStatus) Values() []string {
	return []string{
		string(TokenLeafStatusCreatedStarted),
		string(TokenLeafStatusCreatedSigned),
		string(TokenLeafStatusCreatedSignedCancelled),
		string(TokenLeafStatusCreatedFinalized),
		string(TokenLeafStatusSpentStarted),
		string(TokenLeafStatusSpentSigned),
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
		field.Uint64("withdraw_bond_sats").Immutable(),
		field.Uint64("withdraw_relative_block_locktime").Immutable(),
		field.Bytes("withdraw_revocation_public_key").Immutable(),
		field.Bytes("token_public_key").NotEmpty().Immutable(),
		field.Bytes("token_amount").NotEmpty().Immutable(),
		field.Int32("leaf_created_transaction_output_vout").Immutable(),
		field.Bytes("leaf_spent_ownership_signature").Optional(),
		field.Bytes("leaf_spent_operator_specific_ownership_signature").Optional(),
		field.Int32("leaf_spent_transaction_input_vout").Optional(),
		field.Bytes("leaf_spent_revocation_private_key").Optional(),
		field.Bytes("confirmed_withdraw_block_hash").Optional(),
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
		// Enable fast fetching of all leaves owned by a token owner, or optionally all token leaves
		// owned by a token owner for a specific token type.
		index.Fields("owner_public_key", "token_public_key"),
		// Enables quick unmarking of withdrawn leaves in response to block reorgs.
		index.Fields("confirmed_withdraw_block_hash"),
	}
}
