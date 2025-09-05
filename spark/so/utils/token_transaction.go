package utils

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"math/big"
	"slices"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/protoconverter"
)

// MaxInputOrOutputTokenTransactionOutputs defines the maximum number of input or token outputs allowed in a token transaction.
const MaxInputOrOutputTokenTransactionOutputs = 500

// zero represents a big.Int with value 0, used for amount comparisons.
var zero = new(big.Int)

// TokenTransactionType represents the type of input in a token transaction
type TokenTransactionType int

const (
	TokenTransactionTypeUnknown TokenTransactionType = iota
	TokenTransactionTypeCreate
	TokenTransactionTypeMint
	TokenTransactionTypeTransfer
)

var tokenTransactionTypeBytes = map[TokenTransactionType][]byte{
	TokenTransactionTypeCreate:   {0, 0, 0, byte(tokenpb.TokenTransactionType_TOKEN_TRANSACTION_TYPE_CREATE)},
	TokenTransactionTypeMint:     {0, 0, 0, byte(tokenpb.TokenTransactionType_TOKEN_TRANSACTION_TYPE_MINT)},
	TokenTransactionTypeTransfer: {0, 0, 0, byte(tokenpb.TokenTransactionType_TOKEN_TRANSACTION_TYPE_TRANSFER)},
}

// String returns the string representation of the token transaction type
func (t TokenTransactionType) String() string {
	switch t {
	case TokenTransactionTypeCreate:
		return "CREATE"
	case TokenTransactionTypeMint:
		return "MINT"
	case TokenTransactionTypeTransfer:
		return "TRANSFER"
	default:
		return "UNKNOWN"
	}
}

// HashTokenTransaction generates a SHA256 hash of the TokenTransaction by:
// 1. Taking SHA256 of each field individually
// 2. Concatenating all field hashes in order
// 3. Taking SHA256 of the concatenated hashes
// If partialHash is true generate a partial hash even if the provided transaction is final.
func HashTokenTransaction(tokenTransaction *tokenpb.TokenTransaction, partialHash bool) ([]byte, error) {
	if tokenTransaction == nil {
		return nil, sparkerrors.InternalErrorf("token transaction cannot be nil")
	}

	switch tokenTransaction.Version {
	case 0:
		{
			sparkTx, err := protoconverter.SparkTokenTransactionFromTokenProto(tokenTransaction)
			if err != nil {
				return nil, sparkerrors.InternalErrorf("failed to convert token transaction: %w", err)
			}
			return HashTokenTransactionV0(sparkTx, partialHash)
		}
	case 1:
		return HashTokenTransactionV1(tokenTransaction, partialHash)
	case 2:
		return HashTokenTransactionV2(tokenTransaction, partialHash)
	default:
		return nil, sparkerrors.InternalErrorf("unsupported token transaction version: %d", tokenTransaction.Version)
	}
}

func HashTokenTransactionV2(tokenTransaction *tokenpb.TokenTransaction, partialHash bool) ([]byte, error) {
	if tokenTransaction == nil {
		return nil, fmt.Errorf("token transaction cannot be nil")
	}

	allHashes, err := hashTokenTransactionV1(tokenTransaction, partialHash)
	if err != nil {
		return nil, err
	}

	h := sha256.New()
	h.Reset()

	// Hash invoice attachments
	buf := make([]byte, 4)
	invoices := tokenTransaction.GetInvoiceAttachments()
	invoicesLen := len(invoices)
	binary.BigEndian.PutUint32(buf, uint32(invoicesLen))
	h.Write(buf)
	allHashes = append(allHashes, h.Sum(nil)...)

	type keyedInvoice struct {
		id  uuid.UUID
		raw string
	}
	sortedInvoices := make([]keyedInvoice, 0, len(invoices))
	for i, attachment := range invoices {
		if attachment == nil {
			return nil, fmt.Errorf("invoice attachment at index %d cannot be nil", i)
		}
		rawInvoice := attachment.GetSparkInvoice()
		parsedInvoice, err := common.ParseSparkInvoice(rawInvoice)
		if err != nil {
			return nil, fmt.Errorf("invalid invoice at %d: %w", i, err)
		}
		sortedInvoices = append(sortedInvoices, keyedInvoice{id: parsedInvoice.Id, raw: rawInvoice})
	}
	slices.SortFunc(sortedInvoices, func(a, b keyedInvoice) int { return bytes.Compare(a.id[:], b.id[:]) })
	for _, invoice := range sortedInvoices {
		h.Reset()
		h.Write([]byte(invoice.raw))
		allHashes = append(allHashes, h.Sum(nil)...)
	}

	// Final hash of all concatenated hashes
	h.Reset()
	h.Write(allHashes)
	return h.Sum(nil), nil
}

func HashTokenTransactionV1(tokenTransaction *tokenpb.TokenTransaction, partialHash bool) ([]byte, error) {
	if tokenTransaction == nil {
		return nil, fmt.Errorf("token transaction cannot be nil")
	}

	allHashes, err := hashTokenTransactionV1(tokenTransaction, partialHash)
	if err != nil {
		return nil, err
	}

	// Final hash of all concatenated hashes
	h := sha256.New()
	h.Reset()
	h.Write(allHashes)
	return h.Sum(nil), nil
}

func hashTokenTransactionV1(tokenTransaction *tokenpb.TokenTransaction, partialHash bool) ([]byte, error) {
	h := sha256.New()
	var allHashes []byte
	// Hash version
	h.Reset()
	versionBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(versionBytes, tokenTransaction.GetVersion())
	h.Write(versionBytes)
	allHashes = append(allHashes, h.Sum(nil)...)

	// Hash input type of transaction
	inputType, err := InferTokenTransactionType(tokenTransaction)
	if err != nil {
		return nil, fmt.Errorf("failed to infer token transaction type: %w", err)
	}

	h.Reset()
	h.Write(tokenTransactionTypeBytes[inputType])
	allHashes = append(allHashes, h.Sum(nil)...)

	// Hash transaction input
	var inputHashes []byte
	switch inputType {
	case TokenTransactionTypeTransfer:
		inputHashes, err = hashTransferInputV1(h, tokenTransaction.GetTransferInput())
	case TokenTransactionTypeCreate:
		inputHashes, err = hashCreateInputV1(h, tokenTransaction.GetCreateInput(), partialHash)
	case TokenTransactionTypeMint:
		inputHashes, err = hashMintInputV1(h, tokenTransaction.GetMintInput())
	default:
		return nil, fmt.Errorf("token transaction type %s is not valid", inputType)
	}
	if err != nil {
		return nil, err
	}
	allHashes = append(allHashes, inputHashes...)

	// Hash token outputs (length + contents)
	outputsLen := 0
	if tokenTransaction.TokenOutputs != nil {
		outputsLen = len(tokenTransaction.TokenOutputs)
	}
	h.Reset()
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(outputsLen))
	h.Write(buf)
	allHashes = append(allHashes, h.Sum(nil)...)
	for i, output := range tokenTransaction.TokenOutputs {
		if output == nil {
			return nil, fmt.Errorf("token output at index %d cannot be nil", i)
		}
		h.Reset()

		// Output ID is not set in the partial token transaction.
		if !partialHash {
			id := []byte(output.GetId())
			if len(id) == 0 {
				return nil, fmt.Errorf("token output ID at index %d cannot be nil or empty", i)
			}
			h.Write(id)
		}

		ownerPubKey := output.GetOwnerPublicKey()
		if len(ownerPubKey) == 0 {
			return nil, fmt.Errorf("owner public key at index %d cannot be nil or empty", i)
		}
		h.Write(ownerPubKey)

		// Revocation public key is not set in the partial token transaction.
		if !partialHash {
			revPubKey := output.GetRevocationCommitment()
			if len(revPubKey) == 0 {
				return nil, fmt.Errorf("revocation public key at index %d cannot be nil or empty", i)
			}
			h.Write(revPubKey)

			withdrawalBondBytes := make([]byte, 8)
			binary.BigEndian.PutUint64(withdrawalBondBytes, output.GetWithdrawBondSats())
			h.Write(withdrawalBondBytes)

			withdrawalLocktimeBytes := make([]byte, 8)
			binary.BigEndian.PutUint64(withdrawalLocktimeBytes, output.GetWithdrawRelativeBlockLocktime())
			h.Write(withdrawalLocktimeBytes)
		}

		tokenPubKey := output.GetTokenPublicKey()
		if len(tokenPubKey) == 0 {
			h.Write(make([]byte, 33))
		} else {
			h.Write(tokenPubKey)
		}

		tokenIdentifier := output.GetTokenIdentifier()
		if len(tokenIdentifier) == 0 {
			h.Write(make([]byte, 32))
		} else {
			h.Write(tokenIdentifier)
		}

		tokenAmount := output.GetTokenAmount()
		if len(tokenAmount) == 0 {
			return nil, fmt.Errorf("token amount at index %d cannot be nil or empty", i)
		}
		h.Write(tokenAmount)

		allHashes = append(allHashes, h.Sum(nil)...)
	}

	operatorPublicKeys := tokenTransaction.GetSparkOperatorIdentityPublicKeys()
	if operatorPublicKeys == nil {
		return nil, fmt.Errorf("operator public keys cannot be nil")
	}

	// Sort operator keys for consistent hashing
	slices.SortFunc(operatorPublicKeys, bytes.Compare)

	// Hash spark operator identity public keys (length + contents)
	operatorPublicKeysLen := len(operatorPublicKeys)
	h.Reset()
	buf = make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(operatorPublicKeysLen))
	h.Write(buf)
	allHashes = append(allHashes, h.Sum(nil)...)

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

	// Hash the network field
	h.Reset()
	networkBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(networkBytes, uint32(tokenTransaction.GetNetwork()))
	h.Write(networkBytes)
	allHashes = append(allHashes, h.Sum(nil)...)

	// Hash client_created_timestamp
	h.Reset()
	clientCreatedTimestampBytes := make([]byte, 8)
	if ts := tokenTransaction.GetClientCreatedTimestamp(); ts == nil {
		return nil, fmt.Errorf("client created timestamp cannot be empty")
	} else {
		binary.BigEndian.PutUint64(clientCreatedTimestampBytes, uint64(ts.AsTime().UnixMilli()))
	}
	h.Write(clientCreatedTimestampBytes)
	allHashes = append(allHashes, h.Sum(nil)...)

	// Hash expiry time only for final hash; skip when computing partial hash
	if !partialHash {
		h.Reset()
		expiryTimeBytes := make([]byte, 8)
		if tokenTransaction.GetExpiryTime() == nil {
			return nil, fmt.Errorf("expiry time cannot be empty")
		}
		binary.BigEndian.PutUint64(expiryTimeBytes, uint64(tokenTransaction.GetExpiryTime().AsTime().Unix()))
		h.Write(expiryTimeBytes)
		allHashes = append(allHashes, h.Sum(nil)...)
	}
	return allHashes, nil
}

func hashTransferInputV1(h hash.Hash, transferSource *tokenpb.TokenTransferInput) ([]byte, error) {
	if transferSource == nil {
		return nil, fmt.Errorf("transfer input cannot be nil when hashing transfer transaction")
	}
	var transferHashes []byte
	if transferSource.OutputsToSpend == nil {
		return nil, fmt.Errorf("transfer input outputs cannot be nil")
	}
	outputsLen := len(transferSource.GetOutputsToSpend())
	h.Reset()
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(outputsLen))
	h.Write(buf)
	transferHashes = append(transferHashes, h.Sum(nil)...)
	for i, output := range transferSource.GetOutputsToSpend() {
		if output == nil {
			return nil, fmt.Errorf("transfer input token output at index %d cannot be nil", i)
		}
		h.Reset()

		if txHash := output.GetPrevTokenTransactionHash(); txHash != nil {
			if len(txHash) != 32 {
				return nil, fmt.Errorf("invalid previous transaction hash length at index %d: expected 32 bytes, got %d", i, len(txHash))
			}
			h.Write(txHash)
		}

		buf := make([]byte, 4)
		binary.BigEndian.PutUint32(buf, output.GetPrevTokenTransactionVout())
		h.Write(buf)
		transferHashes = append(transferHashes, h.Sum(nil)...)
	}
	return transferHashes, nil
}

func hashCreateInputV1(h hash.Hash, createInput *tokenpb.TokenCreateInput, partialHash bool) ([]byte, error) {
	if createInput == nil {
		return nil, fmt.Errorf("create input cannot be nil when hashing create transaction")
	}
	var createHashes []byte
	h.Reset()
	pubKey := createInput.GetIssuerPublicKey()
	if len(pubKey) == 0 {
		return nil, fmt.Errorf("issuer public key cannot be nil or empty")
	}
	h.Write(pubKey)
	createHashes = append(createHashes, h.Sum(nil)...)

	h.Reset()
	tokenName := createInput.GetTokenName()
	if len(tokenName) == 0 {
		return nil, fmt.Errorf("token name cannot be empty")
	}
	h.Write([]byte(tokenName))
	createHashes = append(createHashes, h.Sum(nil)...)

	h.Reset()
	tokenTicker := createInput.GetTokenTicker()
	if len(tokenTicker) == 0 {
		return nil, fmt.Errorf("token ticker cannot be empty")
	}
	tokenTickerBytes := []byte(tokenTicker)
	h.Write(tokenTickerBytes)
	createHashes = append(createHashes, h.Sum(nil)...)

	h.Reset()
	decimalsBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(decimalsBytes, createInput.GetDecimals())
	h.Write(decimalsBytes)
	createHashes = append(createHashes, h.Sum(nil)...)

	h.Reset()
	maxSupply := createInput.GetMaxSupply()
	if maxSupply == nil {
		return nil, fmt.Errorf("max supply cannot be nil")
	}
	if len(maxSupply) != 16 {
		return nil, fmt.Errorf("max supply must be exactly 16 bytes, got %d", len(maxSupply))
	}
	h.Write(maxSupply)
	createHashes = append(createHashes, h.Sum(nil)...)

	h.Reset()
	if createInput.GetIsFreezable() {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
	createHashes = append(createHashes, h.Sum(nil)...)

	h.Reset()
	if !partialHash {
		creationEntityPublicKey := createInput.GetCreationEntityPublicKey()
		if creationEntityPublicKey != nil {
			h.Write(creationEntityPublicKey)
		}
	}
	createHashes = append(createHashes, h.Sum(nil)...)
	return createHashes, nil
}

func hashMintInputV1(h hash.Hash, mintInput *tokenpb.TokenMintInput) ([]byte, error) {
	if mintInput == nil {
		return nil, fmt.Errorf("mint input cannot be nil when hashing mint transaction")
	}
	var mintHashes []byte
	h.Reset()
	pubKey := mintInput.GetIssuerPublicKey()
	if len(pubKey) == 0 {
		return nil, fmt.Errorf("issuer public key cannot be nil or empty")
	}
	h.Write(pubKey)
	mintHashes = append(mintHashes, h.Sum(nil)...)

	h.Reset()
	tokenIdentifier := mintInput.GetTokenIdentifier()
	if tokenIdentifier != nil {
		h.Write(tokenIdentifier)
	} else {
		h.Write(make([]byte, 32))
	}
	mintHashes = append(mintHashes, h.Sum(nil)...)

	return mintHashes, nil
}

func HashTokenTransactionV0(tokenTransaction *sparkpb.TokenTransaction, partialHash bool) ([]byte, error) {
	if tokenTransaction == nil {
		return nil, fmt.Errorf("token transaction cannot be nil")
	}

	inputType, err := InferTokenTransactionTypeSparkProtos(tokenTransaction)
	if err != nil {
		return nil, err
	}

	h := sha256.New()
	var allHashes []byte

	var inputHashes []byte
	switch inputType {
	case TokenTransactionTypeTransfer:
		inputHashes, err = hashTransferInputV0(h, tokenTransaction.GetTransferInput())
	case TokenTransactionTypeCreate:
		inputHashes, err = hashCreateInputV0(h, tokenTransaction.GetCreateInput(), partialHash)
	case TokenTransactionTypeMint:
		inputHashes, err = hashMintInputV0(h, tokenTransaction.GetMintInput())
	default:
		return nil, fmt.Errorf("token transaction type %s is not valid", inputType)
	}
	if err != nil {
		return nil, err
	}
	allHashes = append(allHashes, inputHashes...)

	outputHashes, err := hashTokenOutputs(h, tokenTransaction.TokenOutputs, partialHash)
	if err != nil {
		return nil, fmt.Errorf("failed to hash token outputs: %w", err)
	}
	allHashes = append(allHashes, outputHashes...)

	operatorHashes, err := hashOperators(h, tokenTransaction.GetSparkOperatorIdentityPublicKeys())
	if err != nil {
		return nil, fmt.Errorf("failed to hash operators: %w", err)
	}
	allHashes = append(allHashes, operatorHashes...)

	networkHash, err := hashNetwork(h, tokenTransaction.GetNetwork())
	if err != nil {
		return nil, fmt.Errorf("failed to hash network: %w", err)
	}
	allHashes = append(allHashes, networkHash...)

	// Final hash of all concatenated hashes
	h.Reset()
	h.Write(allHashes)
	finalHash := h.Sum(nil)
	return finalHash, nil
}

func hashTransferInputV0(h hash.Hash, transferSource *pb.TokenTransferInput) ([]byte, error) {
	var allHashes []byte
	if transferSource == nil {
		return nil, fmt.Errorf("transfer input cannot be nil when hashing transfer transaction")
	}
	if transferSource.OutputsToSpend == nil {
		return nil, fmt.Errorf("transfer input outputs cannot be nil")
	}
	for i, output := range transferSource.GetOutputsToSpend() {
		if output == nil {
			return nil, fmt.Errorf("transfer input token output at index %d cannot be nil", i)
		}
		h.Reset()

		txHash := output.GetPrevTokenTransactionHash()
		if txHash != nil {
			if len(txHash) != 32 {
				return nil, fmt.Errorf("invalid previous transaction hash length at index %d: expected 32 bytes, got %d", i, len(txHash))
			}
			h.Write(txHash)
		}

		buf := make([]byte, 4)
		binary.BigEndian.PutUint32(buf, output.GetPrevTokenTransactionVout())
		h.Write(buf)
		allHashes = append(allHashes, h.Sum(nil)...)
	}
	return allHashes, nil
}

func hashCreateInputV0(h hash.Hash, createInput *pb.TokenCreateInput, partialHash bool) ([]byte, error) {
	if createInput == nil {
		return nil, fmt.Errorf("create input cannot be nil when hashing create transaction")
	}
	var allHashes []byte

	h.Reset()
	pubKey := createInput.GetIssuerPublicKey()
	if len(pubKey) == 0 {
		return nil, fmt.Errorf("issuer public key cannot be nil or empty")
	}
	h.Write(pubKey)
	allHashes = append(allHashes, h.Sum(nil)...)

	h.Reset()
	tokenName := createInput.GetTokenName()
	if len(tokenName) == 0 {
		return nil, fmt.Errorf("token name cannot be empty")
	}
	if len(tokenName) > 20 {
		return nil, fmt.Errorf("token name cannot be longer than 20 bytes")
	}
	tokenNameBytes := make([]byte, 20)
	copy(tokenNameBytes, tokenName)
	h.Write(tokenNameBytes)
	allHashes = append(allHashes, h.Sum(nil)...)

	h.Reset()
	tokenTicker := createInput.GetTokenTicker()
	if len(tokenTicker) == 0 {
		return nil, fmt.Errorf("token ticker cannot be empty")
	}
	if len(tokenTicker) > 6 {
		return nil, fmt.Errorf("token ticker cannot be longer than 6 bytes")
	}
	tokenTickerBytes := make([]byte, 6)
	copy(tokenTickerBytes, tokenTicker)
	h.Write(tokenTickerBytes)
	allHashes = append(allHashes, h.Sum(nil)...)

	h.Reset()
	decimalsBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(decimalsBytes, createInput.GetDecimals())
	h.Write(decimalsBytes)
	allHashes = append(allHashes, h.Sum(nil)...)

	h.Reset()
	maxSupply := createInput.GetMaxSupply()
	if maxSupply == nil {
		return nil, fmt.Errorf("max supply cannot be nil")
	}
	if len(maxSupply) != 16 {
		return nil, fmt.Errorf("max supply must be exactly 16 bytes, got %d", len(maxSupply))
	}
	h.Write(maxSupply)
	allHashes = append(allHashes, h.Sum(nil)...)

	h.Reset()
	if createInput.GetIsFreezable() {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
	allHashes = append(allHashes, h.Sum(nil)...)

	h.Reset()
	if !partialHash {
		creationEntityPublicKey := createInput.GetCreationEntityPublicKey()
		if creationEntityPublicKey != nil {
			h.Write(creationEntityPublicKey)
		}
	}
	allHashes = append(allHashes, h.Sum(nil)...)
	return allHashes, nil
}

func hashMintInputV0(h hash.Hash, mintInput *pb.TokenMintInput) ([]byte, error) {
	if mintInput == nil {
		return nil, fmt.Errorf("mint input cannot be nil when hashing mint transaction")
	}
	var allHashes []byte
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
	return allHashes, nil
}

func hashTokenOutputs(h hash.Hash, tokenOutputs []*pb.TokenOutput, partialHash bool) ([]byte, error) {
	var allHashes []byte
	for i, output := range tokenOutputs {
		if output == nil {
			return nil, fmt.Errorf("token output at index %d cannot be nil", i)
		}
		h.Reset()

		// Leaf ID is not set in the partial token transaction.
		if !partialHash && output.GetId() != "" {
			id := []byte(output.GetId())
			if len(id) == 0 {
				return nil, fmt.Errorf("token output ID at index %d cannot be empty", i)
			}
			h.Write(id)
		}

		ownerPubKey := output.GetOwnerPublicKey()
		if ownerPubKey != nil {
			if len(ownerPubKey) == 0 {
				return nil, fmt.Errorf("owner public key at index %d cannot be empty", i)
			}
			h.Write(ownerPubKey)
		}

		// Revocation public key is not set in the partial token transaction.
		if !partialHash {
			revPubKey := output.GetRevocationCommitment()
			if revPubKey != nil {
				if len(revPubKey) == 0 {
					return nil, fmt.Errorf("revocation public key at index %d cannot be empty", i)
				}
				h.Write(revPubKey)
			}

			withdrawalBondBytes := make([]byte, 8)
			binary.BigEndian.PutUint64(withdrawalBondBytes, output.GetWithdrawBondSats())
			h.Write(withdrawalBondBytes)

			withdrawalLocktimeBytes := make([]byte, 8)
			binary.BigEndian.PutUint64(withdrawalLocktimeBytes, output.GetWithdrawRelativeBlockLocktime())
			h.Write(withdrawalLocktimeBytes)
		}

		tokenPubKey := output.GetTokenPublicKey()
		if tokenPubKey != nil {
			if len(tokenPubKey) == 0 {
				return nil, fmt.Errorf("token public key at index %d cannot be empty", i)
			}
			h.Write(tokenPubKey)
		}

		tokenAmount := output.GetTokenAmount()
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
	return allHashes, nil
}

func hashOperators(h hash.Hash, operatorPublicKeys [][]byte) ([]byte, error) {
	var allHashes []byte
	if operatorPublicKeys == nil {
		return nil, fmt.Errorf("operator public keys cannot be nil")
	}

	// Sort operator keys for consistent hashing
	slices.SortFunc(operatorPublicKeys, bytes.Compare)

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

	return allHashes, nil
}

func hashNetwork(h hash.Hash, network pb.Network) ([]byte, error) {
	h.Reset()
	networkBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(networkBytes, uint32(network))
	h.Write(networkBytes)
	return h.Sum(nil), nil
}

// HashOperatorSpecificTokenTransactionSignablePayload generates a hash of the operator-specific payload
// by concatenating hashes of the transaction hash and operator public key.
func HashOperatorSpecificTokenTransactionSignablePayload(payload *sparkpb.OperatorSpecificTokenTransactionSignablePayload) ([]byte, error) {
	if payload == nil {
		return nil, sparkerrors.InvalidUserInputErrorf("operator specific token transaction signable payload cannot be nil")
	}

	h := sha256.New()
	var allHashes []byte

	// Hash final_token_transaction_hash
	h.Reset()
	if txHash := payload.GetFinalTokenTransactionHash(); txHash != nil {
		if len(txHash) != 32 {
			return nil, sparkerrors.InvalidUserInputErrorf("invalid final transaction hash length: expected 32 bytes, got %d", len(txHash))
		}
		h.Write(txHash)
	}

	allHashes = append(allHashes, h.Sum(nil)...)

	// Hash operator_identity_public_key
	h.Reset()
	pubKey := payload.GetOperatorIdentityPublicKey()
	if len(pubKey) == 0 {
		return nil, sparkerrors.InvalidUserInputErrorf("operator identity public key cannot be empty")
	}
	h.Write(pubKey)
	allHashes = append(allHashes, h.Sum(nil)...)

	// Final hash of all concatenated hashes
	h.Reset()
	h.Write(allHashes)
	return h.Sum(nil), nil
}

func HashFreezeTokensPayload(payload *tokenpb.FreezeTokensPayload) ([]byte, error) {
	if payload == nil {
		return nil, fmt.Errorf("freeze tokens payload cannot be nil")
	}

	switch payload.Version {
	case 0:
		return HashFreezeTokensPayloadV0(payload)
	case 1:
		return HashFreezeTokensPayloadV1(payload)
	default:
		return nil, fmt.Errorf("unsupported payload version: %d", payload.Version)
	}
}

func HashFreezeTokensPayloadV1(payload *tokenpb.FreezeTokensPayload) ([]byte, error) {
	if payload == nil {
		return nil, fmt.Errorf("payload cannot be nil")
	}

	h := sha256.New()
	var allHashes []byte

	// Hash version
	h.Reset()
	versionBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(versionBytes, payload.GetVersion())
	h.Write(versionBytes)
	allHashes = append(allHashes, h.Sum(nil)...)

	operatorHashes, err := hashFreezePayloadContents(h, payload)
	if err != nil {
		return nil, fmt.Errorf("failed to hash freeze payload contents: %w", err)
	}
	allHashes = append(allHashes, operatorHashes...)

	// Final hash of all concatenated hashes
	h.Reset()
	h.Write(allHashes)
	return h.Sum(nil), nil
}

// HashFreezeTokensPayloadV0 generates a hash of the freeze tokens payload by concatenating
// hashes of the owner public key, token public key, freeze status, timestamp and operator key.
func HashFreezeTokensPayloadV0(payload *tokenpb.FreezeTokensPayload) ([]byte, error) {
	if payload == nil {
		return nil, fmt.Errorf("freeze tokens payload cannot be nil")
	}
	h := sha256.New()

	contentHashes, err := hashFreezePayloadContents(h, payload)
	if err != nil {
		return nil, err
	}

	// Final hash of all concatenated hashes
	h.Reset()
	h.Write(contentHashes)
	return h.Sum(nil), nil
}

// hashFreezePayloadContents extracts the common hashing logic for freeze payload contents
// that is shared between V0 and V1 versions of HashFreezeTokensPayload
func hashFreezePayloadContents(h hash.Hash, payload *tokenpb.FreezeTokensPayload) ([]byte, error) {
	var allHashes []byte

	h.Reset()
	ownerPubKey := payload.GetOwnerPublicKey()
	if len(ownerPubKey) == 0 {
		return nil, fmt.Errorf("owner public key cannot be empty")
	}
	h.Write(ownerPubKey)
	allHashes = append(allHashes, h.Sum(nil)...)

	h.Reset()
	switch payload.Version {
	case 0:
		tokenPublicKey := payload.GetTokenPublicKey()
		if len(tokenPublicKey) == 0 {
			return nil, fmt.Errorf("token public key cannot be empty")
		}
		h.Write(tokenPublicKey)
	default:
		tokenIdentifier := payload.GetTokenIdentifier()
		if tokenIdentifier == nil {
			return nil, fmt.Errorf("token identifier cannot be nil")
		}
		h.Write(tokenIdentifier)
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
	if payload.GetIssuerProvidedTimestamp() == 0 {
		return nil, fmt.Errorf("issuer provided timestamp cannot be 0")
	}
	nonceBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(nonceBytes, payload.GetIssuerProvidedTimestamp())
	h.Write(nonceBytes)
	allHashes = append(allHashes, h.Sum(nil)...)

	h.Reset()
	operatorPubKey := payload.GetOperatorIdentityPublicKey()
	if len(operatorPubKey) == 0 {
		return nil, fmt.Errorf("operator identity public key cannot be empty")
	}
	h.Write(operatorPubKey)
	allHashes = append(allHashes, h.Sum(nil)...)

	return allHashes, nil
}

// InferTokenTransactionTypeSparkProtos validates that exactly one input type is present and returns it
func InferTokenTransactionTypeSparkProtos(tokenTransaction *sparkpb.TokenTransaction) (TokenTransactionType, error) {
	hasCreateInput := tokenTransaction.GetCreateInput() != nil
	hasMintInput := tokenTransaction.GetMintInput() != nil
	hasTransferInput := tokenTransaction.GetTransferInput() != nil

	inputCount := 0
	var inputType TokenTransactionType

	if hasCreateInput {
		inputCount++
		inputType = TokenTransactionTypeCreate
	}
	if hasMintInput {
		inputCount++
		inputType = TokenTransactionTypeMint
	}
	if hasTransferInput {
		inputCount++
		inputType = TokenTransactionTypeTransfer
	}

	if inputCount != 1 {
		return TokenTransactionTypeUnknown, fmt.Errorf("token transaction must have exactly one of create_input, mint_input, or transfer_input")
	}

	return inputType, nil
}

// InferTokenTransactionType validates that exactly one input type is present and returns it
func InferTokenTransactionType(tokenTransaction *tokenpb.TokenTransaction) (TokenTransactionType, error) {
	hasCreateInput := tokenTransaction.GetCreateInput() != nil
	hasMintInput := tokenTransaction.GetMintInput() != nil

	var inputType TokenTransactionType
	if hasCreateInput {
		inputType = TokenTransactionTypeCreate
	} else if hasMintInput {
		inputType = TokenTransactionTypeMint
	} else {
		// If no create or mint, assume its a transfer.
		inputType = TokenTransactionTypeTransfer
	}
	return inputType, nil
}

func validateBaseCreateTransaction(
	tokenTransaction *tokenpb.TokenTransaction,
	inputSignatures []*tokenpb.SignatureWithIndex,
) error {
	createInput := tokenTransaction.GetCreateInput()

	if len(inputSignatures) != 1 {
		return fmt.Errorf("create transactions must have exactly one signature")
	}
	createSignature := inputSignatures[0]
	if createSignature == nil {
		return fmt.Errorf("create signature cannot be nil")
	}

	if len(tokenTransaction.TokenOutputs) > 0 {
		return fmt.Errorf("create transactions must not have any outputs")
	}

	tokenMetadata, err := common.NewTokenMetadataFromCreateInput(createInput, tokenTransaction.Network)
	if err != nil {
		return fmt.Errorf("failed to create token metadata: %w", err)
	}
	if err := tokenMetadata.ValidatePartial(); err != nil {
		return fmt.Errorf("token metadata validation failed: %w", err)
	}

	return nil
}

func validateBaseMintTransaction(
	tokenTransaction *tokenpb.TokenTransaction,
	inputSignatures []*tokenpb.SignatureWithIndex,
	requireTokenIdentifierForMints bool,
) error {
	err := validateBaseTokenOutputs(tokenTransaction, requireTokenIdentifierForMints)
	if err != nil {
		return fmt.Errorf("token output consistency validation failed: %w", err)
	}

	mintInput := tokenTransaction.GetMintInput()

	if tokenTransaction.GetClientCreatedTimestamp() == nil {
		return fmt.Errorf("client created timestamp cannot be nil")
	}

	if tokenTransaction.TokenOutputs == nil {
		return fmt.Errorf("mint outputs to create cannot be nil")
	}
	if len(tokenTransaction.TokenOutputs) == 0 {
		return fmt.Errorf("mint outputs to create cannot be empty")
	}

	if requireTokenIdentifierForMints {
		if mintInput.GetTokenIdentifier() == nil || len(mintInput.GetTokenIdentifier()) != 32 {
			return fmt.Errorf("token identifier cannot be nil and must be 32 bytes")
		}
	}

	hasTokenIdentifier := tokenTransaction.TokenOutputs[0].GetTokenIdentifier() != nil
	if hasTokenIdentifier {
		// Validate that the token identifier matches the mint input
		expectedTokenIdentifier := tokenTransaction.TokenOutputs[0].GetTokenIdentifier()
		if !bytes.Equal(mintInput.GetTokenIdentifier(), expectedTokenIdentifier) {
			return fmt.Errorf("output token identifiers must match input token identifier: %x != %x", mintInput.GetTokenIdentifier(), expectedTokenIdentifier)
		}
	} else {
		// When using token public key, validate token public key matches the issuer public key
		expectedTokenPublicKey := tokenTransaction.TokenOutputs[0].GetTokenPublicKey()
		if !bytes.Equal(mintInput.GetIssuerPublicKey(), expectedTokenPublicKey) {
			return fmt.Errorf("output token public keys must match input issuer public key")
		}
	}

	if len(inputSignatures) != 1 {
		return fmt.Errorf("mint transactions must have exactly one signature")
	}

	issueSignature := inputSignatures[0]
	if issueSignature == nil {
		return fmt.Errorf("mint signature cannot be nil")
	}

	// Validate mint amounts > 0
	for i, output := range tokenTransaction.TokenOutputs {
		amount := new(big.Int).SetBytes(output.GetTokenAmount())

		if amount.Cmp(zero) == 0 {
			return fmt.Errorf("mint amount for output %d cannot be 0", i)
		}

		if requireTokenIdentifierForMints {
			if output.GetTokenIdentifier() == nil {
				return fmt.Errorf("token identifier cannot be nil")
			}
		}
	}

	return nil
}

func validateBaseTransferTransaction(
	tokenTransaction *tokenpb.TokenTransaction,
	inputSignatures []*tokenpb.SignatureWithIndex,
	requireTokenIdentifierForTransfers bool,
) error {
	err := validateBaseTokenOutputs(tokenTransaction, requireTokenIdentifierForTransfers)
	if err != nil {
		return fmt.Errorf("token output consistency validation failed: %w", err)
	}

	transferInput := tokenTransaction.GetTransferInput()
	if tokenTransaction.TokenOutputs == nil {
		return fmt.Errorf("transfer outputs to create cannot be nil")
	}
	if len(tokenTransaction.TokenOutputs) == 0 {
		return fmt.Errorf("transfer outputs to create cannot be empty")
	}

	if len(transferInput.GetOutputsToSpend()) == 0 {
		return fmt.Errorf("transfer outputs to spend cannot be empty")
	}
	if len(tokenTransaction.GetTransferInput().OutputsToSpend) > MaxInputOrOutputTokenTransactionOutputs {
		return fmt.Errorf("too many outputs to spend, maximum is %d", MaxInputOrOutputTokenTransactionOutputs)
	}

	// Validate there is the correct number of signatures for outputs to spend.
	if len(inputSignatures) != len(transferInput.GetOutputsToSpend()) {
		return fmt.Errorf("number of signatures must match number of outputs to spend")
	}

	if requireTokenIdentifierForTransfers {
		for _, output := range tokenTransaction.TokenOutputs {
			if output.GetTokenIdentifier() == nil {
				return fmt.Errorf("token identifier cannot be nil")
			}
		}
	}

	return nil
}

// ValidatePartialTokenTransaction validates a token transaction request received from a user.
// It checks the transaction structure, signatures, and token amounts for create, mint and transfer operations.
// It also ensures that SO-filled fields are not set, as these will be filled by the SOs to form the final transaction.
func ValidatePartialTokenTransaction(
	tokenTransaction *tokenpb.TokenTransaction,
	inputSignatures []*tokenpb.SignatureWithIndex,
	sparkOperatorsFromConfig map[string]*sparkpb.SigningOperatorInfo,
	supportedNetworks []common.Network,
	requireTokenIdentifierForMints bool,
	requireTokenIdentifierForTransfers bool,
) error {
	err := validateBaseTokenTransaction(
		tokenTransaction,
		inputSignatures,
		sparkOperatorsFromConfig,
		supportedNetworks,
		requireTokenIdentifierForMints,
		requireTokenIdentifierForTransfers,
	)
	if err != nil {
		return err
	}

	if tokenTransaction.ExpiryTime != nil {
		return fmt.Errorf("expiry time should not be set by the client")
	}

	inputType, err := InferTokenTransactionType(tokenTransaction)
	if err != nil {
		return err
	}

	// Ensure SO-filled fields are not set in partial token transactions
	switch inputType {
	case TokenTransactionTypeCreate:
		createInput := tokenTransaction.GetCreateInput()
		if createInput.GetCreationEntityPublicKey() != nil {
			return fmt.Errorf("creation entity public key will be added by the SO - do not set this field when starting transactions")
		}
	case TokenTransactionTypeMint, TokenTransactionTypeTransfer:
		for i, output := range tokenTransaction.TokenOutputs {
			if output.GetRevocationCommitment() != nil {
				return fmt.Errorf("output %d revocation commitment will be added by the SO - do not set this field when starting transactions", i)
			}
			if output.WithdrawBondSats != nil {
				return fmt.Errorf("output %d withdraw bond sats will be added by the SO - do not set this field when starting transactions", i)
			}
			if output.WithdrawRelativeBlockLocktime != nil {
				return fmt.Errorf("output %d withdraw relative block locktime will be added by the SO - do not set this field when starting transactions", i)
			}
			if output.Id != nil && *output.Id != "" {
				return fmt.Errorf("output %d ID will be added by the SO - do not set this field when starting transactions", i)
			}
		}
	default:
		return fmt.Errorf("token transaction type unknown")
	}
	return nil
}

func validateBaseTokenOutputs(tokenTransaction *tokenpb.TokenTransaction, requireTokenIdentifier bool) error {
	if len(tokenTransaction.TokenOutputs) == 0 {
		return fmt.Errorf("token outputs cannot be empty for mint and transfer transactions")
	}

	hasTokenIdentifier := tokenTransaction.TokenOutputs[0].GetTokenIdentifier() != nil
	if requireTokenIdentifier && !hasTokenIdentifier {
		return fmt.Errorf("token identifier must be set when token identifier is required")
	}
	if hasTokenIdentifier {
		// Verify all outputs have the same token identifier
		expectedTokenIdentifier := tokenTransaction.TokenOutputs[0].GetTokenIdentifier()
		expectedTokenPublicKey := tokenTransaction.TokenOutputs[0].GetTokenPublicKey()
		if expectedTokenIdentifier == nil {
			return fmt.Errorf("first output must have token identifier if any output has one")
		}
		for i, output := range tokenTransaction.TokenOutputs {
			if output.GetTokenIdentifier() == nil {
				return fmt.Errorf("output %d missing token identifier", i)
			}
			if !bytes.Equal(output.GetTokenIdentifier(), expectedTokenIdentifier) {
				return fmt.Errorf("output %d token identifier (%x) must match mint input token identifier (%x)",
					i, output.GetTokenIdentifier(), expectedTokenIdentifier)
			}
			if !bytes.Equal(output.GetTokenPublicKey(), expectedTokenPublicKey) {
				return fmt.Errorf("output %d token public key (%x) must match expected token public key (%x)",
					i, output.GetTokenPublicKey(), expectedTokenPublicKey)
			}
		}
		// Verify that token public key is not set if token identifier is set
		for i, output := range tokenTransaction.TokenOutputs {
			if output.GetTokenPublicKey() != nil {
				return fmt.Errorf("output %d cannot have token public key when token identifier is set", i)
			}
		}
	} else {
		// If token identifier is not set, conduct legacy validation logic of token public key.
		expectedTokenPublicKey := tokenTransaction.TokenOutputs[0].GetTokenPublicKey()
		if expectedTokenPublicKey == nil {
			return fmt.Errorf("token public key cannot be nil when token identifier is not set")
		}
		for i, output := range tokenTransaction.TokenOutputs {
			if output.GetTokenPublicKey() == nil {
				return fmt.Errorf("output %d token public key cannot be nil when token identifier is not set", i)
			}
			if !bytes.Equal(output.GetTokenPublicKey(), expectedTokenPublicKey) {
				return fmt.Errorf("all outputs must have the same token public key")
			}
		}
	}
	return nil
}

// ValidateOwnershipSignature validates that the ownership signature of a hash (either a token transaction hash
// or freeze tokens payload hash) matches a predefined issuer public key or owner public key of an output being spent.
// It supports both ECDSA DER signatures and Schnorr signatures.
func ValidateOwnershipSignature(signature []byte, hash []byte, issuerOrOwnerPublicKey keys.Public) error {
	if signature == nil {
		return sparkerrors.InvalidUserInputErrorf("ownership signature cannot be nil")
	}
	if hash == nil {
		return sparkerrors.InvalidUserInputErrorf("hash to verify cannot be nil")
	}
	if issuerOrOwnerPublicKey.IsZero() {
		return sparkerrors.InvalidUserInputErrorf("owner public key cannot be zero")
	}

	// Check if it's a Schnorr signature
	if schnorrSig, err := schnorr.ParseSignature(signature); err == nil {
		if schnorrSig.Verify(hash, issuerOrOwnerPublicKey.ToBTCEC()) {
			return nil
		}
		// If Schnorr verification failed, fall through and try ECDSA DER
	}

	// If Schnorr parsing failed, fall through to try DER parsing, which in rare cases could be 64 bytes.
	// Try to parse as ECDSA DER signature
	sig, err := ecdsa.ParseDERSignature(signature)
	if err != nil {
		return sparkerrors.InvalidUserInputErrorf("failed to parse signature as either Schnorr or DER: %w", err)
	}
	if sig == nil {
		return sparkerrors.InvalidUserInputErrorf("parsed signature is nil")
	}

	if !sig.Verify(hash, issuerOrOwnerPublicKey.ToBTCEC()) {
		return sparkerrors.FailedPreconditionErrorf("invalid ownership signature")
	}
	return nil
}

func ValidateFreezeTokensPayload(payload *tokenpb.FreezeTokensPayload, expectedSparkOperatorPublicKey keys.Public) error {
	if payload == nil {
		return fmt.Errorf("freeze tokens payload cannot be nil")
	}

	if len(payload.GetOwnerPublicKey()) == 0 {
		return fmt.Errorf("owner public key cannot be empty")
	}
	switch payload.Version {
	case 0:
		if payload.GetTokenIdentifier() != nil {
			return fmt.Errorf("token identifier must be nil for version 0")
		}
		if payload.GetTokenPublicKey() == nil {
			return fmt.Errorf("token public key cannot be nil for version 0")
		}
	case 1:
		if payload.GetTokenPublicKey() != nil {
			return fmt.Errorf("token public key must be nil for version 1")
		}
		if len(payload.GetTokenIdentifier()) != 32 {
			return fmt.Errorf("token identifier must be exactly 32 bytes, got %d", len(payload.GetTokenIdentifier()))
		}
	default:
		return fmt.Errorf("invalid freeze tokens payload version: %d", payload.Version)
	}
	if payload.GetIssuerProvidedTimestamp() == 0 {
		return fmt.Errorf("issuer provided timestamp cannot be 0")
	}

	payloadOpIDPubKey, err := keys.ParsePublicKey(payload.GetOperatorIdentityPublicKey())
	if err != nil {
		return fmt.Errorf("failed to parse operator identity public key: %w", err)
	}
	if !payloadOpIDPubKey.Equals(expectedSparkOperatorPublicKey) {
		return fmt.Errorf("operator identity public key %s does not match expected operator %s from config", payload.GetOperatorIdentityPublicKey(), expectedSparkOperatorPublicKey)
	}
	return nil
}

// ValidateRevocationKeys validates that the provided revocation private keys correspond to the expected public keys.
// It ensures the private keys can correctly derive the expected public keys, preventing key mismatches.
func ValidateRevocationKeys(revocationPrivateKeys []keys.Private, expectedRevocationPublicKeys []keys.Public) error {
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

	for i, revocationKey := range revocationPrivateKeys {
		expectedPubKey := expectedRevocationPublicKeys[i]
		switch {
		case revocationKey.IsZero():
			return fmt.Errorf("revocation private key at index %d cannot be empty", i)
		case expectedPubKey.IsZero():
			return fmt.Errorf("expected revocation public key at index %d cannot be empty", i)
		case !expectedPubKey.Equals(revocationKey.Public()):
			return fmt.Errorf("revocation key mismatch at index %d: derived public key does not match expected", i)
		}
	}
	return nil
}

func isNetworkSupported(providedNetwork common.Network, networks []common.Network) bool {
	// UNSPECIFIED network should never be considered supported
	return providedNetwork != common.Unspecified && slices.Contains(networks, providedNetwork)
}

// CalculateMintAmountFromTransaction calculates the total amount being minted
// in a token transaction by summing all output amounts.
func CalculateMintAmountFromTransaction(tokenTransaction *tokenpb.TokenTransaction) (*big.Int, error) {
	if tokenTransaction == nil {
		return nil, fmt.Errorf("mint token transaction cannot be nil")
	}
	if len(tokenTransaction.TokenOutputs) == 0 {
		return nil, fmt.Errorf("mint token transaction must have outputs")
	}
	totalAmount := new(big.Int)
	for i, output := range tokenTransaction.TokenOutputs {
		if output.GetTokenAmount() == nil {
			return nil, fmt.Errorf("token amount at output %d cannot be nil", i)
		}
		amount := new(big.Int).SetBytes(output.GetTokenAmount())
		totalAmount.Add(totalAmount, amount)
	}
	return totalAmount, nil
}

// ValidateTransactionMintAgainstMaxSupply calculates the mint amount from a transaction
// and validates it against the current and max supply.
func ValidateTransactionMintAgainstMaxSupply(tokenTransaction *tokenpb.TokenTransaction, currentSupply, maxSupply *big.Int) error {
	newMintAmount, err := CalculateMintAmountFromTransaction(tokenTransaction)
	if err != nil {
		return fmt.Errorf("failed to calculate mint amount: %w", err)
	}

	if currentSupply == nil {
		return fmt.Errorf("current supply cannot be nil")
	}
	if maxSupply == nil {
		return fmt.Errorf("max supply cannot be nil")
	}

	newTotalSupply := new(big.Int).Add(currentSupply, newMintAmount)
	if newTotalSupply.Cmp(maxSupply) > 0 {
		return fmt.Errorf("mint would exceed max supply: total supply after mint (%s) would exceed max supply (%s)",
			newTotalSupply.String(), maxSupply.String())
	}

	return nil
}

// validateBaseTokenTransaction validates the base structure of token transactions
// that is common to both partial and final transactions (eg. excluding SO-filled fields).
func validateBaseTokenTransaction(
	tokenTransaction *tokenpb.TokenTransaction,
	inputSignatures []*tokenpb.SignatureWithIndex,
	expectedSparkOperators map[string]*sparkpb.SigningOperatorInfo,
	supportedNetworks []common.Network,
	requireTokenIdentifierForMints bool,
	requireTokenIdentifierForTransfers bool,
) error {
	if tokenTransaction == nil {
		return fmt.Errorf("token transaction cannot be nil")
	}
	if len(tokenTransaction.TokenOutputs) > MaxInputOrOutputTokenTransactionOutputs {
		return fmt.Errorf("too many token outputs, maximum is %d", MaxInputOrOutputTokenTransactionOutputs)
	}
	network, err := common.NetworkFromProtoNetwork(tokenTransaction.Network)
	if err != nil {
		return fmt.Errorf("failed to convert network: %w", err)
	}

	if !isNetworkSupported(network, supportedNetworks) {
		return fmt.Errorf("network %s is not supported", network)
	}

	if !st.TokenTransactionVersion(tokenTransaction.Version).IsValid() {
		return fmt.Errorf("invalid token transaction version: %d", tokenTransaction.Version)
	}

	inputCount := 0
	if tokenTransaction.GetMintInput() != nil {
		inputCount++
	}
	if tokenTransaction.GetTransferInput() != nil {
		inputCount++
	}
	if tokenTransaction.GetCreateInput() != nil {
		inputCount++
	}
	if inputCount != 1 {
		return fmt.Errorf("token transaction must have exactly one of create_input, mint_input, or transfer_input")
	}

	// Validate that the transaction has exactly one input type
	inputType, err := InferTokenTransactionType(tokenTransaction)
	if err != nil {
		return err
	}

	switch inputType {
	case TokenTransactionTypeCreate:
		if err := validateBaseCreateTransaction(tokenTransaction, inputSignatures); err != nil {
			return err
		}
	case TokenTransactionTypeMint:
		if err := validateBaseMintTransaction(tokenTransaction, inputSignatures, requireTokenIdentifierForMints); err != nil {
			return err
		}
	case TokenTransactionTypeTransfer:
		if err := validateBaseTransferTransaction(tokenTransaction, inputSignatures, requireTokenIdentifierForTransfers); err != nil {
			return err
		}
	default:
		return fmt.Errorf("token transaction type unknown")
	}

	// Check that each operator's public key is present.
	for _, expectedOperator := range expectedSparkOperators {
		found := false
		configPubKey := expectedOperator.GetPublicKey()
		for _, pubKey := range tokenTransaction.GetSparkOperatorIdentityPublicKeys() {
			if bytes.Equal(pubKey, configPubKey) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("missing spark operator identity public key for operator %s", expectedOperator.GetIdentifier())
		}
	}

	return nil
}

// FinalValidationConfig contains the configuration parameters for validating final token transactions
type FinalValidationConfig struct {
	ExpectedSparkOperators             map[string]*sparkpb.SigningOperatorInfo
	SupportedNetworks                  []common.Network
	RequireTokenIdentifierForMints     bool
	RequireTokenIdentifierForTransfers bool
	ExpectedRevocationPublicKeys       []keys.Public
	ExpectedBondSats                   uint64
	ExpectedRelativeBlockLocktime      uint64
	ExpectedCreationEntityPublicKey    keys.Public
}

// ValidateFinalTokenTransaction validates that the final token transaction
// is a commitable combination of user-filled and SO-filled fields.
func ValidateFinalTokenTransaction(
	tokenTransaction *tokenpb.TokenTransaction,
	signaturesWithIndex []*tokenpb.SignatureWithIndex,
	config *FinalValidationConfig,
) error {
	err := validateBaseTokenTransaction(
		tokenTransaction,
		signaturesWithIndex,
		config.ExpectedSparkOperators,
		config.SupportedNetworks,
		config.RequireTokenIdentifierForMints,
		config.RequireTokenIdentifierForTransfers,
	)
	if err != nil {
		return fmt.Errorf("failed to validate shared token transaction structure: %w", err)
	}

	inputType, err := InferTokenTransactionType(tokenTransaction)
	if err != nil {
		return err
	}
	// Ensure SO-filled fields in the final transaction are filled as expected to protect against
	// the coordinator SO from filling with values that peer SOs may not expect.
	switch inputType {
	case TokenTransactionTypeCreate:
		createInput := tokenTransaction.GetCreateInput()
		creationPubKey, err := keys.ParsePublicKey(createInput.GetCreationEntityPublicKey())
		if err != nil {
			return fmt.Errorf("unable to parse creation entity public key: %w", err)
		}
		if !creationPubKey.Equals(config.ExpectedCreationEntityPublicKey) {
			return fmt.Errorf("creation entity public key does not match the reserved entity public key")
		}
	case TokenTransactionTypeMint, TokenTransactionTypeTransfer:
		for i, output := range tokenTransaction.TokenOutputs {
			revocationCommitment, err := keys.ParsePublicKey(output.GetRevocationCommitment())
			if err != nil {
				return fmt.Errorf("unable to parse revocation commitment: %w", err)
			}
			if !revocationCommitment.Equals(config.ExpectedRevocationPublicKeys[i]) {
				return fmt.Errorf("revocation commitment mismatch for output %d", i)
			}
			if output.WithdrawBondSats == nil || output.WithdrawRelativeBlockLocktime == nil {
				return fmt.Errorf("withdrawal params not set for output %d", i)
			}
			if output.GetWithdrawBondSats() != config.ExpectedBondSats {
				return fmt.Errorf("withdrawal bond sats mismatch for output %d", i)
			}
			if output.GetWithdrawRelativeBlockLocktime() != config.ExpectedRelativeBlockLocktime {
				return fmt.Errorf("withdrawal locktime mismatch for output %d", i)
			}
		}
	default:
		return fmt.Errorf("token transaction type unknown")
	}
	return nil
}
