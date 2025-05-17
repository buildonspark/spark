package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type TokenOutputStatus string

const (
	// TokenOutputStatusCreating is the status of an output after the creation has started
	// but before the transaction creating it has been signed.
	TokenOutputStatusCreatedStarted TokenOutputStatus = "CREATED_STARTED"
	// TokenOutputStatusFinalized is the status if a transaction creating this output was started
	// but then cancelled due to a threshold of SOs not responding. These outputs are permanently invalid.
	TokenOutputStatusCreatedStartedCancelled TokenOutputStatus = "CREATED_STARTED_CANCELLED"
	// TokenOutputStatusSigned is the status after an output has been signed by the operator
	// but before the transaction has been finalized.
	TokenOutputStatusCreatedSigned TokenOutputStatus = "CREATED_SIGNED"
	// TokenOutputStatusFinalized is the status if a transaction creating this output was signed
	// but then cancelled due to a threshold of SOs not responding. These outputs are permanently invalid.
	TokenOutputStatusCreatedSignedCancelled TokenOutputStatus = "CREATED_SIGNED_CANCELLED"
	// TokenOutputStatusCreatedFinalized is the status after an output has been finalized by the
	// operator and is ready for spending.
	TokenOutputStatusCreatedFinalized TokenOutputStatus = "CREATED_FINALIZED"
	// TokenOutputStatusSpentStarted is the status of an output after a tx has come in to start
	// spending but before the transaction has been signed.
	TokenOutputStatusSpentStarted TokenOutputStatus = "SPENT_STARTED"
	// TokenOutputStatusSpent is the status of an output after the tx has been signed by the
	// operator to spend it but before it is finalized.
	TokenOutputStatusSpentSigned TokenOutputStatus = "SPENT_SIGNED"
	// TokenOutputStatusSpentFinalized is the status of an output after the tx has been signed
	// by the operator to spend it but before it is finalized.
	TokenOutputStatusSpentFinalized TokenOutputStatus = "SPENT_FINALIZED"
)

func (TokenOutputStatus) Values() []string {
	return []string{
		string(TokenOutputStatusCreatedStarted),
		string(TokenOutputStatusCreatedStartedCancelled),
		string(TokenOutputStatusCreatedSigned),
		string(TokenOutputStatusCreatedSignedCancelled),
		string(TokenOutputStatusCreatedFinalized),
		string(TokenOutputStatusSpentStarted),
		string(TokenOutputStatusSpentSigned),
		string(TokenOutputStatusSpentFinalized),
	}
}

type TokenOutput struct {
	ent.Schema
}

func (TokenOutput) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (TokenOutput) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("status").GoType(TokenOutputStatus("")),
		field.Bytes("owner_public_key").NotEmpty().Immutable(),
		field.Uint64("withdraw_bond_sats").Immutable(),
		field.Uint64("withdraw_relative_block_locktime").Immutable(),
		field.Bytes("withdraw_revocation_commitment").Immutable(),
		field.Bytes("token_public_key").NotEmpty().Immutable(),
		field.Bytes("token_amount").NotEmpty().Immutable(),
		field.Int32("created_transaction_output_vout").Immutable(),
		field.Bytes("spent_ownership_signature").Optional(),
		field.Bytes("spent_operator_specific_ownership_signature").Optional(),
		field.Int32("spent_transaction_input_vout").Optional(),
		field.Bytes("spent_revocation_secret").Optional(),
		field.Bytes("confirmed_withdraw_block_hash").Optional(),
		field.Enum("network").GoType(Network("")).Optional(),
	}
}

func (TokenOutput) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("revocation_keyshare", SigningKeyshare.Type).
			Unique().
			Required().
			Immutable(),
		edge.To("output_created_token_transaction", TokenTransaction.Type).
			Unique(),
		// Not required because these are only set once the output has been spent.
		edge.To("output_spent_token_transaction", TokenTransaction.Type).
			Unique(),
	}
}

func (TokenOutput) Indexes() []ent.Index {
	return []ent.Index{
		// Enable fast fetching of all outputs owned by a token owner, or optionally all token outputs
		// owned by a token owner for a specific token type.
		index.Fields("owner_public_key", "token_public_key"),
		// Enables quick unmarking of withdrawn outputs in response to block reorgs.
		index.Fields("confirmed_withdraw_block_hash"),
	}
}
