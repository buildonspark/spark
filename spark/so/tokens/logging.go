package tokens

import (
	"context"
	"encoding/hex"
	"log/slog"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
)

func LogWithTransactionEnt(ctx context.Context, msg string, tokenTransaction *ent.TokenTransaction, level slog.Level) {
	logger := logging.GetLoggerFromContext(ctx)

	attrs := []any{
		"transaction_uuid", tokenTransaction.ID.String(),
		"transaction_hash", hex.EncodeToString(tokenTransaction.FinalizedTokenTransactionHash),
	}

	logger.Log(ctx, level, msg, attrs...)
}
