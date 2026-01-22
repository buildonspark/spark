package tokens

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/logging"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/utils"
	"go.uber.org/zap"
)

type TokenTransactionAttributes struct {
	Type                    string
	PartialHashHex          string
	FinalHashHex            string
	Bech32mTokenIdentifiers string
}

func GetTokenTxAttrStringsFromProto(ctx context.Context, tx *tokenpb.TokenTransaction) TokenTransactionAttributes {
	logger := logging.GetLoggerFromContext(ctx).Sugar()
	if tx == nil {
		logger.Warn("Token transaction is nil when computing attributes from proto")
		return TokenTransactionAttributes{Type: "unknown", PartialHashHex: "unknown", FinalHashHex: "unknown"}
	}
	var attrs TokenTransactionAttributes
	attrs.Bech32mTokenIdentifiers = "unknown"
	tt, err := utils.InferTokenTransactionType(tx)
	if err != nil {
		logger.With(zap.Error(err)).Warnf("Failed to infer token transaction type when computing attributes from proto. tx: %s", logging.FormatProto("", tx))
		attrs.Type = "unknown"
	} else {
		attrs.Type = tt.String()
	}

	if h, err := utils.HashTokenTransaction(tx, true); err != nil {
		logger.With(zap.Error(err)).Warnf("Failed to compute partial token transaction hash when computing attributes from proto. tx: %s", logging.FormatProto("", tx))
		attrs.PartialHashHex = "unknown"
	} else {
		attrs.PartialHashHex = hex.EncodeToString(h)
	}

	if utils.IsFinalTokenTransaction(tx) {
		if h, err := utils.HashTokenTransaction(tx, false); err != nil {
			logger.With(zap.Error(err)).Warnf("Failed to compute final token transaction hash when computing attributes from proto. tx: %s", logging.FormatProto("", tx))
			attrs.FinalHashHex = "unknown"
		} else {
			attrs.FinalHashHex = hex.EncodeToString(h)
		}
	} else {
		attrs.FinalHashHex = "unknown"
	}

	var rawTokenIdentifiers [][]byte
	switch tt {
	case utils.TokenTransactionTypeMint:
		rawTokenIdentifiers = append(rawTokenIdentifiers, tx.GetMintInput().GetTokenIdentifier())
	case utils.TokenTransactionTypeCreate:
		tokenMetadata, err := common.NewTokenMetadataFromCreateInput(tx.GetCreateInput(), tx.GetNetwork())
		if err != nil {
			logger.With(zap.Error(err)).Warnf("Failed to create token metadata when computing attributes from proto. tx: %s", logging.FormatProto("", tx))
		} else {
			computedTokenIdentifier, err := tokenMetadata.ComputeTokenIdentifier()
			if err != nil {
				logger.With(zap.Error(err)).Warnf("Failed to compute token identifier when computing attributes from proto. tx: %s", logging.FormatProto("", tx))
			} else {
				rawTokenIdentifiers = append(rawTokenIdentifiers, computedTokenIdentifier)
			}
		}
	case utils.TokenTransactionTypeTransfer:
		uniqueRawTokenIdentifiers := make(map[string]bool)
		for _, output := range tx.GetTokenOutputs() {
			uniqueRawTokenIdentifiers[string(output.GetTokenIdentifier())] = true
		}
		for rawTokenIdentifier := range uniqueRawTokenIdentifiers {
			rawTokenIdentifiers = append(rawTokenIdentifiers, []byte(rawTokenIdentifier))
		}
	default:
		logger.Warnf("Unknown token transaction type when computing attributes from proto. tx: %s, type: %s", logging.FormatProto("", tx), tt)
	}
	if len(rawTokenIdentifiers) > 0 {
		network, err := btcnetwork.FromProtoNetwork(tx.GetNetwork())
		if err != nil {
			logger.With(zap.Error(err)).Warnf("Failed to convert network to common network when computing attributes from proto. tx: %s", logging.FormatProto("", tx))
		}
		var bech32mTokenIdentifiers []string
		for _, rawTokenIdentifier := range rawTokenIdentifiers {
			bech32mTokenIdentifier, err := encodeBech32mTokenIdentifier(rawTokenIdentifier, network)
			if err != nil {
				logger.With(zap.Error(err)).Warnf("Failed to encode bech32m token identifier when computing attributes from proto. tx: %s", logging.FormatProto("", tx))
			}
			bech32mTokenIdentifiers = append(bech32mTokenIdentifiers, bech32mTokenIdentifier)
		}
		attrs.Bech32mTokenIdentifiers = strings.Join(bech32mTokenIdentifiers, ",")
	}
	return attrs
}

func GetTokenTxAttrStringsFromEnt(ctx context.Context, tx *ent.TokenTransaction) TokenTransactionAttributes {
	logger := logging.GetLoggerFromContext(ctx).Sugar()
	if tx == nil {
		logger.Warn("Token transaction ent is nil when computing attributes from ent")
		return TokenTransactionAttributes{Type: "unknown", PartialHashHex: "unknown", FinalHashHex: "unknown"}
	}
	var attrs TokenTransactionAttributes
	attrs.Type = tx.InferTokenTransactionTypeEnt().String()
	if len(tx.PartialTokenTransactionHash) == 0 {
		logger.Warnf("Partial token transaction hash is empty when computing attributes from ent. tx_uuid: %s", tx.ID)
		attrs.PartialHashHex = "unknown"
	} else {
		attrs.PartialHashHex = hex.EncodeToString(tx.PartialTokenTransactionHash)
	}
	if len(tx.FinalizedTokenTransactionHash) == 0 {
		logger.Warnf("Final token transaction hash is empty when computing attributes from ent. tx_uuid: %s", tx.ID)
		attrs.FinalHashHex = "unknown"
	} else {
		attrs.FinalHashHex = hex.EncodeToString(tx.FinalizedTokenTransactionHash)
	}

	if tx.Edges.Mint != nil {
		if len(tx.Edges.CreatedOutput) > 0 {
			bech32mTokenIdentifier, err := encodeBech32mTokenIdentifier(tx.Edges.Mint.TokenIdentifier, tx.Edges.CreatedOutput[0].Network)
			if err != nil {
				logger.With(zap.Error(err)).Warnf("Failed to encode bech32m token identifier when computing attributes from ent. tx_uuid: %s", tx.ID)
			}
			attrs.Bech32mTokenIdentifiers = bech32mTokenIdentifier
		} else {
			logger.Warnf("No created outputs when computing attributes from ent. tx_uuid: %s", tx.ID)
			attrs.Bech32mTokenIdentifiers = "unknown"
		}
	} else if tx.Edges.Create != nil {
		bech32mTokenIdentifier, err := encodeBech32mTokenIdentifier(tx.Edges.Create.TokenIdentifier, tx.Edges.Create.Network)
		if err != nil {
			logger.With(zap.Error(err)).Warnf("Failed to encode bech32m token identifier when computing attributes from ent. tx_uuid: %s", tx.ID)
		}
		attrs.Bech32mTokenIdentifiers = bech32mTokenIdentifier
	} else if len(tx.Edges.CreatedOutput) > 0 {
		network := tx.Edges.CreatedOutput[0].Network
		uniqueRawTokenIdentifiers := make(map[string]bool)
		var rawTokenIdentifiers [][]byte
		var bech32mTokenIdentifiers []string
		for _, output := range tx.Edges.CreatedOutput {
			uniqueRawTokenIdentifiers[string(output.TokenIdentifier)] = true
		}
		for rawTokenIdentifier := range uniqueRawTokenIdentifiers {
			rawTokenIdentifiers = append(rawTokenIdentifiers, []byte(rawTokenIdentifier))
		}
		for _, rawTokenIdentifier := range rawTokenIdentifiers {
			bech32mTokenIdentifier, err := encodeBech32mTokenIdentifier(rawTokenIdentifier, network)
			if err != nil {
				logger.With(zap.Error(err)).Warnf("Failed to encode bech32m token identifier when computing attributes from ent. tx_uuid: %s", tx.ID)
			}
			bech32mTokenIdentifiers = append(bech32mTokenIdentifiers, bech32mTokenIdentifier)
		}
		attrs.Bech32mTokenIdentifiers = strings.Join(bech32mTokenIdentifiers, ",")
	} else {
		logger.Warnf("No created outputs found when computing attributes from ent. tx_uuid: %s", tx.ID)
		attrs.Bech32mTokenIdentifiers = "unknown"
	}

	return attrs
}

var tokenIdentifierNetworkPrefix = map[btcnetwork.Network]string{
	btcnetwork.Mainnet: "btkn",
	btcnetwork.Regtest: "btknrt",
	btcnetwork.Testnet: "btknt",
	btcnetwork.Signet:  "btkns",
}

func encodeBech32mTokenIdentifier(tokenIdentifier []byte, network btcnetwork.Network) (string, error) {
	prefix, exists := tokenIdentifierNetworkPrefix[network]
	if !exists {
		return "", fmt.Errorf("unsupported network: %v", network)
	}

	bech32Data, err := bech32.ConvertBits(tokenIdentifier, 8, 5, true)
	if err != nil {
		return "", fmt.Errorf("failed to convert bits for bech32m encoding: %w", err)
	}

	encoded, err := bech32.EncodeM(prefix, bech32Data)
	if err != nil {
		return "", fmt.Errorf("failed to encode bech32m: %w", err)
	}

	return encoded, nil
}
