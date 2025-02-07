package utils

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	pb "github.com/lightsparkdev/spark-go/proto/spark"
)

// hashTokenTransaction generates a SHA256 hash of the TokenTransaction by:
// 1. Taking SHA256 of each field individually
// 2. Concatenating all field hashes in order
// 3. Taking SHA256 of the concatenated hashes
func HashTokenTransaction(tokenTransaction *pb.TokenTransaction) ([]byte, error) {
	if tokenTransaction == nil {
		return nil, fmt.Errorf("token transaction cannot be nil")
	}

	h := sha256.New()
	var allHashes []byte

	// Hash input leaves if a transfer appropriate leaves based on the token source type
	if transferSource := tokenTransaction.GetTransferInput(); transferSource != nil {
		// Hash leaves_to_spend
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
	// Hash input issuance if an issuance.
	if issueInput := tokenTransaction.GetIssueInput(); issueInput != nil {
		h.Reset()
		if issueInput.GetIssuerPublicKey() != nil {
			h.Write(issueInput.GetIssuerPublicKey())
		}
		allHashes = append(allHashes, h.Sum(nil)...)
	}

	// Hash output leaves
	for _, leaf := range tokenTransaction.OutputLeaves {
		h.Reset()
		if leaf.GetId() != "" {
			h.Write([]byte(leaf.GetId()))
		}
		if leaf.GetOwnerPublicKey() != nil {
			h.Write(leaf.GetOwnerPublicKey())
		}
		if leaf.GetRevocationPublicKey() != nil {
			h.Write(leaf.GetRevocationPublicKey())
		}
		binary.BigEndian.PutUint64(make([]byte, 8), leaf.GetWithdrawalBondSats())
		binary.BigEndian.PutUint64(make([]byte, 8), leaf.GetWithdrawalLocktime())
		if leaf.GetTokenPublicKey() != nil {
			h.Write(leaf.GetTokenPublicKey())
		}
		if leaf.GetTokenAmount() != nil {
			h.Write(leaf.GetTokenAmount())
		}
		allHashes = append(allHashes, h.Sum(nil)...)
	}

	// Hash spark operator identity public keys
	for _, pubKey := range tokenTransaction.GetSparkOperatorIdentityPublicKeys() {
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

	// Validation for issuance transactions
	if issueInput := tokenTransaction.GetIssueInput(); issueInput != nil {
		// Validate that the token public key on all created leaves
		// matches the issuer public key.
		if !bytes.Equal(issueInput.GetIssuerPublicKey(), expectedTokenPubKey) {
			return fmt.Errorf("token public key must match issuer public key for issuance transactions")
		}

		// Validate that the transaction signatures match the issuer public key.
		if len(tokenTransactionSignatures.GetOwnerSignatures()) != 1 {
			return fmt.Errorf("issuance transactions must have exactly one signature")
		}

		// Get the signature for the issuance input
		issueSignature := tokenTransactionSignatures.GetOwnerSignatures()[0]
		if issueSignature == nil {
			return fmt.Errorf("issuance signature cannot be nil")
		}
	}

	// Validation for transfer transactions
	if transferSource := tokenTransaction.GetTransferInput(); transferSource != nil {
		// Validate there is the correct number of signatures for leaves to spend.
		if len(tokenTransactionSignatures.GetOwnerSignatures()) != len(transferSource.GetLeavesToSpend()) {
			return fmt.Errorf("number of signatures must match number of leaves to spend")
		}

		for i := range transferSource.GetLeavesToSpend() {
			signature := tokenTransactionSignatures.GetOwnerSignatures()[i]
			if signature == nil {
				return fmt.Errorf("transfer signature cannot be nil")
			}
		}
	}

	// Check that each operator's public key is present
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
	sparkOperatorsFromConfig map[string]*pb.SigningOperatorInfo,
	expectedOutputLeafRevocationPublicKeys [][]byte,
) error {
	// Repeat same validations as for the partial token transaction.
	err := ValidatePartialTokenTransaction(tokenTransaction, tokenTransactionSignatures, sparkOperatorsFromConfig)
	if err != nil {
		return fmt.Errorf("failed to validate final token transaction: %w", err)
	}

	// Additionally validate the revocation public keys which were added to make it final.
	seenRevocationKeys := make(map[string]bool)
	for i, leaf := range tokenTransaction.OutputLeaves {
		if leaf.GetRevocationPublicKey() == nil {
			return fmt.Errorf("revocation public key cannot be nil for leaf %d", i)
		}
		revKeyStr := string(leaf.GetRevocationPublicKey())
		if seenRevocationKeys[revKeyStr] {
			return fmt.Errorf("duplicate revocation public key found for leaf %d", i)
		}
		if !bytes.Equal(leaf.GetRevocationPublicKey(), expectedOutputLeafRevocationPublicKeys[i]) {
			return fmt.Errorf("revocation public key does not match expected for leaf %d", i)
		}
		seenRevocationKeys[revKeyStr] = true
	}
	return nil
}
