package helper

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

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
	secretShare, err := keys.ParsePrivateKey(req.SecretShareTweak.SecretShare)
	if err != nil {
		return nil, fmt.Errorf("unable to parse secret share: %w", err)
	}
	pubKeyTweak, err := keys.ParsePublicKey(req.SecretShareTweak.Proofs[0])
	if err != nil {
		return nil, fmt.Errorf("unable to parse public key: %w", err)
	}
	pubKeySharesTweak, err := keys.ParsePublicKeyMap(req.PubkeySharesTweak)
	if err != nil {
		return nil, fmt.Errorf("unable to parse public key shares tweaks: %w", err)
	}
	keyshare, err = keyshare.TweakKeyShare(ctx, secretShare, pubKeyTweak, pubKeySharesTweak)
	if err != nil || keyshare == nil {
		return nil, fmt.Errorf("unable to tweak keyshare %s for leaf %s: %w", keyshareID, req.LeafId, err)
	}

	verifyingPubKey, err := keys.ParsePublicKey(leaf.VerifyingPubkey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse verifying public key: %w", err)
	}
	tweakedPubKey, err := keys.ParsePublicKey(keyshare.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse tweaked public key: %w", err)
	}

	// Update leaf
	signingPubkey := verifyingPubKey.Sub(tweakedPubKey)
	return leaf.Update().SetOwnerSigningPubkey(signingPubkey.Serialize()), nil
}
