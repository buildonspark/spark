package tokens

import (
	"context"
	"encoding/hex"

	"github.com/lightsparkdev/spark/common/logging"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/utils"
	"go.uber.org/zap"
)

type TokenTransactionAttributes struct {
	Type           string
	PartialHashHex string
	FinalHashHex   string
}

func GetTokenTxAttrStringsFromProto(ctx context.Context, tx *tokenpb.TokenTransaction) TokenTransactionAttributes {
	logger := logging.GetLoggerFromContext(ctx)
	if tx == nil {
		logger.Warn("Token transaction is nil when computing attributes")
		return TokenTransactionAttributes{Type: "unknown", PartialHashHex: "unknown", FinalHashHex: "unknown"}
	}
	var attrs TokenTransactionAttributes
	if tt, err := utils.InferTokenTransactionType(tx); err != nil {
		logger.Warn("Failed to infer token transaction type when computing attributes",
			zap.String("transaction", logging.FormatProto("", tx)),
			zap.Error(err))
		attrs.Type = "unknown"
	} else {
		attrs.Type = tt.String()
	}
	if h, err := utils.HashTokenTransaction(tx, true); err != nil {
		logger.Warn("Failed to compute partial token transaction hash when computing attributes",
			zap.String("transaction", logging.FormatProto("", tx)),
			zap.Error(err))
		attrs.PartialHashHex = "unknown"
	} else {
		attrs.PartialHashHex = hex.EncodeToString(h)
	}
	if utils.IsFinalTokenTransaction(tx) {
		if h, err := utils.HashTokenTransaction(tx, false); err != nil {
			logger.Warn("Failed to compute final token transaction hash when computing attributes",
				zap.String("transaction", logging.FormatProto("", tx)),
				zap.Error(err))
			attrs.FinalHashHex = "unknown"
		} else {
			attrs.FinalHashHex = hex.EncodeToString(h)
		}
	} else {
		attrs.FinalHashHex = "unknown"
	}
	return attrs
}

func GetTokenTxAttrStringsFromEnt(ctx context.Context, tx *ent.TokenTransaction) TokenTransactionAttributes {
	logger := logging.GetLoggerFromContext(ctx)
	if tx == nil {
		logger.Warn("Token transaction ent is nil when computing attributes")
		return TokenTransactionAttributes{Type: "unknown", PartialHashHex: "unknown", FinalHashHex: "unknown"}
	}
	var attrs TokenTransactionAttributes
	attrs.Type = tx.InferTokenTransactionTypeEnt().String()
	if len(tx.PartialTokenTransactionHash) == 0 {
		logger.Warn("Partial token transaction hash is empty when computing attributes",
			zap.Stringer("transaction_uuid", tx.ID))
		attrs.PartialHashHex = "unknown"
	} else {
		attrs.PartialHashHex = hex.EncodeToString(tx.PartialTokenTransactionHash)
	}
	if len(tx.FinalizedTokenTransactionHash) == 0 {
		logger.Warn("Final token transaction hash is empty when computing attributes",
			zap.Stringer("transaction_uuid", tx.ID))
		attrs.FinalHashHex = "unknown"
	} else {
		attrs.FinalHashHex = hex.EncodeToString(tx.FinalizedTokenTransactionHash)
	}
	return attrs
}
