package protoconverter

import (
	"fmt"
	"time"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	legacypb "github.com/lightsparkdev/spark/proto/spark_token_legacy"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SparkTokenTransactionFromTokenProto converts a spark_token.TokenTransaction to a spark.TokenTransaction.
func SparkTokenTransactionFromTokenProto(tokenTx *tokenpb.TokenTransaction) (*legacypb.TokenTransaction, error) {
	if tokenTx == nil {
		return nil, fmt.Errorf("input token transaction cannot be nil")
	}

	tokenOutputs := make([]*legacypb.TokenOutput, len(tokenTx.GetTokenOutputs()))
	for i, o := range tokenTx.GetTokenOutputs() {
		if o == nil {
			return nil, fmt.Errorf("token output %d is nil", i)
		}
		tokenOutputs[i] = &legacypb.TokenOutput{
			Id:                            o.Id,
			OwnerPublicKey:                o.GetOwnerPublicKey(),
			RevocationCommitment:          o.GetRevocationCommitment(),
			WithdrawBondSats:              o.WithdrawBondSats,
			WithdrawRelativeBlockLocktime: o.WithdrawRelativeBlockLocktime,
			TokenPublicKey:                o.GetTokenPublicKey(),
			TokenIdentifier:               o.GetTokenIdentifier(),
			TokenAmount:                   o.GetTokenAmount(),
		}
	}

	transaction := &legacypb.TokenTransaction{
		TokenOutputs:                    tokenOutputs,
		SparkOperatorIdentityPublicKeys: tokenTx.GetSparkOperatorIdentityPublicKeys(),
		Network:                         tokenTx.GetNetwork(),
		// Note: ExpiryTime and Version fields are omitted as they do not exist in legacypb.TokenTransaction.
	}

	switch x := tokenTx.GetTokenInputs().(type) {
	case *tokenpb.TokenTransaction_CreateInput:
		if x.CreateInput == nil {
			return nil, fmt.Errorf("create_input is nil")
		}
		transaction.TokenInputs = &legacypb.TokenTransaction_CreateInput{
			CreateInput: &legacypb.TokenCreateInput{
				IssuerPublicKey:         x.CreateInput.GetIssuerPublicKey(),
				TokenName:               x.CreateInput.GetTokenName(),
				TokenTicker:             x.CreateInput.GetTokenTicker(),
				Decimals:                x.CreateInput.GetDecimals(),
				MaxSupply:               x.CreateInput.GetMaxSupply(),
				IsFreezable:             x.CreateInput.GetIsFreezable(),
				CreationEntityPublicKey: x.CreateInput.GetCreationEntityPublicKey(),
			},
		}
	case *tokenpb.TokenTransaction_MintInput:
		if x.MintInput == nil {
			return nil, fmt.Errorf("mint_input is nil")
		}
		var issuerProvidedTimestamp uint64
		if tokenTx.GetClientCreatedTimestamp() != nil {
			issuerProvidedTimestamp = uint64(tokenTx.GetClientCreatedTimestamp().AsTime().UnixMilli())
		}
		transaction.TokenInputs = &legacypb.TokenTransaction_MintInput{
			MintInput: &legacypb.TokenMintInput{
				IssuerPublicKey:         x.MintInput.GetIssuerPublicKey(),
				TokenIdentifier:         x.MintInput.GetTokenIdentifier(),
				IssuerProvidedTimestamp: issuerProvidedTimestamp,
			},
		}
	case *tokenpb.TokenTransaction_TransferInput:
		if x.TransferInput == nil {
			return nil, fmt.Errorf("transfer_input is nil")
		}
		outputsToSpend := make([]*legacypb.TokenOutputToSpend, len(x.TransferInput.GetOutputsToSpend()))
		for i, o := range x.TransferInput.GetOutputsToSpend() {
			if o == nil {
				return nil, fmt.Errorf("transfer output to spend %d is nil", i)
			}
			outputsToSpend[i] = &legacypb.TokenOutputToSpend{
				PrevTokenTransactionHash: o.GetPrevTokenTransactionHash(),
				PrevTokenTransactionVout: o.GetPrevTokenTransactionVout(),
			}
		}
		transaction.TokenInputs = &legacypb.TokenTransaction_TransferInput{
			TransferInput: &legacypb.TokenTransferInput{
				OutputsToSpend: outputsToSpend,
			},
		}
	default:
		return nil, fmt.Errorf("unknown token_inputs type")
	}

	return transaction, nil
}

// TokenProtoFromSparkTokenTransaction converts a spark TokenTransaction proto to a spark_token TokenTransaction proto.
func TokenProtoFromSparkTokenTransaction(sparkTx *legacypb.TokenTransaction) (*tokenpb.TokenTransaction, error) {
	if sparkTx == nil {
		return nil, fmt.Errorf("input spark token transaction cannot be nil")
	}

	tokenOutputs := make([]*tokenpb.TokenOutput, len(sparkTx.GetTokenOutputs()))
	for i, o := range sparkTx.GetTokenOutputs() {
		if o == nil {
			return nil, fmt.Errorf("token output %d is nil", i)
		}
		tokenOutputs[i] = &tokenpb.TokenOutput{
			Id:                            o.Id,
			OwnerPublicKey:                o.GetOwnerPublicKey(),
			RevocationCommitment:          o.GetRevocationCommitment(),
			WithdrawBondSats:              o.WithdrawBondSats,
			WithdrawRelativeBlockLocktime: o.WithdrawRelativeBlockLocktime,
			TokenPublicKey:                o.GetTokenPublicKey(),
			TokenIdentifier:               o.GetTokenIdentifier(),
			TokenAmount:                   o.GetTokenAmount(),
		}
	}

	tokenTx := &tokenpb.TokenTransaction{
		Version:                         0,
		TokenOutputs:                    tokenOutputs,
		SparkOperatorIdentityPublicKeys: sparkTx.GetSparkOperatorIdentityPublicKeys(),
		Network:                         sparkTx.GetNetwork(),
	}

	switch x := sparkTx.GetTokenInputs().(type) {
	case *legacypb.TokenTransaction_CreateInput:
		if x.CreateInput == nil {
			return nil, fmt.Errorf("create_input is nil")
		}
		tokenTx.TokenInputs = &tokenpb.TokenTransaction_CreateInput{
			CreateInput: &tokenpb.TokenCreateInput{
				IssuerPublicKey:         x.CreateInput.GetIssuerPublicKey(),
				TokenName:               x.CreateInput.GetTokenName(),
				TokenTicker:             x.CreateInput.GetTokenTicker(),
				Decimals:                x.CreateInput.GetDecimals(),
				MaxSupply:               x.CreateInput.GetMaxSupply(),
				IsFreezable:             x.CreateInput.GetIsFreezable(),
				CreationEntityPublicKey: x.CreateInput.GetCreationEntityPublicKey(),
			},
		}
	case *legacypb.TokenTransaction_MintInput:
		if x.MintInput == nil {
			return nil, fmt.Errorf("mint_input is nil")
		}
		var clientCreatedTimestamp *timestamppb.Timestamp
		if x.MintInput.GetIssuerProvidedTimestamp() != 0 {
			clientCreatedTimestamp = timestamppb.New(time.UnixMilli(int64(x.MintInput.GetIssuerProvidedTimestamp())))
		}
		tokenTx.TokenInputs = &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: x.MintInput.GetIssuerPublicKey(),
				TokenIdentifier: x.MintInput.GetTokenIdentifier(),
			},
		}
		tokenTx.ClientCreatedTimestamp = clientCreatedTimestamp
	case *legacypb.TokenTransaction_TransferInput:
		if x.TransferInput == nil {
			return nil, fmt.Errorf("transfer_input is nil")
		}
		outputsToSpend := make([]*tokenpb.TokenOutputToSpend, len(x.TransferInput.GetOutputsToSpend()))
		for i, o := range x.TransferInput.GetOutputsToSpend() {
			if o == nil {
				return nil, fmt.Errorf("transfer output to spend %d is nil", i)
			}
			outputsToSpend[i] = &tokenpb.TokenOutputToSpend{
				PrevTokenTransactionHash: o.GetPrevTokenTransactionHash(),
				PrevTokenTransactionVout: o.GetPrevTokenTransactionVout(),
			}
		}
		tokenTx.TokenInputs = &tokenpb.TokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: outputsToSpend,
			},
		}
	default:
		return nil, fmt.Errorf("unknown token_inputs type")
	}

	return tokenTx, nil
}

func ConvertPartialToV2TxShape(partial *tokenpb.PartialTokenTransaction) (*tokenpb.TokenTransaction, error) {
	if partial == nil {
		return nil, nil
	}

	var validityDuration *uint64
	if v := partial.GetTokenTransactionMetadata().GetValidityDurationSeconds(); v != 0 {
		validityDuration = new(v)
	}

	legacy := &tokenpb.TokenTransaction{
		Version:                         partial.GetVersion(),
		SparkOperatorIdentityPublicKeys: partial.GetTokenTransactionMetadata().GetSparkOperatorIdentityPublicKeys(),
		Network:                         partial.GetTokenTransactionMetadata().GetNetwork(),
		ClientCreatedTimestamp:          partial.GetTokenTransactionMetadata().GetClientCreatedTimestamp(),
		InvoiceAttachments:              partial.GetTokenTransactionMetadata().GetInvoiceAttachments(),
		ValidityDurationSeconds:         validityDuration,
		ExecuteBefore:                   partial.GetExecuteBefore(),
	}

	switch input := partial.GetTokenInputs().(type) {
	case *tokenpb.PartialTokenTransaction_MintInput:
		legacy.TokenInputs = &tokenpb.TokenTransaction_MintInput{MintInput: input.MintInput}
	case *tokenpb.PartialTokenTransaction_TransferInput:
		legacy.TokenInputs = &tokenpb.TokenTransaction_TransferInput{TransferInput: input.TransferInput}
	case *tokenpb.PartialTokenTransaction_CreateInput:
		legacy.TokenInputs = &tokenpb.TokenTransaction_CreateInput{CreateInput: input.CreateInput}
	default:
		return nil, fmt.Errorf("unknown token input type: %T", input)
	}

	legacy.TokenOutputs = make([]*tokenpb.TokenOutput, len(partial.GetPartialTokenOutputs()))
	for i, partialOutput := range partial.GetPartialTokenOutputs() {
		if partialOutput == nil {
			return nil, fmt.Errorf("partial token output %d is nil", i)
		}
		var withdrawBond *uint64
		if v := partialOutput.GetWithdrawBondSats(); v != 0 {
			withdrawBond = new(v)
		}
		var withdrawLocktime *uint64
		if v := partialOutput.GetWithdrawRelativeBlockLocktime(); v != 0 {
			withdrawLocktime = new(v)
		}
		legacy.TokenOutputs[i] = &tokenpb.TokenOutput{
			OwnerPublicKey:                partialOutput.GetOwnerPublicKey(),
			WithdrawBondSats:              withdrawBond,
			WithdrawRelativeBlockLocktime: withdrawLocktime,
			TokenIdentifier:               partialOutput.GetTokenIdentifier(),
			TokenAmount:                   partialOutput.GetTokenAmount(),
		}
	}

	return legacy, nil
}

func ConvertFinalToV2TxShape(final *tokenpb.FinalTokenTransaction) (*tokenpb.TokenTransaction, error) {
	if final == nil {
		return nil, nil
	}

	var validityDuration *uint64
	if v := final.GetTokenTransactionMetadata().GetValidityDurationSeconds(); v != 0 {
		validityDuration = new(v)
	}

	legacy := &tokenpb.TokenTransaction{
		Version:                         final.GetVersion(),
		SparkOperatorIdentityPublicKeys: final.GetTokenTransactionMetadata().GetSparkOperatorIdentityPublicKeys(),
		Network:                         final.GetTokenTransactionMetadata().GetNetwork(),
		ClientCreatedTimestamp:          final.GetTokenTransactionMetadata().GetClientCreatedTimestamp(),
		InvoiceAttachments:              final.GetTokenTransactionMetadata().GetInvoiceAttachments(),
		ValidityDurationSeconds:         validityDuration,
		ExecuteBefore:                   final.GetExecuteBefore(),
	}

	switch input := final.GetTokenInputs().(type) {
	case *tokenpb.FinalTokenTransaction_MintInput:
		legacy.TokenInputs = &tokenpb.TokenTransaction_MintInput{MintInput: input.MintInput}
	case *tokenpb.FinalTokenTransaction_TransferInput:
		legacy.TokenInputs = &tokenpb.TokenTransaction_TransferInput{TransferInput: input.TransferInput}
	case *tokenpb.FinalTokenTransaction_CreateInput:
		legacy.TokenInputs = &tokenpb.TokenTransaction_CreateInput{CreateInput: input.CreateInput}
	default:
		return nil, fmt.Errorf("unknown token input type: %T", input)
	}

	legacy.TokenOutputs = make([]*tokenpb.TokenOutput, len(final.GetFinalTokenOutputs()))
	for i, finalOutput := range final.GetFinalTokenOutputs() {
		partialOutput := finalOutput.GetPartialTokenOutput()
		if partialOutput == nil {
			legacy.TokenOutputs[i] = &tokenpb.TokenOutput{}
		} else {
			var withdrawBond *uint64
			if v := partialOutput.GetWithdrawBondSats(); v != 0 {
				withdrawBond = new(v)
			}
			var withdrawLocktime *uint64
			if v := partialOutput.GetWithdrawRelativeBlockLocktime(); v != 0 {
				withdrawLocktime = new(v)
			}
			legacy.TokenOutputs[i] = &tokenpb.TokenOutput{
				OwnerPublicKey:                partialOutput.GetOwnerPublicKey(),
				WithdrawBondSats:              withdrawBond,
				WithdrawRelativeBlockLocktime: withdrawLocktime,
				TokenIdentifier:               partialOutput.GetTokenIdentifier(),
				TokenAmount:                   partialOutput.GetTokenAmount(),
				RevocationCommitment:          finalOutput.GetRevocationCommitment(),
			}
		}
	}

	return legacy, nil
}

func ConvertBroadcastToStart(broadcast *tokenpb.BroadcastTransactionRequest) (*tokenpb.StartTransactionRequest, error) {
	if broadcast == nil {
		return nil, nil
	}

	legacyPartial, err := ConvertPartialToV2TxShape(broadcast.GetPartialTokenTransaction())
	if err != nil {
		return nil, fmt.Errorf("failed to convert partial token transaction to legacy: %w", err)
	}

	startReq := &tokenpb.StartTransactionRequest{
		IdentityPublicKey:                      broadcast.GetIdentityPublicKey(),
		PartialTokenTransaction:                legacyPartial,
		PartialTokenTransactionOwnerSignatures: broadcast.GetTokenTransactionOwnerSignatures(),
		ValidityDurationSeconds:                broadcast.GetPartialTokenTransaction().GetTokenTransactionMetadata().GetValidityDurationSeconds(),
	}

	return startReq, nil
}

func ConvertV2TxShapeToPartial(legacy *tokenpb.TokenTransaction) (*tokenpb.PartialTokenTransaction, error) {
	if legacy == nil {
		return nil, nil
	}

	partial := &tokenpb.PartialTokenTransaction{
		Version: legacy.GetVersion(),
		TokenTransactionMetadata: &tokenpb.TokenTransactionMetadata{
			SparkOperatorIdentityPublicKeys: legacy.GetSparkOperatorIdentityPublicKeys(),
			Network:                         legacy.GetNetwork(),
			ClientCreatedTimestamp:          legacy.GetClientCreatedTimestamp(),
			InvoiceAttachments:              legacy.GetInvoiceAttachments(),
			ValidityDurationSeconds:         legacy.GetValidityDurationSeconds(),
		},
		ExecuteBefore: legacy.GetExecuteBefore(),
	}

	// Map inputs while erasing SO-filled fields for partials
	switch input := legacy.GetTokenInputs().(type) {
	case *tokenpb.TokenTransaction_MintInput:
		partial.TokenInputs = &tokenpb.PartialTokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: input.MintInput.GetIssuerPublicKey(),
				// token_identifier is optional and passed through if present
				TokenIdentifier: input.MintInput.GetTokenIdentifier(),
			},
		}
	case *tokenpb.TokenTransaction_TransferInput:
		// Transfer input is identical structure in v3 partials
		partial.TokenInputs = &tokenpb.PartialTokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: input.TransferInput.GetOutputsToSpend(),
			},
		}
	case *tokenpb.TokenTransaction_CreateInput:
		// creation_entity_public_key is SO-filled; omit in partial
		ci := input.CreateInput
		partial.TokenInputs = &tokenpb.PartialTokenTransaction_CreateInput{
			CreateInput: &tokenpb.TokenCreateInput{
				IssuerPublicKey: ci.GetIssuerPublicKey(),
				TokenName:       ci.GetTokenName(),
				TokenTicker:     ci.GetTokenTicker(),
				Decimals:        ci.GetDecimals(),
				MaxSupply:       ci.GetMaxSupply(),
				IsFreezable:     ci.GetIsFreezable(),
				ExtraMetadata:   ci.GetExtraMetadata(),
				// CreationEntityPublicKey intentionally omitted (partial transaction)
			},
		}
	default:
		// Unknown or missing input; return what we have so callers can handle nil gracefully
	}

	// Map outputs to PartialTokenOutput, erasing SO-filled fields (id, revocation, etc.)
	if legacy.TokenOutputs != nil {
		partial.PartialTokenOutputs = make([]*tokenpb.PartialTokenOutput, len(legacy.GetTokenOutputs()))
		for i, o := range legacy.GetTokenOutputs() {
			if o == nil {
				partial.PartialTokenOutputs[i] = nil
				continue
			}
			var withdrawBond uint64
			if o.WithdrawBondSats != nil {
				withdrawBond = o.GetWithdrawBondSats()
			}
			var withdrawLocktime uint64
			if o.WithdrawRelativeBlockLocktime != nil {
				withdrawLocktime = o.GetWithdrawRelativeBlockLocktime()
			}
			partial.PartialTokenOutputs[i] = &tokenpb.PartialTokenOutput{
				OwnerPublicKey:                o.GetOwnerPublicKey(),
				WithdrawBondSats:              withdrawBond,
				WithdrawRelativeBlockLocktime: withdrawLocktime,
				TokenIdentifier:               o.GetTokenIdentifier(),
				TokenAmount:                   o.GetTokenAmount(),
			}
		}
	}

	return partial, nil
}

func ConvertV2TxShapeToFinal(legacy *tokenpb.TokenTransaction) (*tokenpb.FinalTokenTransaction, error) {
	if legacy == nil {
		return nil, nil
	}

	final := &tokenpb.FinalTokenTransaction{
		Version: legacy.GetVersion(),
		TokenTransactionMetadata: &tokenpb.TokenTransactionMetadata{
			SparkOperatorIdentityPublicKeys: legacy.GetSparkOperatorIdentityPublicKeys(),
			Network:                         legacy.GetNetwork(),
			ClientCreatedTimestamp:          legacy.GetClientCreatedTimestamp(),
			InvoiceAttachments:              legacy.GetInvoiceAttachments(),
			ValidityDurationSeconds:         legacy.GetValidityDurationSeconds(),
		},
		ExecuteBefore: legacy.GetExecuteBefore(),
	}

	switch input := legacy.GetTokenInputs().(type) {
	case *tokenpb.TokenTransaction_MintInput:
		final.TokenInputs = &tokenpb.FinalTokenTransaction_MintInput{MintInput: input.MintInput}
	case *tokenpb.TokenTransaction_TransferInput:
		final.TokenInputs = &tokenpb.FinalTokenTransaction_TransferInput{TransferInput: input.TransferInput}
	case *tokenpb.TokenTransaction_CreateInput:
		final.TokenInputs = &tokenpb.FinalTokenTransaction_CreateInput{CreateInput: input.CreateInput}
	default:
		return nil, fmt.Errorf("unknown token input type: %T", input)
	}

	final.FinalTokenOutputs = make([]*tokenpb.FinalTokenOutput, len(legacy.GetTokenOutputs()))
	for i, legacyOutput := range legacy.GetTokenOutputs() {
		final.FinalTokenOutputs[i] = &tokenpb.FinalTokenOutput{
			PartialTokenOutput: &tokenpb.PartialTokenOutput{
				OwnerPublicKey:                legacyOutput.GetOwnerPublicKey(),
				WithdrawBondSats:              legacyOutput.GetWithdrawBondSats(),
				WithdrawRelativeBlockLocktime: legacyOutput.GetWithdrawRelativeBlockLocktime(),
				TokenIdentifier:               legacyOutput.GetTokenIdentifier(),
				TokenAmount:                   legacyOutput.GetTokenAmount(),
			},
			RevocationCommitment: legacyOutput.GetRevocationCommitment(),
		}
	}

	return final, nil
}
