package tokens

import (
	"bytes"
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
	if len(sig.GetSignature()) == 0 {
		return nil
	}
	switch sig.GetAuthoritySignatures().(type) {
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
// For multisig signatures, the embedded config must satisfy its threshold and
// the signature set must include ownerPublicKey, binding the presented multisig
// authority back to the stored owner or issuer key.
func ValidateOwnershipSignatureFromAuthority(
	ctx context.Context,
	sig *tokenpb.SignatureWithIndex,
	hash []byte,
	ownerPublicKey keys.Public,
) error {
	if err := validateDeprecatedSignatureConsistency(sig); err != nil {
		return err
	}
	switch v := sig.GetAuthoritySignatures().(type) {
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
		if err := validateMultisigFromProvidedConfig(hash, v.MultisigSignatures); err != nil {
			return err
		}
		return validateMultisigIncludesOwnerSignature(v.MultisigSignatures, ownerPublicKey)
	default:
		// Backwards compat: fall back to deprecated signature field.
		if len(sig.GetSignature()) > 0 {
			return utils.ValidateOwnershipSignature(sig.GetSignature(), hash, ownerPublicKey)
		}
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("no signature provided in SignatureWithIndex"))
	}
}

// validateMultisigFromProvidedConfig validates multisig signatures using
// the MultisigConfig embedded in the signature set, without a DB lookup.
// This is pure cryptographic validation of the set against its own config;
// binding the config to the entity being authorized is enforced by
// validateMultisigIncludesOwnerSignature in ValidateOwnershipSignatureFromAuthority.
func validateMultisigFromProvidedConfig(hash []byte, sigSet *multisigpb.MultisigSignatureSet) error {
	if sigSet == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("multisig signature set cannot be nil"))
	}
	if sigSet.GetMultisigConfig() == nil {
		return sparkerrors.InvalidArgumentMissingField(
			fmt.Errorf("multisig signature set must contain multisig config"))
	}
	return multisig.ValidateMultisigSignatures(sigSet.GetMultisigConfig(), hash, sigSet)
}

// validateMultisigIncludesOwnerSignature requires the multisig signature set
// to contain a signature from ownerPublicKey, binding the request-supplied
// multisig config to the stored owner/issuer key. It must run after
// validateMultisigFromProvidedConfig, which verifies that every signature in
// the set is cryptographically valid and from a distinct config member, so
// presence of the owner key here implies a valid owner signature.
func validateMultisigIncludesOwnerSignature(sigSet *multisigpb.MultisigSignatureSet, ownerPublicKey keys.Public) error {
	if ownerPublicKey.IsZero() {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("owner public key cannot be zero"))
	}
	ownerPublicKeyBytes := ownerPublicKey.Serialize()
	for _, sig := range sigSet.GetSignatures() {
		if sig == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("multisig signature cannot be nil"))
		}
		if !bytes.Equal(sig.GetPublicKey(), ownerPublicKeyBytes) {
			continue
		}
		return nil
	}
	return sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("multisig signature set must include a valid signature from the owner public key"))
}
