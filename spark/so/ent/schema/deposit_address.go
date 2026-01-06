package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/entexample"
)

// DepositAddress is the schema for the deposit addresses table.
type DepositAddress struct {
	ent.Schema
}

// Mixin is the mixin for the deposit addresses table.
func (DepositAddress) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
		NotifyMixin{AdditionalFields: []string{"owner_identity_pubkey", "confirmation_txid"}},
	}
}

// Indexes are the indexes for the deposit addresses table.
func (DepositAddress) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("address"),
		index.Fields("owner_identity_pubkey"),
		index.Fields("owner_signing_pubkey"),
		index.Edges("signing_keyshare"),
		index.Fields("network", "owner_identity_pubkey").Unique().Annotations(entsql.IndexWhere("is_static = true and is_default = true")),
		index.Fields("confirmation_height", "is_static").Annotations(entsql.IndexWhere("availability_confirmed_at IS NULL")),
	}
}

// Fields are the fields for the deposit addresses table.
func (DepositAddress) Fields() []ent.Field {
	return []ent.Field{
		field.String("address").
			NotEmpty().
			Immutable().
			Unique().
			Comment("P2TR address string that pays to the combined public key of SOs and the owner's signing public key.").
			Annotations(entexample.Default(
				"bcrt1pkvkqsq52a8uprpdzlwj2m8r3lhp2zqtp08h7sp5skfydqxkytkeqp0mxzf",
			)),
		field.Enum("network").GoType(btcnetwork.Unspecified).
			Immutable().
			Comment("Network on which the deposit address is valid.").
			Optional().
			Annotations(entexample.Default(btcnetwork.Regtest)),
		field.Bytes("owner_identity_pubkey").
			Immutable().
			GoType(keys.Public{}).
			Comment("Identity public key of the owner of the deposit address.").
			Annotations(entexample.Default(
				"037f699d5b77668b847d92a3d4ad199af4d11ebc2069cf78d7694b08be0a6b381d",
			)),
		field.Bytes("owner_signing_pubkey").
			Immutable().
			GoType(keys.Public{}).
			Comment("Signing public key of the owner of the deposit address.").
			Annotations(entexample.Default(
				"035eea7a3767e4c103c5d8ad4878d35b44af5f36beb56ec75d74c180c9af1ee3c8",
			)),
		field.Int64("confirmation_height").
			Optional().
			Comment("Height of the block that confirmed the deposit address.").
			Annotations(entexample.Default(2630707)),
		field.String("confirmation_txid").
			Optional().
			Comment("Transaction ID of the block that confirmed the deposit address.").
			Annotations(entexample.Default("6afc6ebd5ce104a3d03a927e48b05ee5b9ba52ec28dea2e4b79776e2f95de2d4")),
		field.Time("availability_confirmed_at").
			Optional().
			Default(nil).
			Comment("Timestamp when the availability of funds was confirmed (null if not yet confirmed)"),
		field.JSON("address_signatures", map[string][]byte{}).
			Optional().
			Comment("Address signatures of the deposit address. It is used prove that all SOs have generated the address.").
			Annotations(entexample.Default(map[string]string{
				"0000000000000000000000000000000000000000000000000000000000000001": "3045022100f3273595ed2ce4ec27cd00ab3a430def9014b1ea9c4a57bc86c437f6ad8ac12d022063dcf6444cc38eb5bd284a5ee0455dd22ad4368d367a546fc8f79d91902eb71e",
				"0000000000000000000000000000000000000000000000000000000000000002": "30440220297f5c8b372c17cd20ae1cd5e5572726f439bb2fb797c23a9f5cb718de1e87da0220516a2b4f627b9ce13cb4266d1899fadb625996dc58027d4641c59e3cfa76293c",
				"0000000000000000000000000000000000000000000000000000000000000003": "3045022100ca88e11371b4d126d85259a2a09a8b5000d6dcf80aee1006f3b1a127c5f12ba2022039a5bf1b8103f0504444326c664c7f03a0105c6872316b82c87a34dd02c039d7",
			})),
		field.Bytes("possession_signature").
			Optional().
			Comment("Proof of keyshare possession signature for the deposit address. It is used to prove that the key used by the coordinator to generate the address is known by all SOs.").
			Annotations(entexample.Default("14bc648e78ec4ae6376b6752c35b1bd3f7a3c60a4caf3b107f9a08891bde9565006afe542e056da2726baf0915c61cbf6ec07b84b6fbcba4b82b6e5db953b1db")),
		field.UUID("node_id", uuid.UUID{}).
			Optional().
			Comment("Node ID of the deposit address.").
			Annotations(entexample.Default("019a193a-a08f-7edf-988f-04f324eacc1b")),
		field.Bool("is_static").
			Default(false).
			Comment("Whether the deposit address is static."),
		field.Bool("is_default").
			Default(true).
			Comment("Whether the deposit address is the default address for the user." +
				"This is only used for static deposit addresses. Static deposit addresses should be unique for the network/user, but since this was not previously enforced, is_default is used to enforce uniqueness."),
	}
}

// Edges are the edges for the deposit addresses table.
func (DepositAddress) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("signing_keyshare", SigningKeyshare.Type).
			Unique().
			Required().
			Immutable(),
		edge.To("utxo", Utxo.Type),
		edge.To("utxoswaps", UtxoSwap.Type),
		edge.To("tree", Tree.Type).
			Unique(),
	}
}
