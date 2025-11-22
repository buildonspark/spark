package protoconverter

import (
	"fmt"
	"time"

	pb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SparkTokenTransactionFromTokenProto converts a spark_token.TokenTransaction to a spark.TokenTransaction.
func SparkTokenTransactionFromTokenProto(tokenTx *tokenpb.TokenTransaction) (*pb.TokenTransaction, error) {
	if tokenTx == nil {
		return nil, fmt.Errorf("input token transaction cannot be nil")
	}

	tokenOutputs := make([]*pb.TokenOutput, len(tokenTx.TokenOutputs))
	for i, o := range tokenTx.TokenOutputs {
		tokenOutputs[i] = &pb.TokenOutput{
			Id:                            o.Id,
			OwnerPublicKey:                o.OwnerPublicKey,
			RevocationCommitment:          o.RevocationCommitment,
			WithdrawBondSats:              o.WithdrawBondSats,
			WithdrawRelativeBlockLocktime: o.WithdrawRelativeBlockLocktime,
			TokenPublicKey:                o.TokenPublicKey,
			TokenIdentifier:               o.TokenIdentifier,
			TokenAmount:                   o.TokenAmount,
		}
	}

	transaction := &pb.TokenTransaction{
		TokenOutputs:                    tokenOutputs,
		SparkOperatorIdentityPublicKeys: tokenTx.SparkOperatorIdentityPublicKeys,
		Network:                         tokenTx.Network,
		// Note: ExpiryTime and Version fields are omitted as they do not exist in pb.TokenTransaction.
	}

	switch x := tokenTx.TokenInputs.(type) {
	case *tokenpb.TokenTransaction_CreateInput:
		if x.CreateInput == nil {
			return nil, fmt.Errorf("create_input is nil")
		}
		transaction.TokenInputs = &pb.TokenTransaction_CreateInput{
			CreateInput: &pb.TokenCreateInput{
				IssuerPublicKey:         x.CreateInput.IssuerPublicKey,
				TokenName:               x.CreateInput.TokenName,
				TokenTicker:             x.CreateInput.TokenTicker,
				Decimals:                x.CreateInput.Decimals,
				MaxSupply:               x.CreateInput.MaxSupply,
				IsFreezable:             x.CreateInput.IsFreezable,
				CreationEntityPublicKey: x.CreateInput.CreationEntityPublicKey,
			},
		}
	case *tokenpb.TokenTransaction_MintInput:
		if x.MintInput == nil {
			return nil, fmt.Errorf("mint_input is nil")
		}
		var issuerProvidedTimestamp uint64
		if tokenTx.ClientCreatedTimestamp != nil {
			issuerProvidedTimestamp = uint64(tokenTx.ClientCreatedTimestamp.AsTime().UnixMilli())
		}
		transaction.TokenInputs = &pb.TokenTransaction_MintInput{
			MintInput: &pb.TokenMintInput{
				IssuerPublicKey:         x.MintInput.IssuerPublicKey,
				TokenIdentifier:         x.MintInput.TokenIdentifier,
				IssuerProvidedTimestamp: issuerProvidedTimestamp,
			},
		}
	case *tokenpb.TokenTransaction_TransferInput:
		if x.TransferInput == nil {
			return nil, fmt.Errorf("transfer_input is nil")
		}
		outputsToSpend := make([]*pb.TokenOutputToSpend, len(x.TransferInput.OutputsToSpend))
		for i, o := range x.TransferInput.OutputsToSpend {
			outputsToSpend[i] = &pb.TokenOutputToSpend{
				PrevTokenTransactionHash: o.PrevTokenTransactionHash,
				PrevTokenTransactionVout: o.PrevTokenTransactionVout,
			}
		}
		transaction.TokenInputs = &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TokenTransferInput{
				OutputsToSpend: outputsToSpend,
			},
		}
	default:
		return nil, fmt.Errorf("unknown token_inputs type")
	}

	return transaction, nil
}

// TokenProtoFromSparkTokenTransaction converts a spark TokenTransaction proto to a spark_token TokenTransaction proto.
func TokenProtoFromSparkTokenTransaction(sparkTx *pb.TokenTransaction) (*tokenpb.TokenTransaction, error) {
	if sparkTx == nil {
		return nil, fmt.Errorf("input spark token transaction cannot be nil")
	}

	tokenOutputs := make([]*tokenpb.TokenOutput, len(sparkTx.TokenOutputs))
	for i, o := range sparkTx.TokenOutputs {
		tokenOutputs[i] = &tokenpb.TokenOutput{
			Id:                            o.Id,
			OwnerPublicKey:                o.OwnerPublicKey,
			RevocationCommitment:          o.RevocationCommitment,
			WithdrawBondSats:              o.WithdrawBondSats,
			WithdrawRelativeBlockLocktime: o.WithdrawRelativeBlockLocktime,
			TokenPublicKey:                o.TokenPublicKey,
			TokenIdentifier:               o.TokenIdentifier,
			TokenAmount:                   o.TokenAmount,
		}
	}

	tokenTx := &tokenpb.TokenTransaction{
		Version:                         0,
		TokenOutputs:                    tokenOutputs,
		SparkOperatorIdentityPublicKeys: sparkTx.SparkOperatorIdentityPublicKeys,
		Network:                         sparkTx.Network,
	}

	switch x := sparkTx.TokenInputs.(type) {
	case *pb.TokenTransaction_CreateInput:
		if x.CreateInput == nil {
			return nil, fmt.Errorf("create_input is nil")
		}
		tokenTx.TokenInputs = &tokenpb.TokenTransaction_CreateInput{
			CreateInput: &tokenpb.TokenCreateInput{
				IssuerPublicKey:         x.CreateInput.IssuerPublicKey,
				TokenName:               x.CreateInput.TokenName,
				TokenTicker:             x.CreateInput.TokenTicker,
				Decimals:                x.CreateInput.Decimals,
				MaxSupply:               x.CreateInput.MaxSupply,
				IsFreezable:             x.CreateInput.IsFreezable,
				CreationEntityPublicKey: x.CreateInput.CreationEntityPublicKey,
			},
		}
	case *pb.TokenTransaction_MintInput:
		if x.MintInput == nil {
			return nil, fmt.Errorf("mint_input is nil")
		}
		var clientCreatedTimestamp *timestamppb.Timestamp
		if x.MintInput.IssuerProvidedTimestamp != 0 {
			clientCreatedTimestamp = timestamppb.New(time.UnixMilli(int64(x.MintInput.IssuerProvidedTimestamp)))
		}
		tokenTx.TokenInputs = &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: x.MintInput.IssuerPublicKey,
				TokenIdentifier: x.MintInput.TokenIdentifier,
			},
		}
		tokenTx.ClientCreatedTimestamp = clientCreatedTimestamp
	case *pb.TokenTransaction_TransferInput:
		if x.TransferInput == nil {
			return nil, fmt.Errorf("transfer_input is nil")
		}
		outputsToSpend := make([]*tokenpb.TokenOutputToSpend, len(x.TransferInput.OutputsToSpend))
		for i, o := range x.TransferInput.OutputsToSpend {
			outputsToSpend[i] = &tokenpb.TokenOutputToSpend{
				PrevTokenTransactionHash: o.PrevTokenTransactionHash,
				PrevTokenTransactionVout: o.PrevTokenTransactionVout,
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
	if v := partial.TokenTransactionMetadata.GetValidityDurationSeconds(); v != 0 {
		validityDuration = proto.Uint64(v)
	}

	legacy := &tokenpb.TokenTransaction{
		Version:                         partial.GetVersion(),
		SparkOperatorIdentityPublicKeys: partial.TokenTransactionMetadata.GetSparkOperatorIdentityPublicKeys(),
		Network:                         partial.TokenTransactionMetadata.GetNetwork(),
		ClientCreatedTimestamp:          partial.TokenTransactionMetadata.GetClientCreatedTimestamp(),
		InvoiceAttachments:              partial.TokenTransactionMetadata.GetInvoiceAttachments(),
		ValidityDurationSeconds:         validityDuration,
	}

	switch input := partial.TokenInputs.(type) {
	case *tokenpb.PartialTokenTransaction_MintInput:
		legacy.TokenInputs = &tokenpb.TokenTransaction_MintInput{MintInput: input.MintInput}
	case *tokenpb.PartialTokenTransaction_TransferInput:
		legacy.TokenInputs = &tokenpb.TokenTransaction_TransferInput{TransferInput: input.TransferInput}
	case *tokenpb.PartialTokenTransaction_CreateInput:
		legacy.TokenInputs = &tokenpb.TokenTransaction_CreateInput{CreateInput: input.CreateInput}
	default:
		return nil, fmt.Errorf("unknown token input type: %T", input)
	}

	legacy.TokenOutputs = make([]*tokenpb.TokenOutput, len(partial.PartialTokenOutputs))
	for i, partialOutput := range partial.PartialTokenOutputs {
		var withdrawBond *uint64
		if v := partialOutput.GetWithdrawBondSats(); v != 0 {
			withdrawBond = proto.Uint64(v)
		}
		var withdrawLocktime *uint64
		if v := partialOutput.GetWithdrawRelativeBlockLocktime(); v != 0 {
			withdrawLocktime = proto.Uint64(v)
		}
		legacy.TokenOutputs[i] = &tokenpb.TokenOutput{
			OwnerPublicKey:                partialOutput.GetOwnerPublicKey(),
			WithdrawBondSats:              withdrawBond,
			WithdrawRelativeBlockLocktime: withdrawLocktime,
			TokenIdentifier:               partialOutput.TokenIdentifier,
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
	if v := final.TokenTransactionMetadata.GetValidityDurationSeconds(); v != 0 {
		validityDuration = proto.Uint64(v)
	}

	legacy := &tokenpb.TokenTransaction{
		Version:                         final.GetVersion(),
		SparkOperatorIdentityPublicKeys: final.TokenTransactionMetadata.GetSparkOperatorIdentityPublicKeys(),
		Network:                         final.TokenTransactionMetadata.GetNetwork(),
		ClientCreatedTimestamp:          final.TokenTransactionMetadata.GetClientCreatedTimestamp(),
		InvoiceAttachments:              final.TokenTransactionMetadata.GetInvoiceAttachments(),
		ValidityDurationSeconds:         validityDuration,
	}

	switch input := final.TokenInputs.(type) {
	case *tokenpb.FinalTokenTransaction_MintInput:
		legacy.TokenInputs = &tokenpb.TokenTransaction_MintInput{MintInput: input.MintInput}
	case *tokenpb.FinalTokenTransaction_TransferInput:
		legacy.TokenInputs = &tokenpb.TokenTransaction_TransferInput{TransferInput: input.TransferInput}
	case *tokenpb.FinalTokenTransaction_CreateInput:
		legacy.TokenInputs = &tokenpb.TokenTransaction_CreateInput{CreateInput: input.CreateInput}
	default:
		return nil, fmt.Errorf("unknown token input type: %T", input)
	}

	legacy.TokenOutputs = make([]*tokenpb.TokenOutput, len(final.FinalTokenOutputs))
	for i, finalOutput := range final.FinalTokenOutputs {
		partialOutput := finalOutput.GetPartialTokenOutput()
		if partialOutput == nil {
			legacy.TokenOutputs[i] = &tokenpb.TokenOutput{}
		} else {
			var withdrawBond *uint64
			if v := partialOutput.GetWithdrawBondSats(); v != 0 {
				withdrawBond = proto.Uint64(v)
			}
			var withdrawLocktime *uint64
			if v := partialOutput.GetWithdrawRelativeBlockLocktime(); v != 0 {
				withdrawLocktime = proto.Uint64(v)
			}
			legacy.TokenOutputs[i] = &tokenpb.TokenOutput{
				OwnerPublicKey:                partialOutput.GetOwnerPublicKey(),
				WithdrawBondSats:              withdrawBond,
				WithdrawRelativeBlockLocktime: withdrawLocktime,
				TokenIdentifier:               partialOutput.TokenIdentifier,
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
	}

	// Map inputs while erasing SO-filled fields for partials
	switch input := legacy.TokenInputs.(type) {
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
				// CreationEntityPublicKey intentionally omitted (partial transaction)
			},
		}
	default:
		// Unknown or missing input; return what we have so callers can handle nil gracefully
	}

	// Map outputs to PartialTokenOutput, erasing SO-filled fields (id, revocation, etc.)
	if legacy.TokenOutputs != nil {
		partial.PartialTokenOutputs = make([]*tokenpb.PartialTokenOutput, len(legacy.TokenOutputs))
		for i, o := range legacy.TokenOutputs {
			if o == nil {
				partial.PartialTokenOutputs[i] = nil
				continue
			}
			var withdrawBond uint64
			if o.WithdrawBondSats != nil {
				withdrawBond = *o.WithdrawBondSats
			}
			var withdrawLocktime uint64
			if o.WithdrawRelativeBlockLocktime != nil {
				withdrawLocktime = *o.WithdrawRelativeBlockLocktime
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
	}

	switch input := legacy.TokenInputs.(type) {
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
