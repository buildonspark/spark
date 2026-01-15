package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

// L1WithdrawalTransaction tracks token unilateral withdrawal transactions detected on L1.
type L1WithdrawalTransaction struct {
	ent.Schema
}

func (L1WithdrawalTransaction) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (L1WithdrawalTransaction) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("confirmation_txid").
			GoType(schematype.TxID{}).
			Immutable().
			Comment("Transaction ID of the L1 transaction containing the withdrawal.").
			Annotations(entexample.Default("6afc6ebd5ce104a3d03a927e48b05ee5b9ba52ec28dea2e4b79776e2f95de2d4")),
		field.Bytes("confirmation_block_hash").
			Immutable().
			Comment("Hash of the block that confirmed the withdrawal transaction.").
			Annotations(entexample.Default("0000000000000000000026a904803d445d297a4f32a4b2099f9291059af54a25")),
		field.Uint64("confirmation_height").
			Immutable().
			Comment("Block height at which the withdrawal was confirmed.").
			Annotations(entexample.Default(0)),
		field.Time("detected_at").
			Immutable().
			Comment("Timestamp when the SO detected this withdrawal.").
			Annotations(entexample.Default(time.Unix(0, 0))),
		field.Bytes("owner_signature").
			Immutable().
			Comment("The owner's signature over the aggregated SE signatures for this withdrawal batch.").
			Annotations(entexample.Default("14bc648e78ec4ae6376b6752c35b1bd3f7a3c60a4caf3b107f9a08891bde9565006afe542e056da2726baf0915c61cbf6ec07b84b6fbcba4b82b6e5db953b1db")),
	}
}

func (L1WithdrawalTransaction) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("withdrawals", L1TokenOutputWithdrawal.Type).
			Comment("Individual token output withdrawals included in this transaction."),
		edge.To("se_entity", EntityDkgKey.Type).
			Unique().
			Required().
			Comment("The SE entity (signing key) that co-signed this withdrawal."),
	}
}
