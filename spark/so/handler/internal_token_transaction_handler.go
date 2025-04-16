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
	config      *so.Config
	lrc20Client *lrc20.Client
}

// NewInternalTokenTransactionHandler creates a new InternalTokenTransactionHandler.
func NewInternalTokenTransactionHandler(config *so.Config, client *lrc20.Client) *InternalTokenTransactionHandler {
	return &InternalTokenTransactionHandler{config: config, lrc20Client: client}
}

func (h *InternalTokenTransactionHandler) StartTokenTransactionInternal(ctx context.Context, config *so.Config, req *pbinternal.StartTokenTransactionInternalRequest) (*emptypb.Empty, error) {
	logger := helper.GetLoggerFromContext(ctx)
	logger.Info("Starting token transaction", "key share ids", len(req.KeyshareIds))
	keyshareUUIDs := make([]uuid.UUID, len(req.KeyshareIds))
	// Ensure that the coordinator SO did not pass duplicate keyshare UUIDs for different outputs.
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
	var outputToSpendEnts []*ent.TokenOutput
	if req.FinalTokenTransaction.GetTransferInput() != nil {
		// Get the leaves to spend from the database.
		outputToSpendEnts, err = ent.FetchTokenInputs(ctx, req.FinalTokenTransaction.GetTransferInput().GetOutputsToSpend())
		if err != nil {
			return nil, fmt.Errorf("failed to fetch outputs to spend: %w", err)
		}
		if len(outputToSpendEnts) != len(req.FinalTokenTransaction.GetTransferInput().GetOutputsToSpend()) {
			return nil, fmt.Errorf("failed to fetch all leaves to spend: got %d leaves, expected %d", len(outputToSpendEnts), len(req.FinalTokenTransaction.GetTransferInput().GetOutputsToSpend()))
		}

		err = ValidateTokenTransactionUsingPreviousTransactionData(req.FinalTokenTransaction, req.TokenTransactionSignatures, outputToSpendEnts)
		if err != nil {
			return nil, fmt.Errorf("error validating transfer using previous output data: %w", err)
		}
	}
	logger.Info("Final token transaction validated")

	logger.Info("Verifying token transaction with LRC20 node")
	err = h.VerifyTokenTransactionWithLrc20Node(ctx, req.FinalTokenTransaction)
	if err != nil {
		return nil, err
	}
	logger.Info("Token transaction verified with LRC20 node")
	// Save the token transaction, created output ents, and update the outputs to spend.
	_, err = ent.CreateStartedTransactionEntities(ctx, req.FinalTokenTransaction, req.TokenTransactionSignatures, req.KeyshareIds, outputToSpendEnts)
	if err != nil {
		return nil, fmt.Errorf("failed to save token transaction and output ents: %w", err)
	}

	return &emptypb.Empty{}, nil
}

func (h *InternalTokenTransactionHandler) VerifyTokenTransactionWithLrc20Node(ctx context.Context, tokenTransaction *pb.TokenTransaction) error {
	return h.lrc20Client.VerifySparkTx(ctx, tokenTransaction)
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
	outputToSpendEnts []*ent.TokenOutput,
) error {
	// Validate that the correct number of signatures were provided
	if len(tokenTransactionSignatures.GetOwnerSignatures()) != len(outputToSpendEnts) {
		return fmt.Errorf("number of signatures must match number of ownership public keys")
	}

	// Validate that all token public keys in outputs to spend match the outputs.
	// Ok to just check against the first output because output token public key uniformity
	// is checked in the main ValidateTokenTransaction() call.
	expectedTokenPubKey := tokenTransaction.TokenOutputs[0].GetTokenPublicKey()
	if expectedTokenPubKey == nil {
		return fmt.Errorf("token public key cannot be nil in outputs")
	}
	for i, outputEnt := range outputToSpendEnts {
		if !bytes.Equal(outputEnt.TokenPublicKey, expectedTokenPubKey) {
			return fmt.Errorf("token public key mismatch for output %d - input outputs must be for the same token public key as the output", i)
		}

		// TODO(DL-104): For now we allow the network to be nil to support old outputs. In the future we should require it to be set.
		if outputEnt.Network != schema.Network("") {
			entNetwork, err := outputEnt.Network.MarshalProto()
			if err != nil {
				return fmt.Errorf("failed to marshal network: %w", err)
			}
			if entNetwork != tokenTransaction.Network {
				return fmt.Errorf("network mismatch for output %d - input outputs network must match the network of the transaction (output.network = %d; tx.network = %d)", i, entNetwork, tokenTransaction.Network)
			}
		}
	}

	// Validate token conservation in inputs + outputs.
	totalInputAmount := new(big.Int)
	for _, outputEnt := range outputToSpendEnts {
		inputAmount := new(big.Int).SetBytes(outputEnt.TokenAmount)
		totalInputAmount.Add(totalInputAmount, inputAmount)
	}
	totalOutputAmount := new(big.Int)
	for _, outputLeaf := range tokenTransaction.TokenOutputs {
		outputAmount := new(big.Int).SetBytes(outputLeaf.GetTokenAmount())
		totalOutputAmount.Add(totalOutputAmount, outputAmount)
	}
	if totalInputAmount.Cmp(totalOutputAmount) != 0 {
		return fmt.Errorf("total input amount %s does not match total output amount %s", totalInputAmount.String(), totalOutputAmount.String())
	}

	// Validate that the ownership signatures match the ownership public keys in the outputs to spend.
	// Although this token transaction is final we pass in 'true' to generate the partial hash.
	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		return fmt.Errorf("failed to hash token transaction: %w", err)
	}

	for i, ownershipSignature := range tokenTransactionSignatures.GetOwnerSignatures() {
		if ownershipSignature == nil {
			return fmt.Errorf("ownership signature cannot be nil")
		}

		err = utils.ValidateOwnershipSignature(ownershipSignature, partialTokenTransactionHash, outputToSpendEnts[i].OwnerPublicKey)
		if err != nil {
			return fmt.Errorf("invalid ownership signature for output %d: %w", i, err)
		}
	}

	for i, outputEnt := range outputToSpendEnts {
		err := validateOutputIsSpendable(i, outputEnt)
		if err != nil {
			return err
		}
	}

	return nil
}

// validateOutputIsSpendable checks if a output is eligible to be spent by verifying:
// 1. The output has an appropriate status (Created+Finalized or already marked as SpentStarted)
// 2. The output hasn't been withdrawn already
func validateOutputIsSpendable(index int, output *ent.TokenOutput) error {
	if !isValidOutputStatus(output.Status) {
		return fmt.Errorf("output %d cannot be spent: invalid status %s (must be CreatedFinalized or SpentStarted)",
			index, output.Status)
	}

	if output.ConfirmedWithdrawBlockHash != nil {
		return fmt.Errorf("output %d cannot be spent: already withdrawn", index)
	}

	return nil
}

// isValidOutputStatus checks if a output's status allows it to be spent.
func isValidOutputStatus(status schema.TokenOutputStatus) bool {
	return status == schema.TokenOutputStatusCreatedFinalized ||
		status == schema.TokenOutputStatusSpentStarted
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
	for i, output := range tokenTransaction.TokenOutputs {
		if output.GetRevocationCommitment() == nil {
			return fmt.Errorf("revocation public key cannot be nil for output %d", i)
		}
		if !bytes.Equal(output.GetRevocationCommitment(), expectedRevocationPublicKeys[i]) {
			return fmt.Errorf("revocation public key mismatch for output %d", i)
		}
		if output.WithdrawBondSats == nil || output.WithdrawRelativeBlockLocktime == nil {
			return fmt.Errorf("withdrawal params not set for output %d", i)
		}
		if output.GetWithdrawBondSats() != expectedBondSats {
			return fmt.Errorf("withdrawal bond sats mismatch for output %d", i)
		}
		if output.GetWithdrawRelativeBlockLocktime() != expectedRelativeBlockLocktime {
			return fmt.Errorf("withdrawal locktime mismatch for output %d", i)
		}
	}
	return nil
}
