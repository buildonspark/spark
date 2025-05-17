package ent

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/utils"
)

func GetTokenTransactionMapFromList(transactions []*TokenTransaction) (map[string]*TokenTransaction, error) {
	tokenTransactionMap := make(map[string]*TokenTransaction)
	for _, r := range transactions {
		if len(r.FinalizedTokenTransactionHash) > 0 {
			key := hex.EncodeToString(r.FinalizedTokenTransactionHash)
			tokenTransactionMap[key] = r
		}
	}
	return tokenTransactionMap, nil
}

// Ordered fields are ordered according to the order of the input in the token transaction proto.
func CreateStartedTransactionEntities(
	ctx context.Context,
	tokenTransaction *pb.TokenTransaction,
	tokenTransactionSignatures *pb.TokenTransactionSignatures,
	orderedOutputToCreateRevocationKeyshareIDs []string,
	orderedOutputToSpendEnts []*TokenOutput,
	coordinatorPublicKey []byte,
	transactionExpiryTime time.Time,
) (*TokenTransaction, error) {
	logger := logging.GetLoggerFromContext(ctx)
	db := GetDbFromContext(ctx)

	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		return nil, fmt.Errorf("failed to hash partial token transaction: %w", err)
	}
	finalTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, false)
	if err != nil {
		return nil, fmt.Errorf("failed to hash final token transaction: %w", err)
	}

	var network schema.Network
	err = network.UnmarshalProto(tokenTransaction.Network)
	if err != nil {
		logger.Error("Failed to unmarshal network", "error", err)
		return nil, err
	}

	var tokenTransactionEnt *TokenTransaction
	if tokenTransaction.GetMintInput() != nil {
		tokenMintEnt, err := db.TokenMint.Create().
			SetIssuerPublicKey(tokenTransaction.GetMintInput().GetIssuerPublicKey()).
			SetIssuerSignature(tokenTransactionSignatures.GetOwnerSignatures()[0].Signature).
			SetWalletProvidedTimestamp(tokenTransaction.GetMintInput().GetIssuerProvidedTimestamp()).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create token mint ent, likely due to attempting to restart a mint transaction with a different operator: %w", err)
		}
		tokenTransactionEnt, err = db.TokenTransaction.Create().
			SetPartialTokenTransactionHash(partialTokenTransactionHash).
			SetFinalizedTokenTransactionHash(finalTokenTransactionHash).
			SetStatus(schema.TokenTransactionStatusStarted).
			SetCoordinatorPublicKey(coordinatorPublicKey).
			SetExpiryTime(transactionExpiryTime).
			SetMintID(tokenMintEnt.ID).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create mint token transaction: %w", err)
		}
	}

	if tokenTransaction.GetTransferInput() != nil {
		ownershipSignatures := tokenTransactionSignatures.GetOwnerSignatures()
		if len(ownershipSignatures) != len(orderedOutputToSpendEnts) {
			return nil, fmt.Errorf(
				"number of signatures %d doesn't match number of outputs to spend %d",
				len(ownershipSignatures),
				len(orderedOutputToSpendEnts),
			)
		}

		tokenTransactionEnt, err = db.TokenTransaction.Create().
			SetPartialTokenTransactionHash(partialTokenTransactionHash).
			SetFinalizedTokenTransactionHash(finalTokenTransactionHash).
			SetStatus(schema.TokenTransactionStatusStarted).
			SetCoordinatorPublicKey(coordinatorPublicKey).
			SetExpiryTime(transactionExpiryTime).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create transfer token transaction: %w", err)
		}

		for outputIndex, outputToSpendEnt := range orderedOutputToSpendEnts {
			_, err = db.TokenOutput.UpdateOne(outputToSpendEnt).
				SetStatus(schema.TokenOutputStatusSpentStarted).
				SetOutputSpentTokenTransactionID(tokenTransactionEnt.ID).
				SetSpentOwnershipSignature(ownershipSignatures[outputIndex].Signature).
				SetSpentTransactionInputVout(int32(outputIndex)).
				Save(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to update output to spend: %w", err)
			}
		}
	}

	outputEnts := make([]*TokenOutputCreate, 0, len(tokenTransaction.TokenOutputs))
	for outputIndex, output := range tokenTransaction.TokenOutputs {
		revocationUUID, err := uuid.Parse(orderedOutputToCreateRevocationKeyshareIDs[outputIndex])
		if err != nil {
			return nil, err
		}
		outputUUID, err := uuid.Parse(*output.Id)
		if err != nil {
			return nil, err
		}
		outputEnts = append(
			outputEnts,
			db.TokenOutput.
				Create().
				// TODO: Consider whether the coordinator instead of the wallet should define this ID.
				SetID(outputUUID).
				SetStatus(schema.TokenOutputStatusCreatedStarted).
				SetOwnerPublicKey(output.OwnerPublicKey).
				SetWithdrawBondSats(*output.WithdrawBondSats).
				SetWithdrawRelativeBlockLocktime(*output.WithdrawRelativeBlockLocktime).
				SetWithdrawRevocationCommitment(output.RevocationCommitment).
				SetTokenPublicKey(output.TokenPublicKey).
				SetTokenAmount(output.TokenAmount).
				SetNetwork(network).
				SetCreatedTransactionOutputVout(int32(outputIndex)).
				SetRevocationKeyshareID(revocationUUID).
				SetOutputCreatedTokenTransactionID(tokenTransactionEnt.ID).
				SetNetwork(network),
		)
	}
	_, err = db.TokenOutput.CreateBulk(outputEnts...).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create token outputs: %w", err)
	}
	return tokenTransactionEnt, nil
}

// UpdateSignedTransaction updates the status and ownership signatures of the inputs + outputs
// and the issuer signature (if applicable).
func UpdateSignedTransaction(
	ctx context.Context,
	tokenTransactionEnt *TokenTransaction,
	operatorSpecificOwnershipSignatures [][]byte,
	operatorSignature []byte,
) error {
	// Update the token transaction with the operator signature and new status
	_, err := GetDbFromContext(ctx).TokenTransaction.UpdateOne(tokenTransactionEnt).
		SetOperatorSignature(operatorSignature).
		SetStatus(schema.TokenTransactionStatusSigned).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update token transaction with operator signature and status: %w", err)
	}

	newInputStatus := schema.TokenOutputStatusSpentSigned
	newOutputLeafStatus := schema.TokenOutputStatusCreatedSigned
	if tokenTransactionEnt.Edges.Mint != nil {
		// If this is a mint, update status straight to finalized because a follow up Finalize() call
		// is not necessary for mint.
		newInputStatus = schema.TokenOutputStatusSpentFinalized
		newOutputLeafStatus = schema.TokenOutputStatusCreatedFinalized
		if len(operatorSpecificOwnershipSignatures) != 1 {
			return fmt.Errorf(
				"expected 1 ownership signature for mint, got %d",
				len(operatorSpecificOwnershipSignatures),
			)
		}

		_, err := GetDbFromContext(ctx).TokenMint.UpdateOne(tokenTransactionEnt.Edges.Mint).
			SetOperatorSpecificIssuerSignature(operatorSpecificOwnershipSignatures[0]).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to update mint with signature: %w", err)
		}
	}

	// Update inputs.
	if tokenTransactionEnt.Edges.SpentOutput != nil {
		for _, outputToSpendEnt := range tokenTransactionEnt.Edges.SpentOutput {
			spentLeaves := tokenTransactionEnt.Edges.SpentOutput
			if len(spentLeaves) == 0 {
				return fmt.Errorf("no spent outputs found for transaction. cannot finalize")
			}

			// Validate that we have the right number of revocation keys.
			if len(operatorSpecificOwnershipSignatures) != len(spentLeaves) {
				return fmt.Errorf(
					"number of operator specific ownership signatures (%d) does not match number of spent outputs (%d)",
					len(operatorSpecificOwnershipSignatures),
					len(spentLeaves),
				)
			}

			inputIndex := outputToSpendEnt.SpentTransactionInputVout
			_, err := GetDbFromContext(ctx).TokenOutput.UpdateOne(outputToSpendEnt).
				SetStatus(newInputStatus).
				SetSpentOperatorSpecificOwnershipSignature(operatorSpecificOwnershipSignatures[inputIndex]).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to update spent output to signed: %w", err)
			}
		}
	}

	// Update outputs.
	outputIDs := make([]uuid.UUID, len(tokenTransactionEnt.Edges.CreatedOutput))
	for i, output := range tokenTransactionEnt.Edges.CreatedOutput {
		outputIDs[i] = output.ID
	}
	_, err = GetDbFromContext(ctx).TokenOutput.Update().
		Where(tokenoutput.IDIn(outputIDs...)).
		SetStatus(newOutputLeafStatus).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to bulk update output status to signed: %w", err)
	}

	return nil
}

// UpdateFinalizedTransaction updates the status and ownership signatures of the finalized input + output outputs.
func UpdateFinalizedTransaction(
	ctx context.Context,
	tokenTransactionEnt *TokenTransaction,
	revocationSecrets []*pb.RevocationSecretWithIndex,
) error {
	// Update the token transaction with the operator signature and new status
	_, err := GetDbFromContext(ctx).TokenTransaction.UpdateOne(tokenTransactionEnt).
		SetStatus(schema.TokenTransactionStatusFinalized).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update token transaction with finalized status: %w", err)
	}

	spentLeaves := tokenTransactionEnt.Edges.SpentOutput
	if len(spentLeaves) == 0 {
		return fmt.Errorf("no spent outputs found for transaction. cannot finalize")
	}
	if len(revocationSecrets) != len(spentLeaves) {
		return fmt.Errorf(
			"number of revocation keys (%d) does not match number of spent outputs (%d)",
			len(revocationSecrets),
			len(spentLeaves),
		)
	}
	// Update inputs.
	for _, outputToSpendEnt := range tokenTransactionEnt.Edges.SpentOutput {
		inputIndex := outputToSpendEnt.SpentTransactionInputVout
		_, err := GetDbFromContext(ctx).TokenOutput.UpdateOne(outputToSpendEnt).
			SetStatus(schema.TokenOutputStatusSpentFinalized).
			SetSpentRevocationSecret(revocationSecrets[inputIndex].RevocationSecret).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to update spent output to signed: %w", err)
		}
	}

	// Update outputs.
	outputIDs := make([]uuid.UUID, len(tokenTransactionEnt.Edges.CreatedOutput))
	for i, output := range tokenTransactionEnt.Edges.CreatedOutput {
		outputIDs[i] = output.ID
	}
	_, err = GetDbFromContext(ctx).TokenOutput.Update().
		Where(tokenoutput.IDIn(outputIDs...)).
		SetStatus(schema.TokenOutputStatusCreatedFinalized).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to bulk update output status to finalized: %w", err)
	}
	return nil
}

// UpdateCancelledTransaction updates the status and ownership signatures in the inputs + outputs in response to a cancelled transaction.
func UpdateCancelledTransaction(
	ctx context.Context,
	tokenTransactionEnt *TokenTransaction,
) error {
	db := GetDbFromContext(ctx)

	// Collect all output IDs that need to be locked
	allOutputIDs := make([]uuid.UUID, 0, len(tokenTransactionEnt.Edges.SpentOutput)+len(tokenTransactionEnt.Edges.CreatedOutput))

	// Add spent output IDs
	spentLeaves := tokenTransactionEnt.Edges.SpentOutput
	for _, output := range spentLeaves {
		allOutputIDs = append(allOutputIDs, output.ID)
		// Verify output is in the expected state
		if output.Status != schema.TokenOutputStatusSpentStarted && output.Status != schema.TokenOutputStatusSpentSigned {
			return fmt.Errorf("spent output ID %s has status %s, expected %s or %s",
				output.ID.String(),
				output.Status,
				schema.TokenOutputStatusSpentStarted,
				schema.TokenOutputStatusSpentSigned)
		}
	}

	// Add created output IDs
	for _, output := range tokenTransactionEnt.Edges.CreatedOutput {
		allOutputIDs = append(allOutputIDs, output.ID)
		// Verify output is in the expected state
		if output.Status != schema.TokenOutputStatusCreatedStarted && output.Status != schema.TokenOutputStatusCreatedSigned {
			return fmt.Errorf("created output ID %s has status %s, expected %s or %s",
				output.ID.String(),
				output.Status,
				schema.TokenOutputStatusCreatedStarted,
				schema.TokenOutputStatusCreatedSigned)
		}
	}

	// Lock all outputs in a single query (token transaction was locked upstream).
	_, err := db.TokenOutput.Query().
		Where(tokenoutput.IDIn(allOutputIDs...)).
		ForUpdate().
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to lock all token outputs for update: %w", err)
	}

	var newStatus schema.TokenTransactionStatus
	if tokenTransactionEnt.Status == schema.TokenTransactionStatusStarted {
		newStatus = schema.TokenTransactionStatusStartedCancelled
	} else if tokenTransactionEnt.Status == schema.TokenTransactionStatusSigned {
		newStatus = schema.TokenTransactionStatusSignedCancelled
	} else {
		return fmt.Errorf("token transaction status %s is not valid for cancelling", tokenTransactionEnt.Status)
	}
	_, err = db.TokenTransaction.UpdateOne(tokenTransactionEnt).
		SetStatus(schema.TokenTransactionStatus(newStatus)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update token transaction with finalized status: %w", err)
	}

	// Update spent outputs to re-enable spending.
	_, err = db.TokenOutput.Update().
		Where(tokenoutput.StatusIn(schema.TokenOutputStatusSpentStarted, schema.TokenOutputStatusSpentSigned)).
		Where(tokenoutput.IDIn(allOutputIDs...)).
		SetStatus(schema.TokenOutputStatusCreatedFinalized).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to bulk update spent output status to created finalized: %w", err)
	}
	// Update created outputs to invalidate them
	_, err = db.TokenOutput.Update().
		Where(tokenoutput.StatusEQ(schema.TokenOutputStatusCreatedStarted)).
		Where(tokenoutput.IDIn(allOutputIDs...)).
		SetStatus(schema.TokenOutputStatusCreatedStartedCancelled).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to bulk update created output status to started cancelled: %w", err)
	}
	_, err = db.TokenOutput.Update().
		Where(tokenoutput.StatusEQ(schema.TokenOutputStatusCreatedSigned)).
		Where(tokenoutput.IDIn(allOutputIDs...)).
		SetStatus(schema.TokenOutputStatusCreatedSignedCancelled).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to bulk update created output status to signed cancelled: %w", err)
	}

	return nil
}

func FetchPartialTokenTransactionData(ctx context.Context, partialTokenTransactionHash []byte) (*TokenTransaction, error) {
	tokenTransaction, err := GetDbFromContext(ctx).TokenTransaction.Query().
		Where(tokentransaction.PartialTokenTransactionHash(partialTokenTransactionHash)).
		WithCreatedOutput().
		WithSpentOutput(func(q *TokenOutputQuery) {
			// Needed to enable marshalling of the token transaction proto.
			q.WithOutputCreatedTokenTransaction()
		}).
		WithMint().
		Only(ctx)
	if err != nil {
		return nil, err
	}
	return tokenTransaction, nil
}

// FetchTokenTransactionData refetches the transaction with all its relations.
func FetchAndLockTokenTransactionData(ctx context.Context, finalTokenTransaction *pb.TokenTransaction) (*TokenTransaction, error) {
	finalTokenTransactionHash, err := utils.HashTokenTransaction(finalTokenTransaction, false)
	if err != nil {
		return nil, err
	}

	tokenTransaction, err := GetDbFromContext(ctx).TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHash(finalTokenTransactionHash)).
		WithCreatedOutput().
		WithSpentOutput(func(q *TokenOutputQuery) {
			// Needed to enable marshalling of the token transaction proto.
			q.WithOutputCreatedTokenTransaction()
		}).
		WithMint().
		ForUpdate().
		Only(ctx)
	if err != nil {
		return nil, err
	}

	// Sanity check that inputs and outputs matching the expected length were found.
	if finalTokenTransaction.GetMintInput() != nil {
		if tokenTransaction.Edges.Mint == nil {
			return nil, fmt.Errorf("mint transaction must have a mint record, but none was found")
		}
	} else { // Transfer
		if len(finalTokenTransaction.GetTransferInput().OutputsToSpend) != len(tokenTransaction.Edges.SpentOutput) {
			return nil, fmt.Errorf(
				"number of inputs in proto (%d) does not match number of spent outputs started with this transaction in the database (%d)",
				len(finalTokenTransaction.GetTransferInput().OutputsToSpend),
				len(tokenTransaction.Edges.SpentOutput),
			)
		}
	}
	if len(finalTokenTransaction.TokenOutputs) != len(tokenTransaction.Edges.CreatedOutput) {
		return nil, fmt.Errorf(
			"number of outputs in proto (%d) does not match number of created outputs started with this transaction in the database (%d)",
			len(finalTokenTransaction.TokenOutputs),
			len(tokenTransaction.Edges.CreatedOutput),
		)
	}
	return tokenTransaction, nil
}

// MarshalProto converts a TokenTransaction to a spark protobuf TokenTransaction.
// This assumes the transaction already has all its relationships loaded.
func (r *TokenTransaction) MarshalProto(config *so.Config) (*pb.TokenTransaction, error) {
	// TODO: When adding support for adding/removing, we will need to save this per transaction rather than
	// pulling from the config.
	operatorPublicKeys := make([][]byte, 0, len(config.SigningOperatorMap))
	for _, operator := range config.SigningOperatorMap {
		operatorPublicKeys = append(operatorPublicKeys, operator.IdentityPublicKey)
	}

	// Create a new TokenTransaction
	tokenTransaction := &pb.TokenTransaction{
		TokenOutputs: make([]*pb.TokenOutput, len(r.Edges.CreatedOutput)),
		// Get all operator identity public keys from the config
		SparkOperatorIdentityPublicKeys: operatorPublicKeys,
	}

	// Set up output outputs
	for i, output := range r.Edges.CreatedOutput {
		idStr := output.ID.String()
		tokenTransaction.TokenOutputs[i] = &pb.TokenOutput{
			Id:                            &idStr,
			OwnerPublicKey:                output.OwnerPublicKey,
			RevocationCommitment:          output.WithdrawRevocationCommitment,
			WithdrawBondSats:              &output.WithdrawBondSats,
			WithdrawRelativeBlockLocktime: &output.WithdrawRelativeBlockLocktime,
			TokenPublicKey:                output.TokenPublicKey,
			TokenAmount:                   output.TokenAmount,
		}
	}

	// Determine if this is a mint or transfer transaction.
	if r.Edges.Mint != nil {
		// This is a mint transaction.
		tokenTransaction.TokenInputs = &pb.TokenTransaction_MintInput{
			MintInput: &pb.TokenMintInput{
				IssuerPublicKey:         r.Edges.Mint.IssuerPublicKey,
				IssuerProvidedTimestamp: r.Edges.Mint.WalletProvidedTimestamp,
			},
		}
	} else if len(r.Edges.SpentOutput) > 0 {
		// This is a transfer transaction
		transferInput := &pb.TokenTransferInput{
			OutputsToSpend: make([]*pb.TokenOutputToSpend, len(r.Edges.SpentOutput)),
		}

		for i, output := range r.Edges.SpentOutput {
			// Since we assume all relationships are loaded, we can directly access the created transaction.
			if output.Edges.OutputCreatedTokenTransaction == nil {
				return nil, fmt.Errorf("output spent transaction edge not loaded for output %s", output.ID)
			}

			transferInput.OutputsToSpend[i] = &pb.TokenOutputToSpend{
				PrevTokenTransactionHash: output.Edges.OutputCreatedTokenTransaction.FinalizedTokenTransactionHash,
				PrevTokenTransactionVout: uint32(output.CreatedTransactionOutputVout),
			}
		}

		tokenTransaction.TokenInputs = &pb.TokenTransaction_TransferInput{
			TransferInput: transferInput,
		}
	}

	// Set the network field based on the network values stored in the first created output.
	// All token transaction outputs must have the same network (confirmed in validation when signing
	// the transaction, so its safe to use the first output).
	if len(r.Edges.CreatedOutput) > 0 {
		networkProto, err := r.Edges.CreatedOutput[0].Network.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal network from created output: %w", err)
		}
		tokenTransaction.Network = networkProto
	} else {
		return nil, fmt.Errorf("no outputs were found when reconstructing token transaction with ID: %s", r.ID)
	}

	return tokenTransaction, nil
}
