package tokens

import (
	"context"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/tokens"
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

func GetTracer() trace.Tracer {
	return tracer
}

func GetProtoTokenTransactionTraceAttributes(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction) trace.SpanStartEventOption {
	return buildTraceAttributes(tokens.GetTokenTxAttrStringsFromProto(ctx, tokenTransaction))
}

func GetEntTokenTransactionTraceAttributes(ctx context.Context, tokenTransaction *ent.TokenTransaction) trace.SpanStartEventOption {
	return buildTraceAttributes(tokens.GetTokenTxAttrStringsFromEnt(ctx, tokenTransaction))
}

func buildTraceAttributes(attrs tokens.TokenTransactionAttributes) trace.SpanStartEventOption {
	transactionTypeAttribute := transactionTypeKey.String(attrs.Type)
	transactionPartialHashAttribute := transactionPartialHashKey.String(attrs.PartialHashHex)
	transactionFullHashAttribute := transactionFullHashKey.String(attrs.FinalHashHex)
	return trace.WithAttributes(
		transactionTypeAttribute,
		transactionPartialHashAttribute,
		transactionFullHashAttribute,
	)
}
