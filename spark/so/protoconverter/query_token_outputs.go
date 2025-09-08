package protoconverter

import (
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
)

// TokenProtoQueryTokenOutputsRequestFromSpark converts sparkpb.QueryTokenOutputsRequest to tokenpb.QueryTokenOutputsRequest
func TokenProtoQueryTokenOutputsRequestFromSpark(req *sparkpb.QueryTokenOutputsRequest) *tokenpb.QueryTokenOutputsRequest {
	return &tokenpb.QueryTokenOutputsRequest{
		OwnerPublicKeys:  req.OwnerPublicKeys,
		IssuerPublicKeys: req.TokenPublicKeys, // Field name change: TokenPublicKeys -> IssuerPublicKeys
		TokenIdentifiers: req.TokenIdentifiers,
		Network:          req.Network, // Convert enum
		PageRequest: &sparkpb.PageRequest{
			PageSize:  500,
			Cursor:    "",
			Direction: sparkpb.Direction_NEXT,
		},
	}
}

// SparkQueryTokenOutputsResponseFromTokenProto converts tokenpb.QueryTokenOutputsResponse to sparkpb.QueryTokenOutputsResponse
func SparkQueryTokenOutputsResponseFromTokenProto(resp *tokenpb.QueryTokenOutputsResponse) *sparkpb.QueryTokenOutputsResponse {
	sparkOutputs := make([]*sparkpb.OutputWithPreviousTransactionData, len(resp.OutputsWithPreviousTransactionData))

	for i, tokenOutput := range resp.OutputsWithPreviousTransactionData {
		sparkOutputs[i] = &sparkpb.OutputWithPreviousTransactionData{
			Output: &sparkpb.TokenOutput{
				Id:                            tokenOutput.Output.Id,
				OwnerPublicKey:                tokenOutput.Output.OwnerPublicKey,
				RevocationCommitment:          tokenOutput.Output.RevocationCommitment,
				WithdrawBondSats:              tokenOutput.Output.WithdrawBondSats,
				WithdrawRelativeBlockLocktime: tokenOutput.Output.WithdrawRelativeBlockLocktime,
				TokenPublicKey:                tokenOutput.Output.TokenPublicKey,
				TokenAmount:                   tokenOutput.Output.TokenAmount,
				TokenIdentifier:               tokenOutput.Output.TokenIdentifier,
			},
			PreviousTransactionHash: tokenOutput.PreviousTransactionHash,
			PreviousTransactionVout: tokenOutput.PreviousTransactionVout,
		}
	}

	return &sparkpb.QueryTokenOutputsResponse{
		OutputsWithPreviousTransactionData: sparkOutputs,
	}
}
