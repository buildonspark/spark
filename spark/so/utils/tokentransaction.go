package utils

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
)

// hashTokenTransaction generates a SHA256 hash of the TokenTransaction by:
// 1. Taking SHA256 of each field individually
// 2. Concatenating all field hashes in order
// 3. Taking SHA256 of the concatenated hashes
// If partialHash is true generate a partial hash even if the provided transaction is final.
func HashTokenTransaction(tokenTransaction *pb.TokenTransaction, partialHash bool) ([]byte, error) {
	if tokenTransaction == nil {
		return nil, fmt.Errorf("token transaction cannot be nil")
	}

	h := sha256.New()
	var allHashes []byte

	// Hash input leaves if a transfer.
	if transferSource := tokenTransaction.GetTransferInput(); transferSource != nil {
		for _, leaf := range transferSource.GetLeavesToSpend() {
			h.Reset()
			if leaf.GetPrevTokenTransactionHash() != nil {
				h.Write(leaf.GetPrevTokenTransactionHash())
			}
			buf := make([]byte, 4)
			binary.BigEndian.PutUint32(buf, leaf.GetPrevTokenTransactionLeafVout())
			h.Write(buf)
			allHashes = append(allHashes, h.Sum(nil)...)
		}
	}
	// Hash mint input if a mint
	if mintInput := tokenTransaction.GetMintInput(); mintInput != nil {
		h.Reset()
		if mintInput.GetIssuerPublicKey() != nil {
			h.Write(mintInput.GetIssuerPublicKey())
		}
		allHashes = append(allHashes, h.Sum(nil)...)
	}

	// Hash output leaves
	for _, leaf := range tokenTransaction.OutputLeaves {
		h.Reset()
		// Leaf ID is not set in the partial token transaction.
		if leaf.GetId() != "" && !partialHash {
			h.Write([]byte(leaf.GetId()))
		}
		if leaf.GetOwnerPublicKey() != nil {
			h.Write(leaf.GetOwnerPublicKey())
		}
		// Revocation public key is not set in the partial token transaction.
		if leaf.GetRevocationPublicKey() != nil && !partialHash {
			h.Write(leaf.GetRevocationPublicKey())
		}

		withdrawalBondBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(withdrawalBondBytes, leaf.GetWithdrawalBondSats())
		h.Write(withdrawalBondBytes)

		withdrawalLocktimeBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(withdrawalLocktimeBytes, leaf.GetWithdrawalLocktime())
		h.Write(withdrawalLocktimeBytes)

		if leaf.GetTokenPublicKey() != nil {
			h.Write(leaf.GetTokenPublicKey())
		}
		if leaf.GetTokenAmount() != nil {
			h.Write(leaf.GetTokenAmount())
		}
		allHashes = append(allHashes, h.Sum(nil)...)
	}

	operatorPublicKeys := tokenTransaction.GetSparkOperatorIdentityPublicKeys()
	sort.Slice(operatorPublicKeys, func(i, j int) bool {
		return bytes.Compare(operatorPublicKeys[i], operatorPublicKeys[j]) < 0
	})

	// Hash spark operator identity public keys
	for _, pubKey := range operatorPublicKeys {
		h.Reset()
		if pubKey != nil {
			h.Write(pubKey)
		}
		allHashes = append(allHashes, h.Sum(nil)...)
	}

	// Final hash of all concatenated hashes
	h.Reset()
	h.Write(allHashes)
	return h.Sum(nil), nil
}

func HashOperatorSpecificTokenTransactionSignablePayload(payload *pb.OperatorSpecificTokenTransactionSignablePayload) ([]byte, error) {
	if payload == nil {
		return nil, fmt.Errorf("revocation keyshare signable payload cannot be nil")
	}

	h := sha256.New()
	var allHashes []byte

	// Hash final_token_transaction_hash
	h.Reset()
	if payload.GetFinalTokenTransactionHash() != nil {
		h.Write(payload.GetFinalTokenTransactionHash())
	}
	allHashes = append(allHashes, h.Sum(nil)...)

	// Hash operator_identity_public_key
	h.Reset()
	if payload.GetOperatorIdentityPublicKey() != nil {
		h.Write(payload.GetOperatorIdentityPublicKey())
	}
	allHashes = append(allHashes, h.Sum(nil)...)

	// Final hash of all concatenated hashes
	h.Reset()
	h.Write(allHashes)
	return h.Sum(nil), nil
}

// TODO: Extend to validate the full token transaction after filling revocation keys.
// ValidatePartialTokenTransactionStartRequest validates a partial token transaction start request without revocation keys.
func ValidatePartialTokenTransaction(
	tokenTransaction *pb.TokenTransaction,
	tokenTransactionSignatures *pb.TokenTransactionSignatures,
	sparkOperatorsFromConfig map[string]*pb.SigningOperatorInfo,
) error {
	if tokenTransaction == nil {
		return fmt.Errorf("token transaction cannot be nil")
	}
	if tokenTransaction.OutputLeaves == nil {
		return fmt.Errorf("leaves to create cannot be nil")
	}
	if len(tokenTransaction.OutputLeaves) == 0 {
		return fmt.Errorf("leaves to create cannot be empty")
	}

	// Validate all output leaves have the same token public key
	expectedTokenPubKey := tokenTransaction.OutputLeaves[0].GetTokenPublicKey()
	if expectedTokenPubKey == nil {
		return fmt.Errorf("token public key cannot be nil")
	}
	for _, leaf := range tokenTransaction.OutputLeaves {
		if leaf.GetTokenPublicKey() == nil {
			return fmt.Errorf("token public key cannot be nil")
		}
		if !bytes.Equal(leaf.GetTokenPublicKey(), expectedTokenPubKey) {
			return fmt.Errorf("all leaves must have the same token public key")
		}
	}

	// Validate that the transaction is either a mint or a transfer, but not both.
	hasMintInput := tokenTransaction.GetMintInput() != nil
	hasTransferInput := tokenTransaction.GetTransferInput() != nil
	if (!hasMintInput && !hasTransferInput) || (hasMintInput && hasTransferInput) {
		return fmt.Errorf("token transaction must have exactly one of issue_input or transfer_input")
	}

	// Validation for mint transactions.
	if mintInput := tokenTransaction.GetMintInput(); mintInput != nil {
		// Validate that the token public key on all created leaves
		// matches the issuer public key.
		if !bytes.Equal(mintInput.GetIssuerPublicKey(), expectedTokenPubKey) {
			return fmt.Errorf("token public key must match issuer public key for mint transactions")
		}

		// Validate that the transaction signatures match the issuer public key.
		if len(tokenTransactionSignatures.GetOwnerSignatures()) != 1 {
			return fmt.Errorf("mint transactions must have exactly one signature")
		}

		// Get the signature for the mint input.
		issueSignature := tokenTransactionSignatures.GetOwnerSignatures()[0]
		if issueSignature == nil {
			return fmt.Errorf("mint signature cannot be nil")
		}
	}

	// Validation for transfer transactions
	if transferSource := tokenTransaction.GetTransferInput(); transferSource != nil {
		// Validate there is the correct number of signatures for leaves to spend.
		if len(tokenTransactionSignatures.GetOwnerSignatures()) != len(transferSource.GetLeavesToSpend()) {
			return fmt.Errorf("number of signatures must match number of leaves to spend")
		}
	}

	// Check that each operator's public key is present.
	for _, operatorInfoFromConfig := range sparkOperatorsFromConfig {
		found := false
		configPubKey := operatorInfoFromConfig.GetPublicKey()
		for _, pubKey := range tokenTransaction.GetSparkOperatorIdentityPublicKeys() {
			if bytes.Equal(pubKey, configPubKey) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("missing spark operator identity public key for operator %s", operatorInfoFromConfig.GetIdentifier())
		}
	}

	return nil
}

func ValidateFinalTokenTransaction(
	tokenTransaction *pb.TokenTransaction,
	tokenTransactionSignatures *pb.TokenTransactionSignatures,
	expectedRevocationPublicKeys [][]byte,
	sparkOperatorsFromConfig map[string]*pb.SigningOperatorInfo,
) error {
	// Repeat same validations as for the partial token transaction.
	err := ValidatePartialTokenTransaction(tokenTransaction, tokenTransactionSignatures, sparkOperatorsFromConfig)
	if err != nil {
		return fmt.Errorf("failed to validate final token transaction: %w", err)
	}

	// Additionally validate the revocation public keys which were added to make it final.
	for i, leaf := range tokenTransaction.OutputLeaves {
		if leaf.GetRevocationPublicKey() == nil {
			return fmt.Errorf("revocation public key cannot be nil for leaf %d", i)
		}
		if !bytes.Equal(leaf.GetRevocationPublicKey(), expectedRevocationPublicKeys[i]) {
			return fmt.Errorf("revocation public key mismatch for leaf %d", i)
		}
	}
	return nil
}

// ValidateOwnershipSignature validates the ownership signature of a token transaction and that it matches
// a predefined public key attached to the leaf being spent or the token being created and the submitted transaction.
func ValidateOwnershipSignature(ownershipSignature []byte, partialTokenTransactionHash []byte, ownerPublicKey []byte) error {
	if ownershipSignature == nil {
		return fmt.Errorf("ownership signature cannot be nil")
	}
	if partialTokenTransactionHash == nil {
		return fmt.Errorf("partial token transaction hash cannot be nil")
	}

	sig, _ := ecdsa.ParseDERSignature(ownershipSignature)
	pubKey, err := secp256k1.ParsePubKey(ownerPublicKey)
	if err != nil {
		return fmt.Errorf("failed to parse public key: %w", err)
	}

	if !sig.Verify(partialTokenTransactionHash, pubKey) {
		return fmt.Errorf("invalid ownership signature")
	}
	return nil
}

func ValidateRevocationKeys(revocationPrivateKeys [][]byte, expectedRevocationPublicKeys [][]byte) error {
	if len(expectedRevocationPublicKeys) != len(revocationPrivateKeys) {
		return fmt.Errorf("number of revocation private keys does not match number of leaves to spend")
	}

	for i, revocationPrivateKeyBytes := range revocationPrivateKeys {
		revocationKey := secp256k1.PrivKeyFromBytes(revocationPrivateKeyBytes)
		revocationPubKey := revocationKey.PubKey()
		expectedRevocationPubKey, err := secp256k1.ParsePubKey(expectedRevocationPublicKeys[i])
		if err != nil {
			return fmt.Errorf("failed to parse revocation private key: %w", err)
		}
		if !expectedRevocationPubKey.IsEqual(revocationPubKey) {
			return fmt.Errorf("recovered secret for leaf %d does not match leaf public key", i)
		}
	}
	return nil
}
