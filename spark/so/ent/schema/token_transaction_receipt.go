package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TreeNode is the schema for the tree nodes table.
type TokenTransactionReceipt struct {
	ent.Schema
}

type TokenTransactionStatus string

const (
	TokenTransactionStatusStarted TokenTransactionStatus = "STARTED"
	// TokenTransactionStatusSigned is the status after a transaction has been signed by this operator.
	TokenTransactionStatusSigned TokenTransactionStatus = "SIGNED"
	// TokenTransactionStatusFinalized is the status after the revocation keys for output leaves have been shared with the operator.
	TokenTransactionStatusFinalized TokenTransactionStatus = "FINALIZED"
)

// Values returns the values of the token leaf status.
func (TokenTransactionStatus) Values() []string {
	return []string{
		string(TokenTransactionStatusStarted),
		string(TokenTransactionStatusSigned),
		string(TokenTransactionStatusFinalized),
	}
}

// Mixin is the mixin for the tree nodes table.
func (TokenTransactionReceipt) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (TokenTransactionReceipt) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("partial_token_transaction_hash").NotEmpty(),
		field.Bytes("finalized_token_transaction_hash").NotEmpty().Unique(),
		field.Bytes("operator_signature").Optional().Unique(),
		field.Enum("status").GoType(TokenTransactionStatus(TokenTransactionStatusStarted)).Optional(),
	}
}

// Edges are the edges for the token transaction payloads.
func (TokenTransactionReceipt) Edges() []ent.Edge {
	// Token Transaction Receipts are associated with
	// a) one or more created leaves representing new withdrawable token holdings.
	// b) one or more spent leaves (for transfers) or a single issuance.
	return []ent.Edge{
		edge.From("spent_leaf", TokenLeaf.Type).
			Ref("leaf_spent_token_transaction_receipt"),
		edge.From("created_leaf", TokenLeaf.Type).
			Ref("leaf_created_token_transaction_receipt"),
		edge.To("mint", TokenMint.Type).
			Unique(),
	}
}

// Indexes are the indexes for the tree nodes table.
func (TokenTransactionReceipt) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("finalized_token_transaction_hash"),
	}
}
