package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TreeNode is the schema for the tree nodes table.
type TokenTransaction struct {
	ent.Schema
}

type TokenTransactionStatus string

const (
	TokenTransactionStatusStarted TokenTransactionStatus = "STARTED"
	// TokenTransactionStatusSigned is the status if a transaction was started but then cancelled due to a threshold of the
	// signatures not being acquired.
	TokenTransactionStatusStartedCancelled TokenTransactionStatus = "STARTED_CANCELLED"
	// TokenTransactionStatusSigned is the status after a transaction has been signed by this operator.
	TokenTransactionStatusSigned TokenTransactionStatus = "SIGNED"
	// TokenTransactionStatusSigned is the status if a transaction was signed but then cancelled due to a threshold of the
	// signatures not being acquired.
	TokenTransactionStatusSignedCancelled TokenTransactionStatus = "SIGNED_CANCELLED"
	// TokenTransactionStatusFinalized is the status after the revocation keys for outputs have been shared with the operator.
	TokenTransactionStatusFinalized TokenTransactionStatus = "FINALIZED"
)

func (TokenTransactionStatus) Values() []string {
	return []string{
		string(TokenTransactionStatusStarted),
		string(TokenTransactionStatusStartedCancelled),
		string(TokenTransactionStatusSigned),
		string(TokenTransactionStatusSignedCancelled),
		string(TokenTransactionStatusFinalized),
	}
}

func (TokenTransaction) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (TokenTransaction) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("partial_token_transaction_hash").NotEmpty(),
		field.Bytes("finalized_token_transaction_hash").NotEmpty().Unique(),
		field.Bytes("operator_signature").Optional().Unique(),
		field.Enum("status").GoType(TokenTransactionStatus("")).Optional(),
		field.Time("expiry_time").Optional().Immutable(),
		field.Bytes("coordinator_public_key").Optional(),
	}
}

func (TokenTransaction) Edges() []ent.Edge {
	// Token Transactions are associated with
	// a) one or more created outputs representing new withdrawable token holdings.
	// b) one or more spent outputs (for transfers) or a single mint.
	return []ent.Edge{
		edge.From("spent_output", TokenOutput.Type).
			Ref("output_spent_token_transaction"),
		edge.From("created_output", TokenOutput.Type).
			Ref("output_created_token_transaction"),
		edge.To("mint", TokenMint.Type).
			Unique(),
	}
}

func (TokenTransaction) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("finalized_token_transaction_hash"),
		index.Fields("partial_token_transaction_hash"),
	}
}
