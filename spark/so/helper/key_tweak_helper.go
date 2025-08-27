package helper

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent"
)

func TweakLeafKeyUpdate(ctx context.Context, leaf *ent.TreeNode, req *pb.SendLeafKeyTweak) (*ent.TreeNodeUpdateOne, error) {
	// Tweak keyshare
	keyshare, err := leaf.QuerySigningKeyshare().First(ctx)
	if err != nil || keyshare == nil {
		return nil, fmt.Errorf("unable to load keyshare for leaf %s: %w", req.LeafId, err)
	}
	keyshareID := keyshare.ID.String()

	if req.SecretShareTweak == nil {
		return nil, fmt.Errorf("secret share tweak is not provided for leaf %s", req.LeafId)
	}

	if len(req.SecretShareTweak.Proofs) == 0 {
		return nil, fmt.Errorf("no proofs provided for secret share tweak for leaf %s", req.LeafId)
	}
	keyshare, err = keyshare.TweakKeyShare(
		ctx,
		req.SecretShareTweak.SecretShare,
		req.SecretShareTweak.Proofs[0],
		req.PubkeySharesTweak,
	)
	if err != nil || keyshare == nil {
		return nil, fmt.Errorf("unable to tweak keyshare %s for leaf %s: %w", keyshareID, req.LeafId, err)
	}

	// Update leaf
	signingPubkey, err := common.SubtractPublicKeys(leaf.VerifyingPubkey, keyshare.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to calculate new signing pubkey for leaf %s: %w", req.LeafId, err)
	}
	return leaf.Update().SetOwnerSigningPubkey(signingPubkey), nil
}
