package ent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"google.golang.org/protobuf/proto"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/uint128"
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
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to get db from context: %w", err))
	}

	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		return nil, err
	}
	finalTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, false)
	if err != nil {
		return nil, err
	}

	var network btcnetwork.Network
	if err := network.UnmarshalProto(tokenTransaction.Network); err != nil {
		return nil, sparkerrors.InternalTypeConversionError(fmt.Errorf("failed to unmarshal network: %w", err))
	}

	var tokenTransactionEnt *TokenTransaction
	tokenTransactionType, err := utils.InferTokenTransactionType(tokenTransaction)
	if err != nil {
		return nil, sparkerrors.InternalTypeConversionError(fmt.Errorf("failed to infer token transaction type: %w", err))
	}

	switch tokenTransactionType {
	case utils.TokenTransactionTypeCreate:
		createInput := tokenTransaction.GetCreateInput()
		tokenMetadata, err := common.NewTokenMetadataFromCreateInput(createInput, tokenTransaction.Network)
		if err != nil {
			return nil, sparkerrors.InternalTypeConversionError(fmt.Errorf("failed to create token metadata: %w", err))
		}
		computedTokenIdentifier, err := tokenMetadata.ComputeTokenIdentifier()
		if err != nil {
			return nil, sparkerrors.InternalTypeConversionError(fmt.Errorf("failed to compute token identifier: %w", err))
		}

		issuerPubKey, err := keys.ParsePublicKey(createInput.GetIssuerPublicKey())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse issuer public key: %w", err))
		}
		creationEntityPubKey, err := keys.ParsePublicKey(createInput.GetCreationEntityPublicKey())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse creation entity public key: %w", err))
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
			return nil, sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to create token create ent, likely due to attempting to restart a create transaction with a different operator: %w", err))
		}
		txBuilder := db.TokenTransaction.Create().
			SetPartialTokenTransactionHash(partialTokenTransactionHash).
			SetFinalizedTokenTransactionHash(finalTokenTransactionHash).
			SetStatus(st.TokenTransactionStatusStarted).
			SetCoordinatorPublicKey(coordinatorPublicKey).
			SetVersion(st.TokenTransactionVersion(tokenTransaction.Version)).
			SetCreateID(tokenCreateEnt.ID)
		txBuilder, err = setTokenTransactionTimingFields(txBuilder, tokenTransaction)
		if err != nil {
			return nil, err
		}
		tokenTransactionEnt, err = txBuilder.Save(ctx)
		if err != nil {
			return nil, sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to create create token transaction: %w", err))
		}
	case utils.TokenTransactionTypeMint:
		issuerPubKey, err := keys.ParsePublicKey(tokenTransaction.GetMintInput().GetIssuerPublicKey())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse issuer public key: %w", err))
		}
		tokenMintEnt, err := db.TokenMint.Create().
			SetIssuerPublicKey(issuerPubKey).
			SetIssuerSignature(signaturesWithIndex[0].Signature).
			// TODO CNT-376: remove timestamp field from MintInput and use TokenTransaction.ClientCreatedTimestamp instead
			SetWalletProvidedTimestamp(uint64(tokenTransaction.ClientCreatedTimestamp.AsTime().UnixMilli())).
			SetTokenIdentifier(tokenTransaction.GetMintInput().GetTokenIdentifier()).
			Save(ctx)
		if err != nil {
			return nil, sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to create token mint ent, likely due to attempting to restart a mint transaction with a different operator: %w", err))
		}
		txMintBuilder := db.TokenTransaction.Create().
			SetPartialTokenTransactionHash(partialTokenTransactionHash).
			SetFinalizedTokenTransactionHash(finalTokenTransactionHash).
			SetStatus(st.TokenTransactionStatusStarted).
			SetCoordinatorPublicKey(coordinatorPublicKey).
			SetVersion(st.TokenTransactionVersion(tokenTransaction.Version)).
			SetMintID(tokenMintEnt.ID)
		txMintBuilder, err = setTokenTransactionTimingFields(txMintBuilder, tokenTransaction)
		if err != nil {
			return nil, err
		}
		tokenTransactionEnt, err = txMintBuilder.Save(ctx)
		if err != nil {
			return nil, sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to create mint token transaction: %w", err))
		}
	case utils.TokenTransactionTypeTransfer:
		if len(signaturesWithIndex) != len(orderedOutputToSpendEnts) {
			return nil, sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf(
				"number of signatures %d doesn't match number of outputs to spend %d",
				len(signaturesWithIndex),
				len(orderedOutputToSpendEnts),
			))
		}
		txTransferBuilder := db.TokenTransaction.Create().
			SetPartialTokenTransactionHash(partialTokenTransactionHash).
			SetFinalizedTokenTransactionHash(finalTokenTransactionHash).
			SetStatus(st.TokenTransactionStatusStarted).
			SetCoordinatorPublicKey(coordinatorPublicKey).
			SetVersion(st.TokenTransactionVersion(tokenTransaction.Version))
		txTransferBuilder, err = setTokenTransactionTimingFields(txTransferBuilder, tokenTransaction)
		if err != nil {
			return nil, err
		}
		tokenTransactionEnt, err = txTransferBuilder.Save(ctx)
		if err != nil {
			return nil, sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to create transfer token transaction: %w", err))
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
				return nil, sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to update output to spend: %w", err))
			}
		}
	case utils.TokenTransactionTypeUnknown:
	default:
		return nil, sparkerrors.InternalObjectMalformedField(fmt.Errorf("token transaction type unknown"))
	}
	if tokenTransaction.Version >= 2 && tokenTransaction.GetInvoiceAttachments() != nil {
		sparkInvoiceIDs, sparkInvoicesToCreate, err := prepareSparkInvoiceCreates(ctx, tokenTransaction, tokenTransactionEnt)
		if err != nil {
			return nil, sparkerrors.InternalTypeConversionError(fmt.Errorf("failed to prepare spark invoices: %w", err))
		}
		if len(sparkInvoicesToCreate) > 0 {
			err = db.SparkInvoice.CreateBulk(sparkInvoicesToCreate...).
				OnConflictColumns(sparkinvoice.FieldID).
				DoNothing().
				Exec(ctx)
			if err != nil {
				return nil, sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to create spark invoices: %w", err))
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
				return nil, sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to attach token transaction edge: %w", err))
			}
		}
	}

	// Clients provide one of tokenIdentifier or tokenPublicKey to the server to make transactions.
	// Older clients provide only tokenPublicKey. Newer clients provide only tokenIdentifier.
	//
	// For multi-token transactions, batch fetch all TokenCreate entities to minimize database roundtrips
	// Collect unique token_identifiers and token_public_keys
	tokenIdentifiersToFetch := make([][]byte, 0)
	tokenPublicKeysToFetch := make([]keys.Public, 0)
	tokenIdentifierMap := make(map[string]struct{})
	tokenPublicKeyMap := make(map[string]struct{})

	for _, output := range tokenTransaction.TokenOutputs {
		if output.TokenIdentifier != nil {
			key := string(output.TokenIdentifier)
			if _, exists := tokenIdentifierMap[key]; !exists {
				tokenIdentifiersToFetch = append(tokenIdentifiersToFetch, output.TokenIdentifier)
				tokenIdentifierMap[key] = struct{}{}
			}
		} else if len(output.TokenPublicKey) != 0 {
			tokenPubKey, err := keys.ParsePublicKey(output.GetTokenPublicKey())
			if err != nil {
				return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse token public key: %w", err))
			}
			key := string(tokenPubKey.Serialize())
			if _, exists := tokenPublicKeyMap[key]; !exists {
				tokenPublicKeysToFetch = append(tokenPublicKeysToFetch, tokenPubKey)
				tokenPublicKeyMap[key] = struct{}{}
			}
		}
	}

	// Batch fetch TokenCreate entities
	var tokenCreatesByIdentifier map[string]*TokenCreate
	var tokenCreatesByIssuerPubKey map[string]*TokenCreate

	if len(tokenIdentifiersToFetch) > 0 {
		tokenCreates, err := db.TokenCreate.Query().
			Where(tokencreate.TokenIdentifierIn(tokenIdentifiersToFetch...)).
			All(ctx)
		if err != nil {
			return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to batch fetch token creates by identifier: %w", err))
		}
		tokenCreatesByIdentifier = make(map[string]*TokenCreate, len(tokenCreates))
		for _, tc := range tokenCreates {
			tokenCreatesByIdentifier[string(tc.TokenIdentifier)] = tc
		}
	}

	if len(tokenPublicKeysToFetch) > 0 {
		tokenCreates, err := db.TokenCreate.Query().
			Where(tokencreate.IssuerPublicKeyIn(tokenPublicKeysToFetch...)).
			All(ctx)
		if err != nil {
			return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to batch fetch token creates by issuer public key: %w", err))
		}
		tokenCreatesByIssuerPubKey = make(map[string]*TokenCreate, len(tokenCreates))
		for _, tc := range tokenCreates {
			tokenCreatesByIssuerPubKey[string(tc.IssuerPublicKey.Serialize())] = tc
		}
	}

	// Now create outputs using the fetched TokenCreate entities
	outputEnts := make([]*TokenOutputCreate, 0, len(tokenTransaction.TokenOutputs))
	for outputIndex, output := range tokenTransaction.TokenOutputs {
		revocationUUID, err := uuid.Parse(orderedOutputToCreateRevocationKeyshareIDs[outputIndex])
		if err != nil {
			return nil, err
		}
		outputUUID, err := uuid.Parse(output.GetId())
		if err != nil {
			return nil, err
		}

		// Look up the TokenCreate entity for this specific output
		var tokenCreateEnt *TokenCreate
		var tokenIdentifierToWrite []byte
		var issuerPublicKeyToWrite keys.Public

		if output.TokenIdentifier != nil {
			var found bool
			tokenCreateEnt, found = tokenCreatesByIdentifier[string(output.TokenIdentifier)]
			if !found {
				return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("token create entity not found for token identifier %x at output %d", output.TokenIdentifier, outputIndex))
			}
			issuerPublicKeyToWrite = tokenCreateEnt.IssuerPublicKey
			tokenIdentifierToWrite = output.TokenIdentifier
		} else if len(output.TokenPublicKey) != 0 {
			tokenPubKey, err := keys.ParsePublicKey(output.GetTokenPublicKey())
			if err != nil {
				return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse token public key for output %d: %w", outputIndex, err))
			}
			var found bool
			tokenCreateEnt, found = tokenCreatesByIssuerPubKey[string(tokenPubKey.Serialize())]
			if !found {
				return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("token create entity not found for issuer public key %x at output %d", output.TokenPublicKey, outputIndex))
			}
			tokenIdentifierToWrite = tokenCreateEnt.TokenIdentifier
			issuerPublicKeyToWrite = tokenPubKey
		} else {
			return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("output %d must have either token_identifier or token_public_key", outputIndex))
		}

		ownerPubKey, err := keys.ParsePublicKey(output.OwnerPublicKey)
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse output token owner public key: %w", err))
		}

		tokenAmount, err := uint128.FromBytes(output.TokenAmount)
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("token amount must be 16 bytes: %w", err))
		}

		outputEnts = append(
			outputEnts,
			db.TokenOutput.
				Create().
				SetID(outputUUID).
				SetStatus(st.TokenOutputStatusCreatedStarted).
				SetOwnerPublicKey(ownerPubKey).
				SetWithdrawBondSats(output.GetWithdrawBondSats()).
				SetWithdrawRelativeBlockLocktime(output.GetWithdrawRelativeBlockLocktime()).
				SetWithdrawRevocationCommitment(output.RevocationCommitment).
				SetTokenPublicKey(issuerPublicKeyToWrite).
				SetTokenIdentifier(tokenIdentifierToWrite).
				SetTokenAmount(output.TokenAmount).
				SetAmount(tokenAmount).
				SetNetwork(network).
				SetCreatedTransactionOutputVout(int32(outputIndex)).
				SetRevocationKeyshareID(revocationUUID).
				SetOutputCreatedTokenTransactionID(tokenTransactionEnt.ID).
				SetCreatedTransactionFinalizedHash(tokenTransactionEnt.FinalizedTokenTransactionHash).
				SetNetwork(network).
				SetTokenCreateID(tokenCreateEnt.ID),
		)
	}
	_, err = db.TokenOutput.CreateBulk(outputEnts...).Save(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to create token outputs: %w", err))
	}
	return tokenTransactionEnt, nil
}

func prepareSparkInvoiceCreates(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction, tokenTransactionEnt *TokenTransaction) (map[uuid.UUID]struct{}, []*SparkInvoiceCreate, error) {
	invoiceIDs := make(map[uuid.UUID]struct{})
	var invoiceCreates []*SparkInvoiceCreate
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to get db from context: %w", err))
	}
	for _, invoiceAttachment := range tokenTransaction.GetInvoiceAttachments() {
		if invoiceAttachment == nil {
			return nil, nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("invoice attachment is nil"))
		}
		parsedInvoice, err := common.ParseSparkInvoice(invoiceAttachment.SparkInvoice)
		if err != nil {
			return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to decode spark invoice: %w", err))
		}
		invoiceToCreate := db.SparkInvoice.Create().
			SetID(parsedInvoice.Id).
			SetSparkInvoice(invoiceAttachment.SparkInvoice).
			SetReceiverPublicKey(parsedInvoice.ReceiverPublicKey).
			AddTokenTransactionIDs(tokenTransactionEnt.ID)
		if expiry := parsedInvoice.ExpiryTime; expiry != nil {
			invoiceToCreate = invoiceToCreate.SetExpiryTime(expiry.AsTime())
		}
		invoiceCreates = append(invoiceCreates, invoiceToCreate)
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
		return sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to get db from context: %w", err))
	}

	// Update the token transaction with the operator signature and new status
	_, err = db.TokenTransaction.UpdateOne(tokenTransactionEnt).
		SetOperatorSignature(operatorSignature).
		SetStatus(st.TokenTransactionStatusSigned).
		Save(ctx)
	if err != nil {
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to update token transaction with operator signature and status: %w", err))
	}

	txType := tokenTransactionEnt.InferTokenTransactionTypeEnt()

	newInputStatus := st.TokenOutputStatusSpentSigned
	newOutputLeafStatus := st.TokenOutputStatusCreatedSigned
	if txType == utils.TokenTransactionTypeMint {
		// If this is a mint, update status straight to finalized because a follow up Finalize() call
		// is not necessary for mint.
		newInputStatus = st.TokenOutputStatusSpentFinalized
		newOutputLeafStatus = st.TokenOutputStatusCreatedFinalized
		if tokenTransactionEnt.Version < 3 {
			if len(operatorSpecificOwnershipSignatures) != 1 {
				return sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf(
					"expected 1 ownership signature for mint, got %d",
					len(operatorSpecificOwnershipSignatures),
				))
			}
			_, err := db.TokenMint.UpdateOne(tokenTransactionEnt.Edges.Mint).
				SetOperatorSpecificIssuerSignature(operatorSpecificOwnershipSignatures[0]).
				Save(ctx)
			if err != nil {
				return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to update mint with signature: %w", err))
			}
		}
	}

	// Update inputs.
	if txType == utils.TokenTransactionTypeTransfer {
		spentOutputs := tokenTransactionEnt.Edges.SpentOutput
		if len(spentOutputs) == 0 {
			return sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("no spent outputs found for transfer transaction. cannot sign"))
		}
		if tokenTransactionEnt.Version < 3 {
			// Validate that we have the right number of spent outputs.
			if len(operatorSpecificOwnershipSignatures) != len(spentOutputs) {
				return sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf(
					"number of operator specific ownership signatures (%d) does not match number of spent outputs (%d)",
					len(operatorSpecificOwnershipSignatures),
					len(spentOutputs),
				))
			}

			for _, outputToSpendEnt := range tokenTransactionEnt.Edges.SpentOutput {
				inputIndex := outputToSpendEnt.SpentTransactionInputVout
				_, err := db.TokenOutput.UpdateOne(outputToSpendEnt).
					SetStatus(newInputStatus).
					SetSpentOperatorSpecificOwnershipSignature(operatorSpecificOwnershipSignatures[inputIndex]).
					Save(ctx)
				if err != nil {
					return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to update spent output to signed: %w", err))
				}
			}
		} else {
			for _, outputToSpendEnt := range tokenTransactionEnt.Edges.SpentOutput {
				_, err := db.TokenOutput.UpdateOne(outputToSpendEnt).
					SetStatus(newInputStatus).
					Save(ctx)
				if err != nil {
					return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to update spent output to signed: %w", err))
				}
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
			return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to bulk update output status to signed: %w", err))
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
		return sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to get db from context: %w", err))
	}

	// Update the token transaction with the operator signature and new status
	_, err = db.TokenTransaction.UpdateOne(tokenTransactionEnt).
		SetOperatorSignature(operatorSignature).
		SetStatus(st.TokenTransactionStatusSigned).
		Save(ctx)
	if err != nil {
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to update token transaction with operator signature and status: %w", err))
	}

	txType := tokenTransactionEnt.InferTokenTransactionTypeEnt()

	// Update inputs.
	if txType == utils.TokenTransactionTypeTransfer {
		if len(tokenTransactionEnt.Edges.SpentOutput) == 0 {
			return sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("no spent outputs found for transfer transaction. cannot sign"))
		}
		outputIDs := make([]uuid.UUID, len(tokenTransactionEnt.Edges.SpentOutput))
		for i, output := range tokenTransactionEnt.Edges.SpentOutput {
			outputIDs[i] = output.ID
		}
		_, err = db.TokenOutput.Update().
			Where(tokenoutput.IDIn(outputIDs...)).
			SetStatus(st.TokenOutputStatusSpentSigned).
			Save(ctx)
		if err != nil {
			return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to bulk update spent output status to signed: %w", err))
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
			return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to bulk update output status to signed: %w", err))
		}
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
		return sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("no spent outputs found for txHash %x. cannot finalize", txHash))
	}
	if len(revocationSecrets) != len(spentOutputs) {
		return sparkerrors.InternalKeyshareError(fmt.Errorf(
			"number of revocation keys (%d) does not match number of spent outputs (%d) for txHash %x",
			len(revocationSecrets),
			len(spentOutputs),
			txHash,
		))
	}

	revocationSecretMap := make(map[uint32]keys.Private, len(revocationSecrets))
	for _, revocationSecret := range revocationSecrets {
		revocationSecretMap[revocationSecret.OutputIndex] = revocationSecret.RevocationSecret
	}

	tx, err := GetTxFromContext(ctx)
	if err != nil {
		return sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to get tx from context: %w", err))
	}
	db := tx.Client()

	for _, outputToSpendEnt := range spentOutputs {
		if outputToSpendEnt.SpentTransactionInputVout < 0 {
			return sparkerrors.InternalObjectMalformedField(fmt.Errorf("spent transaction input vout is negative: %d for txHash %x", outputToSpendEnt.SpentTransactionInputVout, txHash))
		}
		inputIndex := uint32(outputToSpendEnt.SpentTransactionInputVout)
		revocationSecret, ok := revocationSecretMap[inputIndex]
		if !ok {
			return sparkerrors.InternalKeyshareError(fmt.Errorf("no revocation secret found for input index %d for txHash %x", inputIndex, txHash))
		}
		if revocationSecret.IsZero() {
			return sparkerrors.InternalKeyshareError(fmt.Errorf("revocation secret is zero for input index %d for txHash %x", inputIndex, txHash))
		}

		_, err := db.TokenOutput.UpdateOne(outputToSpendEnt).
			SetStatus(st.TokenOutputStatusSpentFinalized).
			SetSpentRevocationSecret(revocationSecret).
			Save(ctx)
		if err != nil {
			return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to update spent output for txHash %x: %w", txHash, err))
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
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to bulk update output status to finalized for txHash %x: %w", txHash, err))
	}

	// Update the token transaction status to Finalized.
	_, err = db.TokenTransaction.UpdateOne(tokenTransactionEnt).
		SetStatus(st.TokenTransactionStatusFinalized).
		Save(ctx)
	if err != nil {
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to update token transaction with finalized status for txHash %x: %w", txHash, err))
	}
	if err := tx.Commit(); err != nil {
		return sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to commit and replace transaction after finalizing token transaction: %w", err))
	}

	return nil
}

func FetchPartialTokenTransactionData(ctx context.Context, partialTokenTransactionHash []byte) (*TokenTransaction, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to get db from context: %w", err))
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
		if IsNotFound(err) {
			return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("partial token transaction not found for hash %x: %w", partialTokenTransactionHash, err))
		}
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to fetch partial token transaction by hash: %w", err))
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
		return nil, sparkerrors.InternalTypeConversionError(fmt.Errorf("failed to convert token transaction: %w", err))
	}

	txType, err := utils.InferTokenTransactionTypeSparkProtos(sparkTx)
	if err != nil {
		return nil, err
	}

	switch txType {
	case utils.TokenTransactionTypeCreate:
		if tokenTransaction.Edges.Create == nil {
			return nil, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("database has no create transaction but protobuf has create input - transaction type mismatch"))
		}
	case utils.TokenTransactionTypeMint:
		if tokenTransaction.Edges.Mint == nil {
			return nil, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("database has no mint transaction but protobuf has mint input - transaction type mismatch"))
		}
	case utils.TokenTransactionTypeTransfer:
		if tokenTransaction.Edges.Create != nil || tokenTransaction.Edges.Mint != nil {
			return nil, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("database has create/mint transaction but protobuf has transfer input - transaction type mismatch"))
		}
		transferInput := finalTokenTransaction.GetTransferInput()
		if len(transferInput.GetOutputsToSpend()) != len(tokenTransaction.Edges.SpentOutput) {
			return nil, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf(
				"number of inputs in proto (%d) does not match number of spent outputs started with this transaction in the database (%d)",
				len(transferInput.GetOutputsToSpend()),
				len(tokenTransaction.Edges.SpentOutput),
			))
		}
	default:
		return nil, sparkerrors.InternalObjectMalformedField(fmt.Errorf("token transaction type unknown"))
	}

	if len(finalTokenTransaction.TokenOutputs) != len(tokenTransaction.Edges.CreatedOutput) {
		return nil, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf(
			"number of outputs in proto (%d) does not match number of created outputs started with this transaction in the database (%d)",
			len(finalTokenTransaction.TokenOutputs),
			len(tokenTransaction.Edges.CreatedOutput),
		))
	}
	return tokenTransaction, nil
}

func FetchAndLockTokenTransactionDataByHash(ctx context.Context, tokenTransactionHash []byte) (*TokenTransaction, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to get db from context: %w", err))
	}

	tokenTransaction, err := db.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHash(tokenTransactionHash)).
		// Lock outputs which may be updated along with this token transaction.
		WithCreatedOutput(func(q *TokenOutputQuery) {
			q.ForUpdate()
		}).
		// Lock inputs which may be updated along with this token transaction.
		WithSpentOutput(func(q *TokenOutputQuery) {
			// Needed to enable computation of the progress of a transaction commit.
			// Don't lock because revocation keyshares are append-only.
			q.WithRevocationKeyshare().
				WithTokenPartialRevocationSecretShares().
				// Needed to enable marshalling of the token transaction proto.
				// Don't lock because data for prior token transactions is immutable.
				WithOutputCreatedTokenTransaction().
				ForUpdate()
		}).
		// Don't lock because peer signatures are append-only.
		WithPeerSignatures().
		// Don't lock because although we set the operator-specific issuer signature during signing,
		// there is only one writer under a locked TokenTransaction, so a separate Mint lock is unnecessary.
		WithMint().
		// Don't lock so that token transactions for a token can be executed in parallel.
		// Overmint prevention is enforced by locking TokenCreate dosntream when checking max-supply
		// (ValidateMintDoesNotExceedMaxSupply* calls ForUpdate on TokenCreate).
		WithCreate().
		// Lock invoice which may may not be re-mapped depending on the state of this token transaction.
		WithSparkInvoice(func(q *SparkInvoiceQuery) {
			q.ForUpdate()
		}).
		ForUpdate().
		Only(ctx)
	if err != nil {
		if IsNotFound(err) {
			return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("token transaction not found for hash %x: %w", tokenTransactionHash, err))
		}
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to fetch and lock token transaction by hash: %w", err))
	}

	return tokenTransaction, nil
}

// FetchTokenTransactionDataByHashForRead refetches the transaction with all its relations without acquiring a row lock.
func FetchTokenTransactionDataByHashForRead(ctx context.Context, tokenTransactionHash []byte) (*TokenTransaction, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to get db from context: %w", err))
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
		Only(ctx)
	if err != nil {
		if IsNotFound(err) {
			return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("token transaction not found for hash %x: %w", tokenTransactionHash, err))
		}
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to fetch token transaction for read by hash: %w", err))
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
		InvoiceAttachments:              invoiceAttachments,
	}
	if !t.ClientCreatedTimestamp.IsZero() {
		tokenTransaction.ClientCreatedTimestamp = timestamppb.New(t.ClientCreatedTimestamp)
	}

	if !t.ExpiryTime.IsZero() {
		tokenTransaction.ExpiryTime = timestamppb.New(t.ExpiryTime)
	}

	network, err := t.GetNetworkFromEdges()
	if err != nil {
		return nil, err
	}
	networkProto, err := network.MarshalProto()
	if err != nil {
		return nil, err
	}
	tokenTransaction.Network = networkProto

	if t.Version >= 3 {
		tokenTransaction.ValidityDurationSeconds = proto.Uint64(t.ValidityDurationSeconds)
	}

	// Sort outputs to match the original token transaction using CreatedTransactionOutputVout
	sortedCreatedOutputs := slices.SortedFunc(slices.Values(t.Edges.CreatedOutput), func(a, b *TokenOutput) int {
		return cmp.Compare(a.CreatedTransactionOutputVout, b.CreatedTransactionOutputVout)
	})

	for i, output := range sortedCreatedOutputs {
		tokenTransaction.TokenOutputs[i] = &tokenpb.TokenOutput{
			Id:                            proto.String(output.ID.String()),
			OwnerPublicKey:                output.OwnerPublicKey.Serialize(),
			RevocationCommitment:          output.WithdrawRevocationCommitment,
			WithdrawBondSats:              &output.WithdrawBondSats,
			WithdrawRelativeBlockLocktime: &output.WithdrawRelativeBlockLocktime,
			TokenPublicKey:                output.TokenPublicKey.Serialize(),
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
				IssuerPublicKey: t.Edges.Mint.IssuerPublicKey.Serialize(),
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
				return nil, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("output spent transaction edge not loaded for output %s", output.ID))
			}

			transferInput.OutputsToSpend[i] = &tokenpb.TokenOutputToSpend{
				PrevTokenTransactionHash: output.Edges.OutputCreatedTokenTransaction.FinalizedTokenTransactionHash,
				PrevTokenTransactionVout: uint32(output.CreatedTransactionOutputVout),
			}
		}

		tokenTransaction.TokenInputs = &tokenpb.TokenTransaction_TransferInput{TransferInput: transferInput}

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
		return nil, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("Signed/Finalized transaction unexpectedly does not map to input TTXOs and cannot be marshalled: %s", t.ID))
	}
	return tokenTransaction, nil
}

func setTokenTransactionTimingFields(
	builder *TokenTransactionCreate,
	tokenTransaction *tokenpb.TokenTransaction,
) (*TokenTransactionCreate, error) {
	if tokenTransaction.Version >= 3 {
		if tokenTransaction.ClientCreatedTimestamp == nil {
			return nil, sparkerrors.InternalObjectMissingField(
				fmt.Errorf("v3+ token transaction missing client_created_timestamp"),
			)
		}
		builder = builder.SetClientCreatedTimestamp(tokenTransaction.ClientCreatedTimestamp.AsTime())

		if tokenTransaction.GetValidityDurationSeconds() == 0 {
			return nil, sparkerrors.InternalObjectMissingField(
				fmt.Errorf("v3+ token transaction missing validity_duration_seconds"),
			)
		}
		builder = builder.SetValidityDurationSeconds(tokenTransaction.GetValidityDurationSeconds())
		expiryTime := tokenTransaction.ClientCreatedTimestamp.AsTime().Add(
			time.Duration(tokenTransaction.GetValidityDurationSeconds()) * time.Second,
		)
		builder = builder.SetExpiryTime(expiryTime)
		return builder, nil
	}

	if tokenTransaction.ClientCreatedTimestamp != nil {
		builder = builder.SetClientCreatedTimestamp(tokenTransaction.ClientCreatedTimestamp.AsTime())
	}

	if tokenTransaction.ExpiryTime != nil {
		builder = builder.SetExpiryTime(tokenTransaction.ExpiryTime.AsTime())
	}

	return builder, nil
}

func (t *TokenTransaction) GetNetworkFromEdges() (btcnetwork.Network, error) {
	txType := t.InferTokenTransactionTypeEnt()
	switch txType {
	case utils.TokenTransactionTypeCreate:
		return t.Edges.Create.Network, nil
	case utils.TokenTransactionTypeMint, utils.TokenTransactionTypeTransfer:
		if len(t.Edges.CreatedOutput) == 0 {
			return btcnetwork.Unspecified, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("no outputs were found when reconstructing token transaction with ID: %s", t.ID))
		}
		// All token transaction outputs must have the same network (confirmed in validation when signing
		// the transaction, so its safe to use the first output).
		return t.Edges.CreatedOutput[0].Network, nil
	default:
		return btcnetwork.Unspecified, sparkerrors.InternalObjectMissingField(fmt.Errorf("unknown token transaction type: %s", txType))
	}
}

// InferTokenTransactionTypeEnt determines the transaction type based on the Ent entity's edges.
// This is more efficient than converting to proto and then inferring the type.
func (t *TokenTransaction) InferTokenTransactionTypeEnt() utils.TokenTransactionType {
	if t.Edges.Create != nil {
		return utils.TokenTransactionTypeCreate
	}
	if t.Edges.Mint != nil {
		return utils.TokenTransactionTypeMint
	}
	// If no create or mint, assume its a transfer.
	return utils.TokenTransactionTypeTransfer
}

// ValidateNotExpired checks if a token transaction has expired and returns an error if it has.
func (t *TokenTransaction) ValidateNotExpired() error {
	now := time.Now().UTC()
	switch {
	case t.Version == 1 || t.Version == 2:
		if !t.ExpiryTime.IsZero() && now.After(t.ExpiryTime.UTC()) {
			return sparkerrors.FailedPreconditionExpired(fmt.Errorf("signing failed because token transaction %s has expired at %s, current time: %s", t.ID, t.ExpiryTime.UTC().Format(time.RFC3339), now.Format(time.RFC3339)))
		}
	case t.Version >= 3:
		validity := time.Duration(t.ValidityDurationSeconds) * time.Second
		if now.After(t.ClientCreatedTimestamp.Add(validity)) {
			return sparkerrors.FailedPreconditionExpired(fmt.Errorf("signing failed because v3+ token transaction %s has expired at %s, current time: %s", t.ID, t.ClientCreatedTimestamp.Add(validity).Format(time.RFC3339), now.Format(time.RFC3339)))
		}
	case t.Version == 0:
	default:
		return sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("unsupported token transaction version: %d", t.Version))
	}
	return nil
}
