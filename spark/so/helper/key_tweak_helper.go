package helper

import (
	"context"
	"fmt"
	"math/big"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

// ValidatePubkeySharesTweak checks that every entry in `pubKeySharesTweak`
// equals the public-group evaluation of the polynomial committed to by
// `proofs` at the polynomial index of the operator identified by that key.
//
// Without this check the sender or claimer can hand each SO a per-id
// `pubkey_shares_tweak` map whose entries come from a *different* polynomial
// g' that shares only the constant term with the polynomial g committed by
// `proofs`. Every per-SO local validation
// (`secretsharing.ValidateShare`, cross-SO `ValidateKeyTweakProof`) still
// passes — but `(*SigningKeyshare).TweakKeyShare` blindly applies the supplied
// map to `signing_keyshares.public_shares`, leaving SOs with divergent
// polynomial commitments for the same indices. FROST signing on the leaf
// thereafter fails downstream with
// "calculated R point was not given R" (adaptor_signature.rs).
//
// The check is symmetric for sender (`SendLeafKeyTweak.pubkey_shares_tweak`)
// and receiver (`ClaimLeafKeyTweak.pubkey_shares_tweak`) since both encode
// the same wire shape and feed the same `TweakKeyShare` code path.
//
// Operator polynomial indices come from `config.SigningOperatorMap`:
// operator at array slot N has share index N+1 (1-based, matching
// `secretsharing.SplitSecretWithProofs`).
func ValidatePubkeySharesTweak(config *so.Config, proofs [][]byte, pubKeySharesTweak map[string]keys.Public) error {
	if config == nil {
		return fmt.Errorf("config must not be nil")
	}
	if len(proofs) == 0 {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("proofs must not be empty"))
	}
	if len(pubKeySharesTweak) != len(config.SigningOperatorMap) {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf(
			"pubkey_shares_tweak has %d entries, expected one per operator (%d)",
			len(pubKeySharesTweak), len(config.SigningOperatorMap),
		))
	}
	fieldModulus := secp256k1.S256().N
	for identifier, operator := range config.SigningOperatorMap {
		provided, ok := pubKeySharesTweak[identifier]
		if !ok {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf(
				"pubkey_shares_tweak missing entry for operator %s", identifier,
			))
		}
		index := new(big.Int).SetUint64(operator.ID)
		index.Add(index, big.NewInt(1))
		expected, err := secretsharing.EvaluatePolynomialCommitment(proofs, index, fieldModulus)
		if err != nil {
			return fmt.Errorf("evaluate polynomial commitment at index for operator %s: %w", identifier, err)
		}
		if !provided.Equals(expected) {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf(
				"pubkey_shares_tweak entry for operator %s is inconsistent with secret_share_tweak proofs",
				identifier,
			))
		}
	}
	return nil
}

// TweakLeafKeyUpdate applies the sender-side key tweak from `req` to the
// leaf's underlying signing keyshare and returns the resulting tree-node
// owner-signing-pubkey update.
//
// `config` is required to validate that `req.PubkeySharesTweak` is consistent
// with `req.SecretShareTweak.Proofs` across every operator in the cluster.
// Without that cross-field check, the wire map can be crafted per-SO so that
// every SO accepts the request but their `signing_keyshares.public_shares`
// rows diverge — see ValidatePubkeySharesTweak.
func TweakLeafKeyUpdate(ctx context.Context, config *so.Config, leaf *ent.TreeNode, req *pb.SendLeafKeyTweak) (*ent.TreeNodeUpdateOne, error) {
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
	if err := ValidatePubkeySharesTweak(config, req.SecretShareTweak.Proofs, pubKeySharesTweak); err != nil {
		return nil, fmt.Errorf("invalid pubkey_shares_tweak for leaf %s: %w", req.LeafId, err)
	}
	keyshare, err = keyshare.TweakKeyShare(ctx, secretShare, pubKeyTweak, pubKeySharesTweak)
	if err != nil || keyshare == nil {
		return nil, fmt.Errorf("unable to tweak keyshare %s for leaf %s: %w", keyshareID, req.LeafId, err)
	}

	signingPubkey := leaf.VerifyingPubkey.Sub(keyshare.PublicKey)
	return leaf.Update().SetOwnerSigningPubkey(signingPubkey), nil
}
