package multisig

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/multisig"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

// ValidateMultisigSignatures verifies that a MultisigSignatureSet contains
// enough valid signatures from distinct members of the given config to meet
// the threshold. Signatures may be Schnorr or ECDSA DER encoded.
func ValidateMultisigSignatures(config *pb.MultisigConfig, message []byte, sigSet *pb.MultisigSignatureSet) error {
	if config == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("config cannot be nil"))
	}
	if message == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("message cannot be nil"))
	}
	if sigSet == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("signature set cannot be nil"))
	}

	if sigSet.GetMultisigConfig() == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("signature set must contain multisig config"))
	}

	expectedID, err := ValidateAndComputeMultisigIdentifier(config)
	if err != nil {
		return fmt.Errorf("invalid multisig config: %w", err)
	}
	sigSetID, err := ValidateAndComputeMultisigIdentifier(sigSet.GetMultisigConfig())
	if err != nil {
		return fmt.Errorf("invalid multisig config in signature set: %w", err)
	}
	if !bytes.Equal(sigSetID, expectedID) {
		return sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("multisig config mismatch"))
	}

	seen := make(map[string]bool, len(sigSet.GetSignatures()))

	for _, sig := range sigSet.GetSignatures() {
		pubKeyHex := hex.EncodeToString(sig.GetPublicKey())
		if seen[pubKeyHex] {
			return sparkerrors.InvalidArgumentDuplicateField(fmt.Errorf("duplicate signature from %s", pubKeyHex))
		}
		seen[pubKeyHex] = true

		if !slices.ContainsFunc(config.GetPublicKeys(), func(pk []byte) bool {
			return bytes.Equal(pk, sig.GetPublicKey())
		}) {
			return sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("signer %s is not a member of the multisig", pubKeyHex))
		}

		if err := verifySignature(sig.GetPublicKey(), message, sig.GetSignature()); err != nil {
			return sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("invalid signature from %s: %w", pubKeyHex, err))
		}
	}

	if uint32(len(sigSet.GetSignatures())) < config.GetThreshold() {
		return sparkerrors.FailedPreconditionBadSignature(
			fmt.Errorf("threshold not met: got %d valid signatures, need %d", len(sigSet.GetSignatures()), config.GetThreshold()),
		)
	}

	return nil
}

// verifySignature tries Schnorr first, then falls back to ECDSA DER.
// Both formats are supported because clients may use either signing scheme.
func verifySignature(pubKeyBytes []byte, message []byte, sigBytes []byte) error {
	pubKey, err := keys.ParsePublicKey(pubKeyBytes)
	if err != nil {
		return fmt.Errorf("failed to parse public key: %w", err)
	}

	if schnorrSig, err := schnorr.ParseSignature(sigBytes); err == nil {
		if schnorrSig.Verify(message, pubKey.ToBTCEC()) {
			return nil
		}
		return fmt.Errorf("Schnorr signature verification failed")
	}

	derSig, err := ecdsa.ParseDERSignature(sigBytes)
	if err != nil {
		return fmt.Errorf("failed to parse signature as either Schnorr or DER: %w", err)
	}
	if !derSig.Verify(message, pubKey.ToBTCEC()) {
		return fmt.Errorf("signature verification failed")
	}
	return nil
}
