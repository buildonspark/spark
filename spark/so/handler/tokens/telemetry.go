package tokens

import (
	"context"
	"encoding/hex"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/utils"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("handler.tokens")

const (
	transactionTypeKey        = attribute.Key("token_transaction_type")
	transactionPartialHashKey = attribute.Key("token_transaction_partial_hash")
	transactionFullHashKey    = attribute.Key("token_transaction_full_hash")
)

func getTokenTransactionAttributes(tokenTransaction *tokenpb.TokenTransaction) trace.SpanStartEventOption {
	transactionType, err := utils.InferTokenTransactionType(tokenTransaction)
	var transactionTypeAttribute, transactionPartialHashAttribute, transactionFullHashAttribute attribute.KeyValue
	if err != nil {
		transactionTypeAttribute = transactionTypeKey.String("unknown")
	} else {
		transactionTypeAttribute = transactionTypeKey.String(transactionType.String())
	}
	partialTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		transactionPartialHashAttribute = transactionPartialHashKey.String("unknown")
	} else {
		transactionPartialHashAttribute = transactionPartialHashKey.String(hex.EncodeToString(partialTransactionHash))
	}
	fullTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, false)
	if err != nil {
		transactionFullHashAttribute = transactionFullHashKey.String("unknown")
	} else {
		transactionFullHashAttribute = transactionFullHashKey.String(hex.EncodeToString(fullTransactionHash))
	}
	return trace.WithAttributes(
		transactionTypeAttribute,
		transactionPartialHashAttribute,
		transactionFullHashAttribute,
	)
}

func getTokenTransactionAttributesFromEnt(ctx context.Context, tokenTransaction *ent.TokenTransaction, config *so.Config) trace.SpanStartEventOption {
	tokenProto, err := tokenTransaction.MarshalProto(ctx, config)
	if err != nil {
		return trace.WithAttributes(transactionTypeKey.String("unknown"))
	}
	return getTokenTransactionAttributes(tokenProto)
}
