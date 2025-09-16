package ent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/sparkinvoice"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/protoconverter"
	"github.com/lightsparkdev/spark/so/utils"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	tokenTransaction *tokenpb.TokenTransaction,
	signaturesWithIndex []*tokenpb.SignatureWithIndex,
	orderedOutputToCreateRevocationKeyshareIDs []string,
	orderedOutputToSpendEnts []*TokenOutput,
	coordinatorPublicKey keys.Public,
) (*TokenTransaction, error) {
	// Ordered fields are ordered according to the order of the input in the token transaction proto.
	logger := logging.GetLoggerFromContext(ctx)
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		return nil, fmt.Errorf("failed to hash partial token transaction: %w", err)
	}
	finalTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, false)
	if err != nil {
		return nil, fmt.Errorf("failed to hash final token transaction: %w", err)
	}

	var network st.Network
	err = network.UnmarshalProto(tokenTransaction.Network)
	if err != nil {
		logger.Error("Failed to unmarshal network", zap.Error(err))
		return nil, err
	}

	var tokenTransactionEnt *TokenTransaction
	tokenTransactionType, err := utils.InferTokenTransactionType(tokenTransaction)
	if err != nil {
		return nil, sparkerrors.InternalErrorf("failed to infer token transaction type: %w", err)
	}
	switch tokenTransactionType {
	case utils.TokenTransactionTypeCreate:
		createInput := tokenTransaction.GetCreateInput()
		tokenMetadata, err := common.NewTokenMetadataFromCreateInput(createInput, tokenTransaction.Network)
		if err != nil {
			return nil, sparkerrors.InternalErrorf("failed to create token metadata: %w", err)
		}
		computedTokenIdentifier, err := tokenMetadata.ComputeTokenIdentifierV1()
		if err != nil {
			return nil, sparkerrors.InternalErrorf("failed to compute token identifier: %w", err)
		}

		issuerPubKey, err := keys.ParsePublicKey(createInput.GetIssuerPublicKey())
		if err != nil {
			return nil, fmt.Errorf("failed to parse issuer public key: %w", err)
		}
		creationEntityPubKey, err := keys.ParsePublicKey(createInput.GetCreationEntityPublicKey())
		if err != nil {
			return nil, fmt.Errorf("failed to parse creation entity public key: %w", err)
		}
		tokenCreateEnt, err := db.TokenCreate.Create().
			SetIssuerPublicKey(issuerPubKey).
			SetIssuerSignature(signaturesWithIndex[0].Signature).
			SetTokenName(createInput.GetTokenName()).
			SetTokenTicker(createInput.GetTokenTicker()).
			SetDecimals(uint8(createInput.GetDecimals())).
			SetMaxSupply(createInput.GetMaxSupply()).
			SetIsFreezable(createInput.GetIsFreezable()).
			SetCreationEntityPublicKey(creationEntityPubKey).
			SetNetwork(network).
			SetTokenIdentifier(computedTokenIdentifier).
			Save(ctx)
		if err != nil {
			return nil, sparkerrors.InternalErrorf("failed to create token create ent, likely due to attempting to restart a create transaction with a different operator: %w", err)
		}
		txBuilder := db.TokenTransaction.Create().
			SetPartialTokenTransactionHash(partialTokenTransactionHash).
			SetFinalizedTokenTransactionHash(finalTokenTransactionHash).
			SetStatus(st.TokenTransactionStatusStarted).
			SetCoordinatorPublicKey(coordinatorPublicKey.Serialize()).
			SetVersion(st.TokenTransactionVersion(tokenTransaction.Version)).
			SetCreateID(tokenCreateEnt.ID)
		if tokenTransaction.ExpiryTime != nil {
			txBuilder = txBuilder.SetExpiryTime(tokenTransaction.ExpiryTime.AsTime())
		}
		tokenTransactionEnt, err = txBuilder.Save(ctx)
		if err != nil {
			return nil, sparkerrors.InternalErrorf("failed to create create token transaction: %w", err)
		}
	case utils.TokenTransactionTypeMint:
		tokenMintEnt, err := db.TokenMint.Create().
			SetIssuerPublicKey(tokenTransaction.GetMintInput().GetIssuerPublicKey()).
			SetIssuerSignature(signaturesWithIndex[0].Signature).
			// TODO CNT-376: remove timestamp field from MintInput and use TokenTransaction.ClientCreatedTimestamp instead
			SetWalletProvidedTimestamp(uint64(tokenTransaction.ClientCreatedTimestamp.AsTime().UnixMilli())).
			SetTokenIdentifier(tokenTransaction.GetMintInput().GetTokenIdentifier()).
			Save(ctx)
		if err != nil {
			return nil, sparkerrors.InternalErrorf("failed to create token mint ent, likely due to attempting to restart a mint transaction with a different operator: %w", err)
		}
		txMintBuilder := db.TokenTransaction.Create().
			SetPartialTokenTransactionHash(partialTokenTransactionHash).
			SetFinalizedTokenTransactionHash(finalTokenTransactionHash).
			SetStatus(st.TokenTransactionStatusStarted).
			SetCoordinatorPublicKey(coordinatorPublicKey.Serialize()).
			SetClientCreatedTimestamp(tokenTransaction.ClientCreatedTimestamp.AsTime()).
			SetVersion(st.TokenTransactionVersion(tokenTransaction.Version)).
			SetMintID(tokenMintEnt.ID)
		if tokenTransaction.ExpiryTime != nil && tokenTransaction.Version != 0 {
			txMintBuilder = txMintBuilder.SetExpiryTime(tokenTransaction.ExpiryTime.AsTime())
		}
		tokenTransactionEnt, err = txMintBuilder.Save(ctx)
		if err != nil {
			return nil, sparkerrors.InternalErrorf("failed to create mint token transaction: %w", err)
		}
	case utils.TokenTransactionTypeTransfer:
		if len(signaturesWithIndex) != len(orderedOutputToSpendEnts) {
			return nil, sparkerrors.InternalErrorf(
				"number of signatures %d doesn't match number of outputs to spend %d",
				len(signaturesWithIndex),
				len(orderedOutputToSpendEnts),
			)
		}
		txTransferBuilder := db.TokenTransaction.Create().
			SetPartialTokenTransactionHash(partialTokenTransactionHash).
			SetFinalizedTokenTransactionHash(finalTokenTransactionHash).
			SetStatus(st.TokenTransactionStatusStarted).
			SetCoordinatorPublicKey(coordinatorPublicKey.Serialize()).
			SetClientCreatedTimestamp(tokenTransaction.ClientCreatedTimestamp.AsTime()).
			SetVersion(st.TokenTransactionVersion(tokenTransaction.Version))
		if tokenTransaction.ExpiryTime != nil && tokenTransaction.Version != 0 {
			txTransferBuilder = txTransferBuilder.SetExpiryTime(tokenTransaction.ExpiryTime.AsTime())
		}
		tokenTransactionEnt, err = txTransferBuilder.Save(ctx)
		if err != nil {
			return nil, sparkerrors.InternalErrorf("failed to create transfer token transaction: %w", err)
		}
		for outputIndex, outputToSpendEnt := range orderedOutputToSpendEnts {
			_, err = db.TokenOutput.UpdateOne(outputToSpendEnt).
				SetStatus(st.TokenOutputStatusSpentStarted).
				SetOutputSpentTokenTransactionID(tokenTransactionEnt.ID).
				AddOutputSpentStartedTokenTransactions(tokenTransactionEnt).
				SetSpentOwnershipSignature(signaturesWithIndex[outputIndex].Signature).
				SetSpentTransactionInputVout(int32(outputIndex)).
				Save(ctx)
			if err != nil {
				return nil, sparkerrors.InternalErrorf("failed to update output to spend: %w", err)
			}
		}
	case utils.TokenTransactionTypeUnknown:
	default:
		return nil, sparkerrors.InternalErrorf("token transaction type unknown")
	}
	if tokenTransaction.Version >= 2 && tokenTransaction.GetInvoiceAttachments() != nil {
		sparkInvoiceIDs, sparkInvoicesToCreate, err := prepareSparkInvoiceCreates(ctx, tokenTransaction, tokenTransactionEnt)
		if err != nil {
			return nil, sparkerrors.InternalErrorf("failed to prepare spark invoices: %w", err)
		}
		if len(sparkInvoicesToCreate) > 0 {
			err = db.SparkInvoice.CreateBulk(sparkInvoicesToCreate...).
				OnConflictColumns(sparkinvoice.FieldID).
				DoNothing().
				Exec(ctx)
			if err != nil {
				return nil, sparkerrors.InternalErrorf("failed to create spark invoices: %w", err)
			}
			sparkInvoiceIDsToAdd := make([]uuid.UUID, 0, len(sparkInvoiceIDs))
			for sparkInvoiceID := range sparkInvoiceIDs {
				sparkInvoiceIDsToAdd = append(sparkInvoiceIDsToAdd, sparkInvoiceID)
			}
			err = db.SparkInvoice.
				Update().
				Where(
					sparkinvoice.IDIn(sparkInvoiceIDsToAdd...),
					sparkinvoice.Not(
						sparkinvoice.HasTokenTransactionWith(tokentransaction.IDEQ(tokenTransactionEnt.ID)),
					),
				).
				AddTokenTransactionIDs(tokenTransactionEnt.ID).
				Exec(ctx)
			if err != nil {
				return nil, sparkerrors.InternalErrorf("failed to attach token transaction edge: %w", err)
			}
		}
	}

	// Clients provide one of tokenIdentifier or tokenPublicKey to the server to make transactions.
	// Older clients provide only tokenPublicKey. Newer clients provide only tokenIdentifier.
	//
	// To ensure both backwards and forwards compatibility, fetch and write the missing field.
	// Since we have already hashed the final token transaction, the txHash still represents
	// the original token transaction that was passed by the client.
	var tokenIdentifierToWrite []byte
	var issuerPublicKeyToWrite keys.Public

	tokenOutputs := tokenTransaction.GetTokenOutputs()
	var tokenCreateEnt *TokenCreate
	if len(tokenOutputs) > 0 {
		// We enforce one of tokenIdentifier or tokenPublicKey from the client.
		// Query for the missing field
		if tokenOutputs[0].TokenIdentifier != nil {
			tokenCreateEnt, err = db.TokenCreate.Query().
				Where(tokencreate.TokenIdentifier(tokenOutputs[0].TokenIdentifier)).
				Only(ctx)
			if err != nil {
				// An error occured when fetching the spark token create ent.
				return nil, fmt.Errorf("failed to fetch token create ent: %w", err)
			}
			issuerPublicKeyToWrite = tokenCreateEnt.IssuerPublicKey
		} else if len(tokenOutputs[0].TokenPublicKey) != 0 {
			tokenPubKey, err := keys.ParsePublicKey(tokenOutputs[0].TokenPublicKey)
			if err != nil {
				return nil, fmt.Errorf("failed to parse token public key: %w", err)
			}
			tokenCreateEnt, err = db.TokenCreate.Query().
				Where(tokencreate.IssuerPublicKey(tokenPubKey)).
				Only(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to fetch token create ent: %w", err)
			}
			tokenIdentifierToWrite = tokenCreateEnt.TokenIdentifier
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

		if issuerPublicKeyToWrite.IsZero() {
			outputPubKey, err := keys.ParsePublicKey(output.GetTokenPublicKey())
			if err != nil {
				return nil, fmt.Errorf("failed to parse output token public key: %w", err)
			}
			issuerPublicKeyToWrite = outputPubKey
		}
		if len(tokenIdentifierToWrite) == 0 {
			tokenIdentifierToWrite = output.TokenIdentifier
		}

		outputEnts = append(
			outputEnts,
			db.TokenOutput.
				Create().
				SetID(outputUUID).
				SetStatus(st.TokenOutputStatusCreatedStarted).
				SetOwnerPublicKey(output.OwnerPublicKey).
				SetWithdrawBondSats(output.GetWithdrawBondSats()).
				SetWithdrawRelativeBlockLocktime(output.GetWithdrawRelativeBlockLocktime()).
				SetWithdrawRevocationCommitment(output.RevocationCommitment).
				SetTokenPublicKey(issuerPublicKeyToWrite.Serialize()).
				SetTokenIdentifier(tokenIdentifierToWrite).
				SetTokenAmount(output.TokenAmount).
				SetNetwork(network).
				SetCreatedTransactionOutputVout(int32(outputIndex)).
				SetRevocationKeyshareID(revocationUUID).
				SetOutputCreatedTokenTransactionID(tokenTransactionEnt.ID).
				SetNetwork(network).
				SetTokenCreateID(tokenCreateEnt.ID),
		)
	}
	_, err = db.TokenOutput.CreateBulk(outputEnts...).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create token outputs: %w", err)
	}
	return tokenTransactionEnt, nil
}

func prepareSparkInvoiceCreates(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction, tokenTransactionEnt *TokenTransaction) (map[uuid.UUID]struct{}, []*SparkInvoiceCreate, error) {
	invoiceIDs := make(map[uuid.UUID]struct{})
	invoiceCreates := make([]*SparkInvoiceCreate, 0)
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, invoiceAttachment := range tokenTransaction.GetInvoiceAttachments() {
		if invoiceAttachment == nil {
			return nil, nil, fmt.Errorf("invoice attachment is nil")
		}
		parsedInvoice, err := common.ParseSparkInvoice(invoiceAttachment.SparkInvoice)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to decode spark invoice: %w", err)
		}
		invoiceToCreate := db.SparkInvoice.Create().
			SetID(parsedInvoice.Id).
			SetSparkInvoice(invoiceAttachment.SparkInvoice).
			SetReceiverPublicKey(parsedInvoice.ReceiverPublicKey.Serialize()).
			AddTokenTransactionIDs(tokenTransactionEnt.ID)
		if expiry := parsedInvoice.ExpiryTime; expiry != nil {
			invoiceToCreate = invoiceToCreate.SetExpiryTime(expiry.AsTime())
		}
		invoiceCreates = append(
			invoiceCreates,
			invoiceToCreate,
		)
		invoiceIDs[parsedInvoice.Id] = struct{}{}
	}
	return invoiceIDs, invoiceCreates, nil
}

// UpdateSignedTransaction updates the status and ownership signatures of the inputs + outputs
// and the issuer signature (if applicable).
func UpdateSignedTransaction(
	ctx context.Context,
	tokenTransactionEnt *TokenTransaction,
	operatorSpecificOwnershipSignatures [][]byte,
	operatorSignature []byte,
) error {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	// Update the token transaction with the operator signature and new status
	_, err = db.TokenTransaction.UpdateOne(tokenTransactionEnt).
		SetOperatorSignature(operatorSignature).
		SetStatus(st.TokenTransactionStatusSigned).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update token transaction with operator signature and status: %w", err)
	}

	newInputStatus := st.TokenOutputStatusSpentSigned
	newOutputLeafStatus := st.TokenOutputStatusCreatedSigned
	if tokenTransactionEnt.Edges.Mint != nil {
		// If this is a mint, update status straight to finalized because a follow up Finalize() call
		// is not necessary for mint.
		newInputStatus = st.TokenOutputStatusSpentFinalized
		newOutputLeafStatus = st.TokenOutputStatusCreatedFinalized
		if len(operatorSpecificOwnershipSignatures) != 1 {
			return fmt.Errorf(
				"expected 1 ownership signature for mint, got %d",
				len(operatorSpecificOwnershipSignatures),
			)
		}

		_, err := db.TokenMint.UpdateOne(tokenTransactionEnt.Edges.Mint).
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
			_, err := db.TokenOutput.UpdateOne(outputToSpendEnt).
				SetStatus(newInputStatus).
				SetSpentOperatorSpecificOwnershipSignature(operatorSpecificOwnershipSignatures[inputIndex]).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to update spent output to signed: %w", err)
			}
		}
	}

	// Update outputs.
	if numOutputs := len(tokenTransactionEnt.Edges.CreatedOutput); numOutputs > 0 {
		outputIDs := make([]uuid.UUID, numOutputs)
		for i, output := range tokenTransactionEnt.Edges.CreatedOutput {
			outputIDs[i] = output.ID
		}
		_, err = db.TokenOutput.Update().
			Where(tokenoutput.IDIn(outputIDs...)).
			SetStatus(newOutputLeafStatus).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to bulk update output status to signed: %w", err)
		}
	}

	return nil
}

// UpdateSignedTransferTransactionWithoutOperatorSpecificOwnershipSignatures is used to update the status of a token transaction to signed
// when the operator specific ownership signatures are not available. This is used when the operator does not successfully commit
// after signing, but we have proof that the operator signed the transaction.
func UpdateSignedTransferTransactionWithoutOperatorSpecificOwnershipSignatures(
	ctx context.Context,
	tokenTransactionEnt *TokenTransaction,
	operatorSignature []byte,
) error {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	// Update the token transaction with the operator signature and new status
	_, err = db.TokenTransaction.UpdateOne(tokenTransactionEnt).
		SetOperatorSignature(operatorSignature).
		SetStatus(st.TokenTransactionStatusSigned).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update token transaction with operator signature and status: %w", err)
	}

	// Update inputs.
	if tokenTransactionEnt.Edges.SpentOutput != nil {
		outputIDs := make([]uuid.UUID, len(tokenTransactionEnt.Edges.SpentOutput))
		for i, output := range tokenTransactionEnt.Edges.SpentOutput {
			outputIDs[i] = output.ID
		}
		_, err = db.TokenOutput.Update().
			Where(tokenoutput.IDIn(outputIDs...)).
			SetStatus(st.TokenOutputStatusSpentSigned).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to bulk update spent output status to signed: %w", err)
		}
	}

	// Update outputs.
	if numOutputs := len(tokenTransactionEnt.Edges.CreatedOutput); numOutputs > 0 {
		outputIDs := make([]uuid.UUID, numOutputs)
		for i, output := range tokenTransactionEnt.Edges.CreatedOutput {
			outputIDs[i] = output.ID
		}
		_, err = db.TokenOutput.Update().
			Where(tokenoutput.IDIn(outputIDs...)).
			SetStatus(st.TokenOutputStatusCreatedSigned).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to bulk update output status to signed: %w", err)
		}
	}

	return nil
}

// UpdateFinalizedTransaction updates the status and ownership signatures of the finalized input + output outputs.
func UpdateFinalizedTransaction(
	ctx context.Context,
	tokenTransactionEnt *TokenTransaction,
	revocationSecrets []*pb.RevocationSecretWithIndex,
) error {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	// Update the token transaction with the operator signature and new status
	_, err = db.TokenTransaction.UpdateOne(tokenTransactionEnt).
		SetStatus(st.TokenTransactionStatusFinalized).
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
		_, err := db.TokenOutput.UpdateOne(outputToSpendEnt).
			SetStatus(st.TokenOutputStatusSpentFinalized).
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
	_, err = db.TokenOutput.Update().
		Where(tokenoutput.IDIn(outputIDs...)).
		SetStatus(st.TokenOutputStatusCreatedFinalized).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to bulk update output status to finalized: %w", err)
	}
	return nil
}

type RecoveredRevocationSecret struct {
	OutputIndex      uint32
	RevocationSecret keys.Private
}

func FinalizeCoordinatedTokenTransactionWithRevocationKeys(
	ctx context.Context,
	tokenTransactionEnt *TokenTransaction,
	revocationSecrets []*RecoveredRevocationSecret,
) error {
	spentOutputs := tokenTransactionEnt.Edges.SpentOutput
	txHash := tokenTransactionEnt.FinalizedTokenTransactionHash
	if len(spentOutputs) == 0 {
		return fmt.Errorf("no spent outputs found for txHash %x. cannot finalize", txHash)
	}
	if len(revocationSecrets) != len(spentOutputs) {
		return fmt.Errorf(
			"number of revocation keys (%d) does not match number of spent outputs (%d) for txHash %x",
			len(revocationSecrets),
			len(spentOutputs),
			txHash,
		)
	}

	revocationSecretMap := make(map[uint32]keys.Private, len(revocationSecrets))
	for _, revocationSecret := range revocationSecrets {
		revocationSecretMap[revocationSecret.OutputIndex] = revocationSecret.RevocationSecret
	}

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	for _, outputToSpendEnt := range spentOutputs {
		if outputToSpendEnt.SpentTransactionInputVout < 0 {
			return fmt.Errorf("spent transaction input vout is negative: %d for txHash %x", outputToSpendEnt.SpentTransactionInputVout, txHash)
		}
		inputIndex := uint32(outputToSpendEnt.SpentTransactionInputVout)
		revocationSecret, ok := revocationSecretMap[inputIndex]
		if !ok {
			return fmt.Errorf("no revocation secret found for input index %d for txHash %x", inputIndex, txHash)
		}
		if revocationSecret.IsZero() {
			return fmt.Errorf("revocation secret is zero for input index %d for txHash %x", inputIndex, txHash)
		}

		_, err := db.TokenOutput.UpdateOne(outputToSpendEnt).
			SetStatus(st.TokenOutputStatusSpentFinalized).
			SetSpentRevocationSecret(revocationSecret.Serialize()).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to update spent output for txHash %x: %w", txHash, err)
		}
	}

	// Update outputs.
	outputIDs := make([]uuid.UUID, len(tokenTransactionEnt.Edges.CreatedOutput))
	for i, output := range tokenTransactionEnt.Edges.CreatedOutput {
		outputIDs[i] = output.ID
	}
	_, err = db.TokenOutput.Update().
		Where(tokenoutput.IDIn(outputIDs...)).
		SetStatus(st.TokenOutputStatusCreatedFinalized).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to bulk update output status to finalized for txHash %x: %w", txHash, err)
	}

	// Update the token transaction status to Finalized.
	_, err = db.TokenTransaction.UpdateOne(tokenTransactionEnt).
		SetStatus(st.TokenTransactionStatusFinalized).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update token transaction with finalized status for txHash %x: %w", txHash, err)
	}
	if err := db.Commit(); err != nil {
		return fmt.Errorf("failed to commit and replace transaction after finalizing token transaction: %w", err)
	}

	return nil
}

// UpdateCancelledTransaction updates the status and ownership signatures in the inputs + outputs in response to a cancelled transaction.
func UpdateCancelledTransaction(ctx context.Context, tokenTransactionEnt *TokenTransaction) error {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	// Collect all output IDs that need to be locked
	allOutputIDs := make([]uuid.UUID, 0, len(tokenTransactionEnt.Edges.SpentOutput)+len(tokenTransactionEnt.Edges.CreatedOutput))

	// Add spent output IDs
	spentLeaves := tokenTransactionEnt.Edges.SpentOutput
	for _, output := range spentLeaves {
		allOutputIDs = append(allOutputIDs, output.ID)
		// Verify output is in the expected state
		if output.Status != st.TokenOutputStatusSpentStarted && output.Status != st.TokenOutputStatusSpentSigned {
			return fmt.Errorf("spent output ID %s has status %s, expected %s or %s",
				output.ID.String(),
				output.Status,
				st.TokenOutputStatusSpentStarted,
				st.TokenOutputStatusSpentSigned)
		}
	}

	// Add created output IDs
	for _, output := range tokenTransactionEnt.Edges.CreatedOutput {
		allOutputIDs = append(allOutputIDs, output.ID)
		// Verify output is in the expected state
		if output.Status != st.TokenOutputStatusCreatedStarted && output.Status != st.TokenOutputStatusCreatedSigned {
			return fmt.Errorf("created output ID %s has status %s, expected %s or %s",
				output.ID.String(),
				output.Status,
				st.TokenOutputStatusCreatedStarted,
				st.TokenOutputStatusCreatedSigned)
		}
	}

	// Lock all outputs in a single query (token transaction was locked upstream).
	_, err = db.TokenOutput.Query().
		Where(tokenoutput.IDIn(allOutputIDs...)).
		ForUpdate().
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to lock all token outputs for update: %w", err)
	}

	var newStatus st.TokenTransactionStatus
	switch tokenTransactionEnt.Status {
	case st.TokenTransactionStatusStarted:
		newStatus = st.TokenTransactionStatusStartedCancelled
	case st.TokenTransactionStatusSigned:
		newStatus = st.TokenTransactionStatusSignedCancelled
	default:
		return fmt.Errorf("token transaction status %s is not valid for cancelling", tokenTransactionEnt.Status)
	}
	_, err = db.TokenTransaction.UpdateOne(tokenTransactionEnt).
		SetStatus(newStatus).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update token transaction with new status: %w", err)
	}

	// Update spent outputs to re-enable spending.
	_, err = db.TokenOutput.Update().
		Where(tokenoutput.StatusIn(st.TokenOutputStatusSpentStarted, st.TokenOutputStatusSpentSigned)).
		Where(tokenoutput.IDIn(allOutputIDs...)).
		SetStatus(st.TokenOutputStatusCreatedFinalized).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to bulk update spent output status to created finalized: %w", err)
	}
	// Update created outputs to invalidate them
	_, err = db.TokenOutput.Update().
		Where(tokenoutput.StatusEQ(st.TokenOutputStatusCreatedStarted)).
		Where(tokenoutput.IDIn(allOutputIDs...)).
		SetStatus(st.TokenOutputStatusCreatedStartedCancelled).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to bulk update created output status to started cancelled: %w", err)
	}
	_, err = db.TokenOutput.Update().
		Where(tokenoutput.StatusEQ(st.TokenOutputStatusCreatedSigned)).
		Where(tokenoutput.IDIn(allOutputIDs...)).
		SetStatus(st.TokenOutputStatusCreatedSignedCancelled).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to bulk update created output status to signed cancelled: %w", err)
	}

	return nil
}

func FetchPartialTokenTransactionData(ctx context.Context, partialTokenTransactionHash []byte) (*TokenTransaction, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	tokenTransaction, err := db.TokenTransaction.Query().
		Where(tokentransaction.PartialTokenTransactionHash(partialTokenTransactionHash)).
		WithCreatedOutput().
		WithSpentOutput(func(q *TokenOutputQuery) {
			// Needed to enable marshalling of the token transaction proto.
			q.WithOutputCreatedTokenTransaction()
		}).
		WithMint().
		WithCreate().
		Only(ctx)
	if err != nil {
		return nil, err
	}
	return tokenTransaction, nil
}

// FetchAndLockTokenTransactionData refetches the transaction with all its relations.
func FetchAndLockTokenTransactionData(ctx context.Context, finalTokenTransaction *tokenpb.TokenTransaction) (*TokenTransaction, error) {
	calculatedFinalTokenTransactionHash, err := utils.HashTokenTransaction(finalTokenTransaction, false)
	if err != nil {
		return nil, err
	}

	tokenTransaction, err := FetchAndLockTokenTransactionDataByHash(ctx, calculatedFinalTokenTransactionHash)
	if err != nil {
		return nil, err
	}

	// Sanity check that inputs and outputs matching the expected length were found.
	// Also ensure the database entity type matches the protobuf type.
	sparkTx, err := protoconverter.SparkTokenTransactionFromTokenProto(finalTokenTransaction)
	if err != nil {
		return nil, fmt.Errorf("failed to convert token transaction: %w", err)
	}

	txType, err := utils.InferTokenTransactionTypeSparkProtos(sparkTx)
	if err != nil {
		return nil, fmt.Errorf("invalid token transaction inputs: %w", err)
	}

	switch txType {
	case utils.TokenTransactionTypeCreate:
		if tokenTransaction.Edges.Create == nil {
			return nil, fmt.Errorf("database has no create transaction but protobuf has create input - transaction type mismatch")
		}
	case utils.TokenTransactionTypeMint:
		if tokenTransaction.Edges.Mint == nil {
			return nil, fmt.Errorf("database has no mint transaction but protobuf has mint input - transaction type mismatch")
		}
	case utils.TokenTransactionTypeTransfer:
		if tokenTransaction.Edges.Create != nil || tokenTransaction.Edges.Mint != nil {
			return nil, fmt.Errorf("database has create/mint transaction but protobuf has transfer input - transaction type mismatch")
		}
		transferInput := finalTokenTransaction.GetTransferInput()
		if len(transferInput.GetOutputsToSpend()) != len(tokenTransaction.Edges.SpentOutput) {
			return nil, fmt.Errorf(
				"number of inputs in proto (%d) does not match number of spent outputs started with this transaction in the database (%d)",
				len(transferInput.GetOutputsToSpend()),
				len(tokenTransaction.Edges.SpentOutput),
			)
		}
	default:
		return nil, fmt.Errorf("token transaction type unknown")
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

func FetchAndLockTokenTransactionDataByHash(ctx context.Context, tokenTransactionHash []byte) (*TokenTransaction, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	tokenTransaction, err := db.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHash(tokenTransactionHash)).
		WithCreatedOutput().
		WithSpentOutput(func(q *TokenOutputQuery) {
			// Needed to enable computation of the progress of a transaction commit.
			q.WithRevocationKeyshare().
				WithTokenPartialRevocationSecretShares().
				// Needed to enable marshalling of the token transaction proto.
				WithOutputCreatedTokenTransaction()
		}).
		WithPeerSignatures().
		WithMint().
		WithCreate().
		WithSparkInvoice().
		ForUpdate().
		Only(ctx)
	if err != nil {
		return nil, err
	}

	return tokenTransaction, nil
}

// MarshalProto converts a TokenTransaction to a token protobuf TokenTransaction.
// This assumes the transaction already has all its relationships loaded.
func (t *TokenTransaction) MarshalProto(ctx context.Context, config *so.Config) (*tokenpb.TokenTransaction, error) {
	logger := logging.GetLoggerFromContext(ctx)

	operatorPublicKeys := make([][]byte, 0, len(config.SigningOperatorMap))
	for _, operator := range config.SigningOperatorMap {
		operatorPublicKeys = append(operatorPublicKeys, operator.IdentityPublicKey.Serialize())
	}
	invoiceAttachments := make([]*tokenpb.InvoiceAttachment, 0, len(t.Edges.SparkInvoice))
	for _, invoice := range t.Edges.SparkInvoice {
		invoiceAttachments = append(invoiceAttachments, &tokenpb.InvoiceAttachment{
			SparkInvoice: invoice.SparkInvoice,
		})
	}

	// V3 deterministic ordering: sort operator keys and invoices
	if uint32(t.Version) == 3 {
		// Sort operator keys bytewise ascending
		slices.SortFunc(operatorPublicKeys, func(a, b []byte) int { return bytes.Compare(a, b) })

		// Sort invoices lexicographically by the invoice attachment string
		slices.SortFunc(invoiceAttachments, func(a, b *tokenpb.InvoiceAttachment) int {
			return cmp.Compare(a.GetSparkInvoice(), b.GetSparkInvoice())
		})
	}

	tokenTransaction := &tokenpb.TokenTransaction{
		Version:      uint32(t.Version),
		TokenOutputs: make([]*tokenpb.TokenOutput, len(t.Edges.CreatedOutput)),
		// Get all operator identity public keys from the config
		SparkOperatorIdentityPublicKeys: operatorPublicKeys,
		ExpiryTime:                      timestamppb.New(t.ExpiryTime),
		InvoiceAttachments:              invoiceAttachments,
	}
	if !t.ClientCreatedTimestamp.IsZero() {
		tokenTransaction.ClientCreatedTimestamp = timestamppb.New(t.ClientCreatedTimestamp)
	}

	network, err := t.GetNetworkFromEdges()
	if err != nil {
		return nil, fmt.Errorf("failed to get network from edges: %w", err)
	}
	networkProto, err := network.MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal network from schema type: %w", err)
	}
	tokenTransaction.Network = networkProto

	// Sort outputs to match the original token transaction using CreatedTransactionOutputVout
	sortedCreatedOutputs := slices.SortedFunc(slices.Values(t.Edges.CreatedOutput), func(a, b *TokenOutput) int {
		return cmp.Compare(a.CreatedTransactionOutputVout, b.CreatedTransactionOutputVout)
	})

	for i, output := range sortedCreatedOutputs {
		tokenTransaction.TokenOutputs[i] = &tokenpb.TokenOutput{
			Id:                            proto.String(output.ID.String()),
			OwnerPublicKey:                output.OwnerPublicKey,
			RevocationCommitment:          output.WithdrawRevocationCommitment,
			WithdrawBondSats:              &output.WithdrawBondSats,
			WithdrawRelativeBlockLocktime: &output.WithdrawRelativeBlockLocktime,
			TokenPublicKey:                output.TokenPublicKey,
			TokenIdentifier:               output.TokenIdentifier,
			TokenAmount:                   output.TokenAmount,
		}
		if t.Version == 0 {
			tokenTransaction.TokenOutputs[i].TokenIdentifier = nil
		} else {
			tokenTransaction.TokenOutputs[i].TokenPublicKey = nil
		}
	}

	if t.Edges.Create != nil {
		tokenTransaction.TokenInputs = &tokenpb.TokenTransaction_CreateInput{
			CreateInput: &tokenpb.TokenCreateInput{
				IssuerPublicKey: t.Edges.Create.IssuerPublicKey.Serialize(),
				TokenName:       t.Edges.Create.TokenName,
				TokenTicker:     t.Edges.Create.TokenTicker,
				// Protos do not have support for uint8, so convert to uint32.
				Decimals:                uint32(t.Edges.Create.Decimals),
				MaxSupply:               t.Edges.Create.MaxSupply,
				IsFreezable:             t.Edges.Create.IsFreezable,
				CreationEntityPublicKey: t.Edges.Create.CreationEntityPublicKey.Serialize(),
			},
		}
	} else if t.Edges.Mint != nil {
		tokenTransaction.TokenInputs = &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: t.Edges.Mint.IssuerPublicKey,
				TokenIdentifier: t.Edges.Mint.TokenIdentifier,
			},
		}
	} else if len(t.Edges.SpentOutput) > 0 {
		// This is a transfer transaction
		transferInput := &tokenpb.TokenTransferInput{
			OutputsToSpend: make([]*tokenpb.TokenOutputToSpend, len(t.Edges.SpentOutput)),
		}

		// Sort outputs to match the original token transaction using SpentTransactionInputVout
		sortedSpentOutputs := slices.SortedFunc(slices.Values(t.Edges.SpentOutput), func(a, b *TokenOutput) int {
			return cmp.Compare(a.SpentTransactionInputVout, b.SpentTransactionInputVout)
		})

		for i, output := range sortedSpentOutputs {
			// Since we assume all relationships are loaded, we can directly access the created transaction.
			if output.Edges.OutputCreatedTokenTransaction == nil {
				return nil, fmt.Errorf("output spent transaction edge not loaded for output %s", output.ID)
			}

			transferInput.OutputsToSpend[i] = &tokenpb.TokenOutputToSpend{
				PrevTokenTransactionHash: output.Edges.OutputCreatedTokenTransaction.FinalizedTokenTransactionHash,
				PrevTokenTransactionVout: uint32(output.CreatedTransactionOutputVout),
			}
		}

		tokenTransaction.TokenInputs = &tokenpb.TokenTransaction_TransferInput{
			TransferInput: transferInput,
		}

		// Because we checked for create and mint inputs below, if it doesn't map to inputs it is a special case where a transfer
		// may not have successfully completed and has since had its inputs remappted.
	} else if t.Status == st.TokenTransactionStatusStarted || t.Status == st.TokenTransactionStatusStartedCancelled ||
		t.Status == st.TokenTransactionStatusSignedCancelled {
		logger.Sugar().Warnf(
			"Started transaction %s with hash %x does not map to input TTXOs. This is likely due to those inputs being spent and remapped to a subsequent transaction.",
			t.ID,
			t.FinalizedTokenTransactionHash,
		)
	} else if t.Status == st.TokenTransactionStatusSigned && t.Version != 0 && time.Now().After(t.ExpiryTime) {
		// Preemption logic in V1 Transactions allows the inputs on certain signed transactions to be remapped after expiry.
		logger.Sugar().Warnf(
			"Signed transaction %s with hash %x does not map to input TTXOs. This is likely due to this transaction being pre-empted and those inputs being spent and remapped to a subsequent transaction.",
			t.ID,
			t.FinalizedTokenTransactionHash,
		)
	} else {
		return nil, fmt.Errorf("Signed/Finalized transaction unexpectedly does not map to input TTXOs and cannot be marshalled: %s", t.ID)
	}
	return tokenTransaction, nil
}

func (t *TokenTransaction) GetNetworkFromEdges() (st.Network, error) {
	txType, err := t.InferTokenTransactionTypeEnt()
	if err != nil {
		return st.NetworkUnspecified, fmt.Errorf("invalid token transaction inputs: %w", err)
	}

	switch txType {
	case utils.TokenTransactionTypeCreate:
		return t.Edges.Create.Network, nil
	case utils.TokenTransactionTypeMint, utils.TokenTransactionTypeTransfer:
		if len(t.Edges.CreatedOutput) == 0 {
			return st.NetworkUnspecified, fmt.Errorf("no outputs were found when reconstructing token transaction with ID: %s", t.ID)
		}
		// All token transaction outputs must have the same network (confirmed in validation when signing
		// the transaction, so its safe to use the first output).
		return t.Edges.CreatedOutput[0].Network, nil
	default:
		return st.NetworkUnspecified, fmt.Errorf("unknown token transaction type: %s", txType)
	}
}

// InferTokenTransactionTypeEnt determines the transaction type based on the Ent entity's edges.
// This is more efficient than converting to proto and then inferring the type.
func (t *TokenTransaction) InferTokenTransactionTypeEnt() (utils.TokenTransactionType, error) {
	if t.Edges.Create != nil {
		return utils.TokenTransactionTypeCreate, nil
	}
	if t.Edges.Mint != nil {
		return utils.TokenTransactionTypeMint, nil
	}
	// If no create or mint, assume its a transfer.
	return utils.TokenTransactionTypeTransfer, nil
}

// ValidateNotExpired checks if a token transaction has expired and returns an error if it has.
func (t *TokenTransaction) ValidateNotExpired(defaultV0TransactionExpiryDuration time.Duration) error {
	now := time.Now().UTC()
	if !t.ExpiryTime.IsZero() {
		if now.After(t.ExpiryTime.UTC()) {
			return fmt.Errorf("signing failed because token transaction %s has expired at %s, current time: %s",
				t.ID, t.ExpiryTime.UTC().Format(time.RFC3339), now.Format(time.RFC3339))
		}
	} else if t.Version == 0 {
		v0ComputedExpirationTime := t.CreateTime.Add(defaultV0TransactionExpiryDuration)
		if now.After(v0ComputedExpirationTime) {
			return fmt.Errorf("signing failed because v0 token transaction %s has computed expiration time %s, current time: %s",
				t.ID, v0ComputedExpirationTime.Format(time.RFC3339), now.Format(time.RFC3339))
		}
	}
	return nil
}

// IsExpired checks if a token transaction has expired at the given time.
func (t *TokenTransaction) IsExpired(requestTime time.Time, defaultV0TransactionExpiryDuration time.Duration) bool {
	if t.Status != st.TokenTransactionStatusStarted && t.Status != st.TokenTransactionStatusSigned {
		return false
	}

	if !t.ExpiryTime.IsZero() {
		return requestTime.After(t.ExpiryTime)
	} else if t.Version == 0 {
		v0ComputedExpirationTime := t.CreateTime.Add(defaultV0TransactionExpiryDuration)
		return requestTime.After(v0ComputedExpirationTime)
	}
	return false
}
