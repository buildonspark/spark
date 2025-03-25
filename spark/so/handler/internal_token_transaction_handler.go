package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/helper"
	"github.com/lightsparkdev/spark-go/so/lrc20"
	"github.com/lightsparkdev/spark-go/so/utils"
	"google.golang.org/protobuf/types/known/emptypb"
)

// InternalTokenTransactionHandler is the deposit handler for so internal
type InternalTokenTransactionHandler struct {
	config *so.Config
}

// NewInternalTokenTransactionHandler creates a new InternalTokenTransactionHandler.
func NewInternalTokenTransactionHandler(config *so.Config) *InternalTokenTransactionHandler {
	return &InternalTokenTransactionHandler{config: config}
}

func (h *InternalTokenTransactionHandler) StartTokenTransactionInternal(ctx context.Context, config *so.Config, req *pbinternal.StartTokenTransactionInternalRequest) (*emptypb.Empty, error) {
	logger := helper.GetLoggerFromContext(ctx)
	logger.Info("Starting token transaction", "key share ids", len(req.KeyshareIds))
	keyshareUUIDs := make([]uuid.UUID, len(req.KeyshareIds))
	// Ensure that the coordinator SO did not pass duplicate keyshare UUIDs for different leaves.
	seenUUIDs := make(map[uuid.UUID]bool)
	for i, id := range req.KeyshareIds {
		uuid, err := uuid.Parse(id)
		if err != nil {
			log.Printf("Failed to parse keyshare ID: %v", err)
			return nil, err
		}
		if seenUUIDs[uuid] {
			return nil, fmt.Errorf("duplicate keyshare UUID found: %s", uuid)
		}
		seenUUIDs[uuid] = true
		keyshareUUIDs[i] = uuid
	}
	logger.Info("Marking keyshares as used")
	keysharesMap, err := ent.MarkSigningKeysharesAsUsed(ctx, config, keyshareUUIDs)
	if err != nil {
		log.Printf("Failed to mark keyshares as used: %v", err)
		return nil, err
	}
	logger.Info("Keyshares marked as used")
	expectedRevocationPublicKeys := make([][]byte, len(req.KeyshareIds))
	for i, id := range keyshareUUIDs {
		keyshare, ok := keysharesMap[id]
		if !ok {
			return nil, fmt.Errorf("keyshare ID not found: %s", id)
		}
		expectedRevocationPublicKeys[i] = keyshare.PublicKey
	}

	logger.Info("Validating final token transaction")
	// Validate the final token transaction.
	err = validateFinalTokenTransaction(ctx, config, req.FinalTokenTransaction, req.TokenTransactionSignatures, expectedRevocationPublicKeys)
	if err != nil {
		return nil, fmt.Errorf("invalid final token transaction: %w", err)
	}
	if req.FinalTokenTransaction.GetMintInput() != nil {
		if req.FinalTokenTransaction.GetMintInput().GetIssuerProvidedTimestamp() == 0 {
			return nil, errors.New("issuer provided timestamp must be set for mint transaction")
		}
		err = ValidateMintSignature(req.FinalTokenTransaction, req.TokenTransactionSignatures)
		if err != nil {
			return nil, fmt.Errorf("invalid token transaction: %w", err)
		}
	}
	var leafToSpendEnts []*ent.TokenLeaf
	if req.FinalTokenTransaction.GetTransferInput() != nil {
		// Get the leaves to spend from the database.
		leafToSpendEnts, err = ent.FetchInputLeaves(ctx, req.FinalTokenTransaction.GetTransferInput().GetLeavesToSpend())
		if err != nil {
			return nil, fmt.Errorf("failed to fetch leaves to spend: %w", err)
		}
		if len(leafToSpendEnts) != len(req.FinalTokenTransaction.GetTransferInput().GetLeavesToSpend()) {
			return nil, fmt.Errorf("failed to fetch all leaves to spend: got %d leaves, expected %d", len(leafToSpendEnts), len(req.FinalTokenTransaction.GetTransferInput().GetLeavesToSpend()))
		}

		err = ValidateTokenTransactionUsingPreviousTransactionData(req.FinalTokenTransaction, req.TokenTransactionSignatures, leafToSpendEnts)
		if err != nil {
			return nil, fmt.Errorf("error validating transfer using previous leaf data: %w", err)
		}
	}
	logger.Info("Final token transaction validated")

	logger.Info("Verifying token transaction with LRC20 node")
	err = h.VerifyTokenTransactionWithLrc20Node(ctx, config, req.FinalTokenTransaction)
	if err != nil {
		return nil, err
	}
	logger.Info("Token transaction verified with LRC20 node")
	// Save the token transaction receipt, created leaf ents, and update the leaves to spend.
	_, err = ent.CreateStartedTransactionEntities(ctx, req.FinalTokenTransaction, req.TokenTransactionSignatures, req.KeyshareIds, leafToSpendEnts)
	if err != nil {
		return nil, fmt.Errorf("failed to save token transaction receipt and leaf ents: %w", err)
	}

	return &emptypb.Empty{}, nil
}

func (h *InternalTokenTransactionHandler) VerifyTokenTransactionWithLrc20Node(ctx context.Context, config *so.Config, tokenTransaction *pb.TokenTransaction) error {
	client := lrc20.NewClient(config)
	return client.VerifySparkTx(ctx, tokenTransaction)
}

func ValidateMintSignature(
	tokenTransaction *pb.TokenTransaction,
	tokenTransactionSignatures *pb.TokenTransactionSignatures,
) error {
	// Although this token transaction is final we pass in 'true' to generate the partial hash.
	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		return fmt.Errorf("failed to hash token transaction: %w", err)
	}

	err = utils.ValidateOwnershipSignature(tokenTransactionSignatures.GetOwnerSignatures()[0], partialTokenTransactionHash, tokenTransaction.GetMintInput().GetIssuerPublicKey())
	if err != nil {
		return fmt.Errorf("invalid issuer signature: %w", err)
	}

	return nil
}

func ValidateTokenTransactionUsingPreviousTransactionData(
	tokenTransaction *pb.TokenTransaction,
	tokenTransactionSignatures *pb.TokenTransactionSignatures,
	leafToSpendEnts []*ent.TokenLeaf,
) error {
	// Validate that the correct number of signatures were provided
	if len(tokenTransactionSignatures.GetOwnerSignatures()) != len(leafToSpendEnts) {
		return fmt.Errorf("number of signatures must match number of ownership public keys")
	}

	// Validate that all token public keys in leaves to spend match the output leaves.
	// Ok to just check against the first output because output token public key uniformity
	// is checked in the main ValidateTokenTransaction() call.
	expectedTokenPubKey := tokenTransaction.OutputLeaves[0].GetTokenPublicKey()
	if expectedTokenPubKey == nil {
		return fmt.Errorf("token public key cannot be nil in output leaves")
	}
	for i, leafEnt := range leafToSpendEnts {
		if !bytes.Equal(leafEnt.TokenPublicKey, expectedTokenPubKey) {
			return fmt.Errorf("token public key mismatch for leaf %d - input leaves must be for the same token public key as the output", i)
		}

		// TODO(DL-104): For now we allow the network to be nil to support old leaves. In the future we should require it to be set.
		if leafEnt.Network != schema.Network("") {
			entNetwork, err := leafEnt.Network.MarshalProto()
			if err != nil {
				return fmt.Errorf("failed to marshal network: %w", err)
			}
			if entNetwork != tokenTransaction.Network {
				return fmt.Errorf("network mismatch for leaf %d - input leaves network must match the network of the transaction (leaf.network = %d; tx.network = %d)", i, entNetwork, tokenTransaction.Network)
			}
		}
	}

	// Validate token conservation in inputs + outputs.
	totalInputAmount := new(big.Int)
	for _, leafEnt := range leafToSpendEnts {
		inputAmount := new(big.Int).SetBytes(leafEnt.TokenAmount)
		totalInputAmount.Add(totalInputAmount, inputAmount)
	}
	totalOutputAmount := new(big.Int)
	for _, outputLeaf := range tokenTransaction.OutputLeaves {
		outputAmount := new(big.Int).SetBytes(outputLeaf.GetTokenAmount())
		totalOutputAmount.Add(totalOutputAmount, outputAmount)
	}
	if totalInputAmount.Cmp(totalOutputAmount) != 0 {
		return fmt.Errorf("total input amount %s does not match total output amount %s", totalInputAmount.String(), totalOutputAmount.String())
	}

	// Validate that the ownership signatures match the ownership public keys in the leaves to spend.
	// Although this token transaction is final we pass in 'true' to generate the partial hash.
	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		return fmt.Errorf("failed to hash token transaction: %w", err)
	}

	for i, ownershipSignature := range tokenTransactionSignatures.GetOwnerSignatures() {
		if ownershipSignature == nil {
			return fmt.Errorf("ownership signature cannot be nil")
		}

		err = utils.ValidateOwnershipSignature(ownershipSignature, partialTokenTransactionHash, leafToSpendEnts[i].OwnerPublicKey)
		if err != nil {
			return fmt.Errorf("invalid ownership signature for leaf %d: %w", i, err)
		}
	}

	for i, leafEnt := range leafToSpendEnts {
		err := validateLeafIsSpendable(i, leafEnt)
		if err != nil {
			return err
		}
	}

	return nil
}

// validateLeafIsSpendable checks if a leaf is eligible to be spent by verifying:
// 1. The leaf has an appropriate status (Created+Finalized or already marked as SpentStarted)
// 2. The leaf hasn't been withdrawn already
func validateLeafIsSpendable(index int, leaf *ent.TokenLeaf) error {
	if !isValidLeafStatus(leaf.Status) {
		return fmt.Errorf("leaf %d cannot be spent: invalid status %s (must be CreatedFinalized or SpentStarted)",
			index, leaf.Status)
	}

	if leaf.ConfirmedWithdrawBlockHash != nil {
		return fmt.Errorf("leaf %d cannot be spent: already withdrawn", index)
	}

	return nil
}

// isValidLeafStatus checks if a leaf's status allows it to be spent.
func isValidLeafStatus(status schema.TokenLeafStatus) bool {
	return status == schema.TokenLeafStatusCreatedFinalized ||
		status == schema.TokenLeafStatusSpentStarted
}

func validateFinalTokenTransaction(
	ctx context.Context,
	config *so.Config,
	tokenTransaction *pb.TokenTransaction,
	tokenTransactionSignatures *pb.TokenTransactionSignatures,
	expectedRevocationPublicKeys [][]byte,
) error {
	logger := helper.GetLoggerFromContext(ctx)
	network, err := common.NetworkFromProtoNetwork(tokenTransaction.Network)
	if err != nil {
		logger.Error("Failed to get network from proto network", "error", err)
		return err
	}
	expectedBondSats := config.Lrc20Configs[network.String()].WithdrawBondSats
	expectedRelativeBlockLocktime := config.Lrc20Configs[network.String()].WithdrawRelativeBlockLocktime
	sparkOperatorsFromConfig := config.GetSigningOperatorList()
	// Repeat same validations as for the partial token transaction.
	err = utils.ValidatePartialTokenTransaction(tokenTransaction, tokenTransactionSignatures, sparkOperatorsFromConfig, config.SupportedNetworks)
	if err != nil {
		return fmt.Errorf("failed to validate final token transaction: %w", err)
	}

	// Additionally validate the revocation public keys and withdrawal params which were added to make it final.
	for i, leaf := range tokenTransaction.OutputLeaves {
		if leaf.GetRevocationPublicKey() == nil {
			return fmt.Errorf("revocation public key cannot be nil for leaf %d", i)
		}
		if !bytes.Equal(leaf.GetRevocationPublicKey(), expectedRevocationPublicKeys[i]) {
			return fmt.Errorf("revocation public key mismatch for leaf %d", i)
		}
		if leaf.WithdrawBondSats == nil || leaf.WithdrawRelativeBlockLocktime == nil {
			return fmt.Errorf("withdrawal params not set for leaf %d", i)
		}
		if leaf.GetWithdrawBondSats() != expectedBondSats {
			return fmt.Errorf("withdrawal bond sats mismatch for leaf %d", i)
		}
		if leaf.GetWithdrawRelativeBlockLocktime() != expectedRelativeBlockLocktime {
			return fmt.Errorf("withdrawal locktime mismatch for leaf %d", i)
		}
	}
	return nil
}
