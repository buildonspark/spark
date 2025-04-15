package ent

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark-go/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark-go/so/utils"
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

func CreateStartedTransactionEntities(
	ctx context.Context,
	tokenTransaction *pb.TokenTransaction,
	tokenTransactionSignatures *pb.TokenTransactionSignatures,
	outputRevocationKeyshareIDs []string,
	outputToSpendEnts []*TokenOutput,
) (*TokenTransaction, error) {
	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		log.Printf("Failed to hash partial token transaction: %v", err)
		return nil, err
	}
	finalTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, false)
	if err != nil {
		log.Printf("Failed to hash final token transaction: %v", err)
		return nil, err
	}

	db := GetDbFromContext(ctx)
	var tokenMintEnt *TokenMint
	if tokenTransaction.GetMintInput() != nil {
		tokenMintEnt, err = db.TokenMint.Create().
			SetIssuerPublicKey(tokenTransaction.GetMintInput().GetIssuerPublicKey()).
			SetIssuerSignature(tokenTransactionSignatures.GetOwnerSignatures()[0]).
			SetWalletProvidedTimestamp(tokenTransaction.GetMintInput().GetIssuerProvidedTimestamp()).
			Save(ctx)
		if err != nil {
			log.Printf("Failed to create token mint ent: %v", err)
			return nil, err
		}
	}

	txUpdate := db.TokenTransaction.Create().
		SetPartialTokenTransactionHash(partialTokenTransactionHash).
		SetFinalizedTokenTransactionHash(finalTokenTransactionHash).
		SetStatus(schema.TokenTransactionStatusStarted)
	if tokenMintEnt != nil {
		txUpdate.SetMintID(tokenMintEnt.ID)
	}
	tokenTransactionEnt, err := txUpdate.Save(ctx)
	if err != nil {
		log.Printf("Failed to create token transaction: %v", err)
		return nil, err
	}

	if tokenTransaction.GetTransferInput() != nil {
		ownershipSignatures := tokenTransactionSignatures.GetOwnerSignatures()
		if len(ownershipSignatures) != len(outputToSpendEnts) {
			return nil, fmt.Errorf(
				"number of signatures %d doesn't match number of outputs to spend %d",
				len(ownershipSignatures),
				len(outputToSpendEnts),
			)
		}

		for outputIndex, outputToSpendEnt := range outputToSpendEnts {
			var network schema.Network
			err := network.UnmarshalProto(tokenTransaction.Network)
			if err != nil {
				return nil, err
			}
			_, err = db.TokenOutput.UpdateOne(outputToSpendEnt).
				SetStatus(schema.TokenOutputStatusSpentStarted).
				SetOutputSpentTokenTransactionID(tokenTransactionEnt.ID).
				SetSpentOwnershipSignature(ownershipSignatures[outputIndex]).
				SetSpentTransactionInputVout(int32(outputIndex)).
				SetNetwork(network).
				Save(ctx)
			if err != nil {
				log.Printf("Failed to update output to spend: %v", err)
				return nil, err
			}
		}
	}

	outputEnts := make([]*TokenOutputCreate, 0, len(tokenTransaction.OutputLeaves))
	for outputIndex, output := range tokenTransaction.OutputLeaves {
		revocationUUID, err := uuid.Parse(outputRevocationKeyshareIDs[outputIndex])
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
				SetWithdrawRevocationCommitment(output.RevocationPublicKey).
				SetTokenPublicKey(output.TokenPublicKey).
				SetTokenAmount(output.TokenAmount).
				SetCreatedTransactionOutputVout(int32(outputIndex)).
				SetRevocationKeyshareID(revocationUUID).
				SetOutputCreatedTokenTransactionID(tokenTransactionEnt.ID),
		)
	}
	_, err = db.TokenOutput.CreateBulk(outputEnts...).Save(ctx)
	if err != nil {
		log.Printf("Failed to create token outputs: %v", err)
		return nil, err
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
		log.Printf("Failed to bulk update output status to signed: %v", err)
		return err
	}

	return nil
}

// UpdateFinalizedTransaction updates the status and ownership signatures of the finalized input + output outputs.
func UpdateFinalizedTransaction(
	ctx context.Context,
	tokenTransactionEnt *TokenTransaction,
	revocationKeys [][]byte,
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
	if len(revocationKeys) != len(spentLeaves) {
		return fmt.Errorf(
			"number of revocation keys (%d) does not match number of spent outputs (%d)",
			len(revocationKeys),
			len(spentLeaves),
		)
	}
	// Update inputs.
	for _, outputToSpendEnt := range tokenTransactionEnt.Edges.SpentOutput {
		inputIndex := outputToSpendEnt.SpentTransactionInputVout
		_, err := GetDbFromContext(ctx).TokenOutput.UpdateOne(outputToSpendEnt).
			SetStatus(schema.TokenOutputStatusSpentFinalized).
			SetSpentRevocationSecret(revocationKeys[inputIndex]).
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
		log.Printf("Failed to bulk update output status to signed: %v", err)
		return err
	}
	return nil
}

// UpdateCancelledTransaction updates the status and ownership signatures in the inputs + outputs in response to a cancelled transaction.
func UpdateCancelledTransaction(
	ctx context.Context,
	tokenTransactionEnt *TokenTransaction,
) error {
	// Update the token transaction with the operator signature and new status.
	_, err := GetDbFromContext(ctx).TokenTransaction.UpdateOne(tokenTransactionEnt).
		SetStatus(schema.TokenTransactionStatus(schema.TokenTransactionStatusSignedCancelled)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update token transaction with finalized status: %w", err)
	}

	// Change input statuses back to CREATED_FINALIZED to re-enable spending.
	spentLeaves := tokenTransactionEnt.Edges.SpentOutput
	for _, outputToSpendEnt := range spentLeaves {
		if outputToSpendEnt.Status != schema.TokenOutputStatusSpentSigned {
			return fmt.Errorf("spent output ID %s has status %s, expected %s",
				outputToSpendEnt.ID.String(),
				outputToSpendEnt.Status,
				schema.TokenOutputStatusSpentSigned)
		}
		_, err := GetDbFromContext(ctx).TokenOutput.UpdateOne(outputToSpendEnt).
			SetStatus(schema.TokenOutputStatusCreatedFinalized).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to cancel transaction and update spent output back to CREATED_FINALIZED: %w", err)
		}
	}

	// Change output output statuses to SIGNED_CANCELLED to invalidate them.
	outputIDs := make([]uuid.UUID, len(tokenTransactionEnt.Edges.CreatedOutput))
	for i, output := range tokenTransactionEnt.Edges.CreatedOutput {
		outputIDs[i] = output.ID
		// Verify output is in the expected state.
		if output.Status != schema.TokenOutputStatusCreatedSigned {
			return fmt.Errorf("created output ID %s has status %s, expected %s",
				output.ID.String(),
				output.Status,
				schema.TokenOutputStatusCreatedSigned)
		}
	}
	_, err = GetDbFromContext(ctx).TokenOutput.Update().
		Where(tokenoutput.IDIn(outputIDs...)).
		SetStatus(schema.TokenOutputStatusCreatedSignedCancelled).
		Save(ctx)
	if err != nil {
		log.Printf("Failed to bulk update output status to signed: %v", err)
		return err
	}
	return nil
}

// FetchTokenTransactionData refetches the transaction with all its relations.
func FetchAndLockTokenTransactionData(ctx context.Context, finalTokenTransaction *pb.TokenTransaction) (*TokenTransaction, error) {
	finalTokenTransactionHash, err := utils.HashTokenTransaction(finalTokenTransaction, false)
	if err != nil {
		return nil, err
	}

	tokenTransction, err := GetDbFromContext(ctx).TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHash(finalTokenTransactionHash)).
		WithCreatedOutput().
		WithSpentOutput().
		WithMint().
		ForUpdate().
		Only(ctx)
	if err != nil {
		return nil, err
	}

	// Sanity check that inputs and outputs matching the expected length were found.
	if finalTokenTransaction.GetMintInput() != nil {
		if tokenTransction.Edges.Mint == nil {
			return nil, fmt.Errorf("mint transaction must have a mint record, but none was found")
		}
	} else { // Transfer
		if len(finalTokenTransaction.GetTransferInput().LeavesToSpend) != len(tokenTransction.Edges.SpentOutput) {
			return nil, fmt.Errorf(
				"number of inputs in transaction (%d) does not match number of spent outputs in transaction (%d)",
				len(finalTokenTransaction.GetTransferInput().LeavesToSpend),
				len(tokenTransction.Edges.SpentOutput),
			)
		}
	}
	if len(finalTokenTransaction.OutputLeaves) != len(tokenTransction.Edges.CreatedOutput) {
		return nil, fmt.Errorf(
			"number of outputs in transaction (%d) does not match number of created outputs in transaction (%d)",
			len(finalTokenTransaction.OutputLeaves),
			len(tokenTransction.Edges.CreatedOutput),
		)
	}
	return tokenTransction, nil
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
		OutputLeaves: make([]*pb.TokenLeafOutput, len(r.Edges.CreatedOutput)),
		// Get all operator identity public keys from the config
		SparkOperatorIdentityPublicKeys: operatorPublicKeys,
	}

	// Set up output outputs
	for i, output := range r.Edges.CreatedOutput {
		idStr := output.ID.String()
		tokenTransaction.OutputLeaves[i] = &pb.TokenLeafOutput{
			Id:                            &idStr,
			OwnerPublicKey:                output.OwnerPublicKey,
			RevocationPublicKey:           output.WithdrawRevocationCommitment,
			WithdrawBondSats:              &output.WithdrawBondSats,
			WithdrawRelativeBlockLocktime: &output.WithdrawRelativeBlockLocktime,
			TokenPublicKey:                output.TokenPublicKey,
			TokenAmount:                   output.TokenAmount,
		}
	}

	// Determine if this is a mint or transfer transaction.
	if r.Edges.Mint != nil {
		// This is a mint transaction.
		tokenTransaction.TokenInput = &pb.TokenTransaction_MintInput{
			MintInput: &pb.MintInput{
				IssuerPublicKey:         r.Edges.Mint.IssuerPublicKey,
				IssuerProvidedTimestamp: r.Edges.Mint.WalletProvidedTimestamp,
			},
		}
	} else if len(r.Edges.SpentOutput) > 0 {
		// This is a transfer transaction
		transferInput := &pb.TransferInput{
			LeavesToSpend: make([]*pb.TokenLeafToSpend, len(r.Edges.SpentOutput)),
		}

		for i, output := range r.Edges.SpentOutput {
			// Since we assume all relationships are loaded, we can directly access the created transaction.
			if output.Edges.OutputCreatedTokenTransaction == nil {
				return nil, fmt.Errorf("output created transaction edge not loaded for output %s", output.ID)
			}

			transferInput.LeavesToSpend[i] = &pb.TokenLeafToSpend{
				PrevTokenTransactionHash:     output.Edges.OutputCreatedTokenTransaction.FinalizedTokenTransactionHash,
				PrevTokenTransactionLeafVout: uint32(output.CreatedTransactionOutputVout),
			}
		}

		tokenTransaction.TokenInput = &pb.TokenTransaction_TransferInput{
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
