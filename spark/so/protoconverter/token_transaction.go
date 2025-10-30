package protoconverter

import (
	"fmt"

	pb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
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
