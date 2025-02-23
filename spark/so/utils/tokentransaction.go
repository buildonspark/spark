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

// MaxOutputLeaves defines the maximum number of input or output leaves allowed in a token transaction.
const MaxInputOrOutputTokenTransactionLeaves = 100

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
		if transferSource.LeavesToSpend == nil {
			return nil, fmt.Errorf("transfer input leaves cannot be nil")
		}
		for i, leaf := range transferSource.GetLeavesToSpend() {
			if leaf == nil {
				return nil, fmt.Errorf("transfer input leaf at index %d cannot be nil", i)
			}
			h.Reset()

			txHash := leaf.GetPrevTokenTransactionHash()
			if txHash != nil {
				if len(txHash) != 32 {
					return nil, fmt.Errorf("invalid previous transaction hash length at index %d: expected 32 bytes, got %d", i, len(txHash))
				}
				h.Write(txHash)
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
		pubKey := mintInput.GetIssuerPublicKey()
		if pubKey != nil {
			if len(pubKey) == 0 {
				return nil, fmt.Errorf("issuer public key cannot be empty")
			}
			h.Write(pubKey)
		}

		if mintInput.GetIssuerProvidedTimestamp() != 0 {
			nonceBytes := make([]byte, 8)
			binary.LittleEndian.PutUint64(nonceBytes, mintInput.GetIssuerProvidedTimestamp())
			h.Write(nonceBytes)
		}

		allHashes = append(allHashes, h.Sum(nil)...)
	}

	// Hash output leaves
	if tokenTransaction.OutputLeaves == nil {
		return nil, fmt.Errorf("output leaves cannot be nil")
	}
	for i, leaf := range tokenTransaction.OutputLeaves {
		if leaf == nil {
			return nil, fmt.Errorf("output leaf at index %d cannot be nil", i)
		}
		h.Reset()

		// Leaf ID is not set in the partial token transaction.
		if !partialHash && leaf.GetId() != "" {
			id := []byte(leaf.GetId())
			if len(id) == 0 {
				return nil, fmt.Errorf("leaf ID at index %d cannot be empty", i)
			}
			h.Write(id)
		}

		ownerPubKey := leaf.GetOwnerPublicKey()
		if ownerPubKey != nil {
			if len(ownerPubKey) == 0 {
				return nil, fmt.Errorf("owner public key at index %d cannot be empty", i)
			}
			h.Write(ownerPubKey)
		}

		// Revocation public key is not set in the partial token transaction.
		if !partialHash {
			revPubKey := leaf.GetRevocationPublicKey()
			if revPubKey != nil {
				if len(revPubKey) == 0 {
					return nil, fmt.Errorf("revocation public key at index %d cannot be empty", i)
				}
				h.Write(revPubKey)
			}

			withdrawalBondBytes := make([]byte, 8)
			binary.BigEndian.PutUint64(withdrawalBondBytes, leaf.GetWithdrawBondSats())
			h.Write(withdrawalBondBytes)

			withdrawalLocktimeBytes := make([]byte, 8)
			binary.BigEndian.PutUint64(withdrawalLocktimeBytes, leaf.GetWithdrawRelativeBlockLocktime())
			h.Write(withdrawalLocktimeBytes)
		}

		tokenPubKey := leaf.GetTokenPublicKey()
		if tokenPubKey != nil {
			if len(tokenPubKey) == 0 {
				return nil, fmt.Errorf("token public key at index %d cannot be empty", i)
			}
			h.Write(tokenPubKey)
		}

		tokenAmount := leaf.GetTokenAmount()
		if tokenAmount != nil {
			if len(tokenAmount) == 0 {
				return nil, fmt.Errorf("token amount at index %d cannot be empty", i)
			}
			if len(tokenAmount) > 16 {
				return nil, fmt.Errorf("token amount at index %d exceeds maximum length: got %d bytes, max 16", i, len(tokenAmount))
			}
			h.Write(tokenAmount)
		}

		allHashes = append(allHashes, h.Sum(nil)...)
	}

	operatorPublicKeys := tokenTransaction.GetSparkOperatorIdentityPublicKeys()
	if operatorPublicKeys == nil {
		return nil, fmt.Errorf("operator public keys cannot be nil")
	}

	// Sort operator keys for consistent hashing
	sort.Slice(operatorPublicKeys, func(i, j int) bool {
		return bytes.Compare(operatorPublicKeys[i], operatorPublicKeys[j]) < 0
	})

	// Hash spark operator identity public keys
	for i, pubKey := range operatorPublicKeys {
		if pubKey == nil {
			return nil, fmt.Errorf("operator public key at index %d cannot be nil", i)
		}
		if len(pubKey) == 0 {
			return nil, fmt.Errorf("operator public key at index %d cannot be empty", i)
		}
		h.Reset()
		h.Write(pubKey)
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

func HashFreezeTokensPayload(payload *pb.FreezeTokensPayload) ([]byte, error) {
	if payload == nil {
		return nil, fmt.Errorf("revocation keyshare signable payload cannot be nil")
	}

	h := sha256.New()
	var allHashes []byte

	h.Reset()
	if payload.GetOwnerPublicKey() != nil {
		h.Write(payload.GetOwnerPublicKey())
	}
	allHashes = append(allHashes, h.Sum(nil)...)

	h.Reset()
	if payload.GetTokenPublicKey() != nil {
		h.Write(payload.GetTokenPublicKey())
	}
	allHashes = append(allHashes, h.Sum(nil)...)

	h.Reset()
	if payload.GetShouldUnfreeze() {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
	allHashes = append(allHashes, h.Sum(nil)...)

	h.Reset()
	if payload.GetIssuerProvidedTimestamp() != 0 {
		nonceBytes := make([]byte, 8)
		binary.LittleEndian.PutUint64(nonceBytes, payload.IssuerProvidedTimestamp)
		h.Write(nonceBytes)
	}
	allHashes = append(allHashes, h.Sum(nil)...)

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
	if len(tokenTransaction.OutputLeaves) > MaxInputOrOutputTokenTransactionLeaves {
		return fmt.Errorf("too many output leaves, maximum is %d", MaxInputOrOutputTokenTransactionLeaves)
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
		if mintInput.GetIssuerProvidedTimestamp() == 0 {
			return fmt.Errorf("issuer provided timestamp cannot be 0")
		}

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
		if len(transferSource.GetLeavesToSpend()) == 0 {
			return fmt.Errorf("leaves to spend cannot be empty")
		}
		if len(tokenTransaction.GetTransferInput().LeavesToSpend) > MaxInputOrOutputTokenTransactionLeaves {
			return fmt.Errorf("too many leaves to spend, maximum is %d", MaxInputOrOutputTokenTransactionLeaves)
		}

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

// ValidateRevocationKeys validates that the provided revocation private keys correspond to the expected public keys.
// It ensures the private keys can correctly derive the expected public keys, preventing key mismatches.
func ValidateRevocationKeys(revocationPrivateKeys [][]byte, expectedRevocationPublicKeys [][]byte) error {
	if revocationPrivateKeys == nil {
		return fmt.Errorf("revocation private keys cannot be nil")
	}
	if expectedRevocationPublicKeys == nil {
		return fmt.Errorf("expected revocation public keys cannot be nil")
	}
	if len(expectedRevocationPublicKeys) != len(revocationPrivateKeys) {
		return fmt.Errorf("number of revocation private keys (%d) does not match number of expected public keys (%d)",
			len(revocationPrivateKeys), len(expectedRevocationPublicKeys))
	}

	for i, revocationPrivateKeyBytes := range revocationPrivateKeys {
		if revocationPrivateKeyBytes == nil {
			return fmt.Errorf("revocation private key at index %d cannot be nil", i)
		}
		if expectedRevocationPublicKeys[i] == nil {
			return fmt.Errorf("expected revocation public key at index %d cannot be nil", i)
		}

		// secp256k1 private keys must be 32 bytes
		if len(revocationPrivateKeyBytes) != 32 {
			return fmt.Errorf("invalid revocation private key length at index %d: expected 32 bytes, got %d",
				i, len(revocationPrivateKeyBytes))
		}

		// Safely parse the private key
		var revocationKey *secp256k1.PrivateKey
		func() {
			defer func() {
				if r := recover(); r != nil {
					revocationKey = nil
				}
			}()
			revocationKey = secp256k1.PrivKeyFromBytes(revocationPrivateKeyBytes)
		}()
		if revocationKey == nil {
			return fmt.Errorf("failed to parse revocation private key at index %d", i)
		}

		revocationPubKey := revocationKey.PubKey()
		expectedRevocationPubKey, err := secp256k1.ParsePubKey(expectedRevocationPublicKeys[i])
		if err != nil {
			return fmt.Errorf("failed to parse expected revocation public key at index %d: %w", i, err)
		}
		if expectedRevocationPubKey == nil {
			return fmt.Errorf("parsed expected revocation public key is nil at index %d", i)
		}

		if !expectedRevocationPubKey.IsEqual(revocationPubKey) {
			return fmt.Errorf("revocation key mismatch at index %d: derived public key does not match expected", i)
		}
	}
	return nil
}
