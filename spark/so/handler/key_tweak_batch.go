package handler

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
)

// rotateLeafKeyshares rotates every keyshare in tweaks via one batched
// ent.TweakSigningKeyshares call. leafIDByKeyshareID names the tree node
// owning each keyshare so a rotation failure is attributed to the leaf an
// operator would look up.
func rotateLeafKeyshares(
	ctx context.Context,
	transferID uuid.UUID,
	tweaks []*ent.SigningKeyshareTweak,
	leafIDByKeyshareID map[uuid.UUID]uuid.UUID,
) (map[uuid.UUID]*ent.SigningKeyshare, error) {
	rotated, err := ent.TweakSigningKeyshares(ctx, tweaks)
	if err != nil {
		var rotationErr *ent.KeyshareRotationError
		if errors.As(err, &rotationErr) {
			if leafID, ok := leafIDByKeyshareID[rotationErr.KeyshareID]; ok {
				return nil, fmt.Errorf("unable to tweak keyshare %s for leaf %s in transfer %s: %w",
					rotationErr.KeyshareID, leafID, transferID, err)
			}
		}
		return nil, fmt.Errorf("unable to tweak keyshares for transfer %s: %w", transferID, err)
	}
	return rotated, nil
}

// treeNodeOwnerKeyUpdate carries one tree node's post-rotation owner keys.
// The node must have its Tree and SigningKeyshare edges loaded, and its row
// must already exist — the upsert in applyTreeNodeOwnerKeyUpdates relies on
// the conflict path always taking the UPDATE branch.
type treeNodeOwnerKeyUpdate struct {
	node               *ent.TreeNode
	ownerSigningPubkey keys.Public
	// ownerIdentityPubkey is written only when applyTreeNodeOwnerKeyUpdates
	// is called with updateOwnerIdentity (receiver claims change ownership;
	// sender tweaks do not).
	ownerIdentityPubkey keys.Public
}

// applyTreeNodeOwnerKeyUpdates persists post-rotation owner keys for all
// nodes with chunked CreateBulk+OnConflict upserts — ent's bulk-UPDATE
// workaround. Chunking respects PostgreSQL's 65535-parameter limit.
func applyTreeNodeOwnerKeyUpdates(ctx context.Context, db *ent.Client, transferID uuid.UUID, updates []treeNodeOwnerKeyUpdate, updateOwnerIdentity bool) error {
	builders := make([]*ent.TreeNodeCreate, 0, len(updates))
	for _, update := range updates {
		ownerIdentity := update.node.OwnerIdentityPubkey
		if updateOwnerIdentity {
			ownerIdentity = update.ownerIdentityPubkey
		}
		// Builders set ID (for conflict matching), all required fields, and
		// the fields being updated.
		builders = append(builders, db.TreeNode.Create().
			SetID(update.node.ID).
			SetTree(update.node.Edges.Tree).
			SetNetwork(update.node.Edges.Tree.Network).
			SetSigningKeyshare(update.node.Edges.SigningKeyshare).
			SetValue(update.node.Value).
			SetVerifyingPubkey(update.node.VerifyingPubkey).
			SetOwnerIdentityPubkey(ownerIdentity).
			SetOwnerSigningPubkey(update.ownerSigningPubkey).
			SetRawTx(update.node.RawTx).
			SetVout(update.node.Vout).
			SetStatus(update.node.Status))
	}
	const maxBatchSize = 1000
	for chunk := range slices.Chunk(builders, maxBatchSize) {
		if err := db.TreeNode.CreateBulk(chunk...).
			OnConflictColumns(enttreenode.FieldID).
			Update(func(u *ent.TreeNodeUpsert) {
				// Status is intentionally excluded from the update set: an
				// exited-to-L1 leaf keeps its on-chain status, and a stale
				// read must not revive it.
				u.UpdateOwnerSigningPubkey()
				if updateOwnerIdentity {
					u.UpdateOwnerIdentityPubkey()
				}
				// Carried explicitly: custom upsert resolvers bypass ent's
				// UpdateDefault.
				u.UpdateUpdateTime()
			}).
			Exec(ctx); err != nil {
			return fmt.Errorf("unable to batch update tree node keys for transfer %s: %w", transferID, err)
		}
	}
	return nil
}
