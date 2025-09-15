package tokens

import (
	"encoding/hex"

	"github.com/lightsparkdev/spark/so/ent"
	"go.uber.org/zap"
)

func GetEntTokenTransactionAttrs(tokenTransaction *ent.TokenTransaction) []zap.Field {
	return []zap.Field{
		zap.Stringer("transaction_uuid", tokenTransaction.ID),
		zap.String("transaction_hash", hex.EncodeToString(tokenTransaction.FinalizedTokenTransactionHash)),
	}
}

func GetFinalizedTokenTransactionAttrs(finalizedTokenTransactionHash []byte) []zap.Field {
	return []zap.Field{
		zap.String("transaction_hash", hex.EncodeToString(finalizedTokenTransactionHash)),
	}
}

func GetPartialTokenTransactionAttrs(partialTokenTransactionHash []byte) []zap.Field {
	return []zap.Field{
		zap.String("partial_transaction_hash", hex.EncodeToString(partialTokenTransactionHash)),
	}
}
