package tokens

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/multisig"
	multisigpb "github.com/lightsparkdev/spark/proto/multisig"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/utils"
)

// validateDeprecatedSignatureConsistency checks that the deprecated signature
// field is not set when the authority_signatures oneof is populated.
func validateDeprecatedSignatureConsistency(sig *tokenpb.SignatureWithIndex) error {
	if len(sig.Signature) == 0 {
		return nil
	}
	switch sig.AuthoritySignatures.(type) {
	case *tokenpb.SignatureWithIndex_MultisigSignatures, *tokenpb.SignatureWithIndex_SingleSignature:
		return sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("deprecated signature field must not be set when authority_signatures oneof is populated"))
	}
	return nil
}

// ValidateOwnershipSignatureFromAuthority validates the signature in a
// SignatureWithIndex against the given public key. It handles the
// authority_signatures oneof (single_signature or multisig_signatures)
// with backwards-compatible fallback to the deprecated signature field.
//
// For single-key signatures, ownerPublicKey is used for verification.
// For multisig signatures, the config embedded in the request is used
// for pure cryptographic validation. Callers are responsible for
// verifying the config is authorized for the entity being operated on
// (e.g. via entity edges).
func ValidateOwnershipSignatureFromAuthority(
	ctx context.Context,
	sig *tokenpb.SignatureWithIndex,
	hash []byte,
	ownerPublicKey keys.Public,
) error {
	if err := validateDeprecatedSignatureConsistency(sig); err != nil {
		return err
	}
	switch v := sig.AuthoritySignatures.(type) {
	case *tokenpb.SignatureWithIndex_SingleSignature:
		pubKeyBytes := v.SingleSignature.GetPublicKey()
		if len(pubKeyBytes) == 0 {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("single_signature must include a public key"))
		}
		claimedKey, err := keys.ParsePublicKey(pubKeyBytes)
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid public key in single_signature: %w", err))
		}
		if !claimedKey.Equals(ownerPublicKey) {
			return sparkerrors.FailedPreconditionBadSignature(
				fmt.Errorf("single_signature public key does not match output owner"))
		}
		return utils.ValidateOwnershipSignature(v.SingleSignature.GetSignature(), hash, ownerPublicKey)
	case *tokenpb.SignatureWithIndex_MultisigSignatures:
		return validateMultisigFromProvidedConfig(hash, v.MultisigSignatures)
	default:
		// Backwards compat: fall back to deprecated signature field.
		if len(sig.Signature) > 0 {
			return utils.ValidateOwnershipSignature(sig.Signature, hash, ownerPublicKey)
		}
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("no signature provided in SignatureWithIndex"))
	}
}

// validateMultisigFromProvidedConfig validates multisig signatures using
// the MultisigConfig embedded in the signature set, without a DB lookup.
// Authorization (verifying the config is the correct authority for the
// entity) must be enforced by callers, e.g. via entity edge checks.
func validateMultisigFromProvidedConfig(hash []byte, sigSet *multisigpb.MultisigSignatureSet) error {
	if sigSet == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("multisig signature set cannot be nil"))
	}
	if sigSet.MultisigConfig == nil {
		return sparkerrors.InvalidArgumentMissingField(
			fmt.Errorf("multisig signature set must contain multisig config"))
	}
	return multisig.ValidateMultisigSignatures(sigSet.MultisigConfig, hash, sigSet)
}
