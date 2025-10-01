package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/lightsparkdev/spark/common/keys"
)

type SparkInvoice struct {
	ent.Schema
}

func (SparkInvoice) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (SparkInvoice) Fields() []ent.Field {
	return []ent.Field{
		field.String("spark_invoice").
			NotEmpty().
			Unique().
			Immutable().
			Comment("The raw invoice string"),
		field.Time("expiry_time").
			Optional().
			Immutable().
			Comment("The expiry time of the invoice"),
		field.Bytes("receiver_public_key").
			Immutable().
			GoType(keys.Public{}).
			Comment("The public key of the receiver of the invoice"),
	}
}

func (SparkInvoice) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("token_transaction", TokenTransaction.Type).
			Ref("spark_invoice").
			Comment("The token transaction this invoice paid. Only set for invoices that paid a token transaction."),
		edge.From("transfer", Transfer.Type).
			Ref("spark_invoice").
			Comment("The sats transfer this invoice paid. Only set for invoices that paid a sats transfer."),
	}
}
