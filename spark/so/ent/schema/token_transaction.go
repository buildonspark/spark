package schema

import (
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

type TokenTransaction struct {
	ent.Schema
}

func (TokenTransaction) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
		NotifyMixin{},
	}
}

func (TokenTransaction) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("partial_token_transaction_hash").NotEmpty(),
		field.Bytes("finalized_token_transaction_hash").NotEmpty().Unique(),
		field.Bytes("operator_signature").Optional().Unique(),
		field.Enum("status").GoType(st.TokenTransactionStatus("")).Optional(),
		field.Time("expiry_time").Optional().Immutable(),
		field.Bytes("coordinator_public_key").Optional(),
		field.Time("client_created_timestamp").Optional(),
		field.Int("version").GoType(st.TokenTransactionVersion(0)).Default(int(st.TokenTransactionVersionV0)).Validate(func(v int) error {
			if !st.TokenTransactionVersion(v).IsValid() {
				return fmt.Errorf("invalid token transaction version: %d", v)
			}
			return nil
		}),
	}
}

func (TokenTransaction) Edges() []ent.Edge {
	// Token Transactions are associated with
	// a) one or more created outputs representing new withdrawable token holdings.
	// b) one or more spent outputs (for transfers) or a single mint.
	return []ent.Edge{
		edge.From("spent_output", TokenOutput.Type).
			Ref("output_spent_token_transaction"),
		edge.From("spent_output_v2", TokenOutput.Type).
			Ref("output_spent_started_token_transactions"),
		edge.From("created_output", TokenOutput.Type).
			Ref("output_created_token_transaction"),
		edge.To("mint", TokenMint.Type).
			Unique(),
		edge.To("create", TokenCreate.Type).
			Unique(),
		edge.To("payment_intent", PaymentIntent.Type).Unique(),
		edge.To("peer_signatures", TokenTransactionPeerSignature.Type),
		edge.To("spark_invoice", SparkInvoice.Type),
	}
}

func (TokenTransaction) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("finalized_token_transaction_hash"),
		index.Fields("partial_token_transaction_hash"),
		index.Fields("expiry_time", "status"),
		// Needed for query_token_transactions query
		index.Fields("update_time"),
	}
}
