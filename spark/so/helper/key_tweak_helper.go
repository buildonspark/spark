package helper

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/ent"
)

func TweakLeafKey(ctx context.Context, leaf *ent.TreeNode, req *pb.SendLeafKeyTweak, updatedRefundTx []byte) error {
	// Tweak keyshare
	keyshare, err := leaf.QuerySigningKeyshare().First(ctx)
	if err != nil || keyshare == nil {
		return fmt.Errorf("unable to load keyshare for leaf %s: %v", req.LeafId, err)
	}

	keyshare, err = keyshare.TweakKeyShare(
		ctx,
		req.SecretShareTweak.SecretShare,
		req.SecretShareTweak.Proofs[0],
		req.PubkeySharesTweak,
	)
	if err != nil || keyshare == nil {
		return fmt.Errorf("unable to tweak keyshare %s for leaf %s: %v", keyshare.ID.String(), req.LeafId, err)
	}

	// Update leaf
	signingPubkey, err := common.SubtractPublicKeys(leaf.VerifyingPubkey, keyshare.PublicKey)
	if err != nil {
		return fmt.Errorf("unable to calculate new signing pubkey for leaf %s: %v", req.LeafId, err)
	}
	leafMutator := leaf.
		Update().
		SetOwnerSigningPubkey(signingPubkey)
	if updatedRefundTx != nil {
		leafMutator.SetRawRefundTx(updatedRefundTx)
	}
	leaf, err = leafMutator.Save(ctx)
	if err != nil || leaf == nil {
		return fmt.Errorf("unable to update leaf %s: %v", req.LeafId, err)
	}
	return nil
}
