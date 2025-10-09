package tokens

import (
	"context"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/ent"
	"go.uber.org/zap"
)

func GetEntTokenTransactionZapAttrs(ctx context.Context, tokenTransaction *ent.TokenTransaction) []zap.Field {
	attrs := GetTokenTxAttrStringsFromEnt(ctx, tokenTransaction)
	return []zap.Field{
		zap.Stringer("transaction_uuid", tokenTransaction.ID),
		zap.String("transaction_type", attrs.Type),
		zap.String("partial_transaction_hash", attrs.PartialHashHex),
		zap.String("final_transaction_hash", attrs.FinalHashHex),
	}
}

func GetProtoTokenTransactionZapAttrs(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction) []zap.Field {
	attrs := GetTokenTxAttrStringsFromProto(ctx, tokenTransaction)
	return []zap.Field{
		zap.String("transaction_type", attrs.Type),
		zap.String("partial_transaction_hash", attrs.PartialHashHex),
		zap.String("final_transaction_hash", attrs.FinalHashHex),
	}
}
