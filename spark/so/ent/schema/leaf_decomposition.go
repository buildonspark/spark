package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/keys"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

// LeafDecomposition is the delegate-path (SE2) decomposition installed on a single
// leaf under a delegation grant. It lives alongside the leaf's primary
// decomposition: the aggregate key is unchanged, but this row records the second
// user-side signing key (the delegate's) and points at the SE2 keyshare that
// completes it. At most one decomposition may be ACTIVE per leaf.
//
// The signing_keyshare edge is optional so the row can tombstone: when the SE2
// keyshare is hard-deleted at revoke/consume (cryptographically killing the
// delegate path), the edge is cleared and the row remains as a REVOKED/CONSUMED
// record.
type LeafDecomposition struct {
	ent.Schema
}

// Mixin is the mixin for the leaf decompositions table.
func (LeafDecomposition) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the leaf decompositions table.
func (LeafDecomposition) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("delegate_signing_pubkey").
			Immutable().
			GoType(keys.Public{}).
			Comment("The delegate-path user signing public key registered for this leaf (the second decomposition's user share).").
			Annotations(entexample.Default(
				"02008022cc74b350a1fea49d1cfc2ded422ad1bfe8eeea0f25cb90b02dad091706",
			)),
		field.Enum("status").
			GoType(st.LeafDecompositionStatus("")).
			Default(string(st.LeafDecompositionStatusActive)).
			Comment("Decomposition lifecycle status. Only ACTIVE may contribute a delegate-path signature; CONSUMED/REVOKED/REVOKE_PENDING are terminal or in-flight-terminal."),
	}
}

// Edges are the edges for the leaf decompositions table.
func (LeafDecomposition) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("tree_node", TreeNode.Type).
			Unique().
			Required().
			Immutable().
			Comment("The leaf this decomposition is installed on."),
		edge.To("signing_keyshare", SigningKeyshare.Type).
			Unique().
			Comment("The SE2 keyshare completing the delegate path. Cleared when the keyshare is hard-deleted at revoke/consume so the row can tombstone."),
		edge.From("delegation_grant", DelegationGrant.Type).
			Ref("leaf_decompositions").
			Unique().
			Required().
			Immutable().
			Comment("The grant under which this decomposition was installed."),
	}
}

// Indexes are the indexes for the leaf decompositions table.
func (LeafDecomposition) Indexes() []ent.Index {
	return []ent.Index{
		// At most one active delegate-path decomposition per leaf. Tombstoned
		// (CONSUMED/REVOKED/REVOKE_PENDING) rows are excluded so a leaf can be
		// re-delegated after a prior decomposition is retired.
		index.Edges("tree_node").
			Unique().
			Annotations(entsql.IndexWhere("status = 'ACTIVE'")).
			StorageKey("leafdecomposition_unique_active_per_leaf"),
		index.Edges("delegation_grant"),
	}
}
