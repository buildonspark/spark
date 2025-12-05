package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/entexample"
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
			Comment("The raw invoice string").
			Annotations(entexample.Default(
				"sprt1pgssxhddk4hj2rqch0gagjavhny06x6jyd6nwc9k0edtendfxrrg24y7zgassqgjzqqej9el2a784jdwgnkqzckzev435fg2yz42p28mpamy6tcj2yth8dl7wvpk78qe9n53vr2q3l86x537jqxxvysppgdyp74y65nk6elg9l5r07wpu5pn7035tyxz8l0l7fak3k30e8mwndq5zrvd0jfdhywt0yr2cj4acy2tewqwxvzyjus6ql223xk0m43dmd7q3kk3fd",
			)),
		field.Time("expiry_time").
			Optional().
			Immutable().
			Comment("The expiry time of the invoice"),
		field.Bytes("receiver_public_key").
			Immutable().
			GoType(keys.Public{}).
			Comment("The public key of the receiver of the invoice").
			Annotations(entexample.Default(
				"035dadb56f250c18bbd1d44bacbcc8fd1b5223753760b67e5abccda930c685549e",
			)),
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
