package handler

import (
	"bytes"
	"testing"

	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/stretchr/testify/require"

	pb "github.com/lightsparkdev/spark/proto/spark"
)

func TestQueryUserSignedRefunds_RejectsMalformedPaymentHashLength(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	handler := NewLightningHandler(&so.Config{})
	_, err := handler.QueryUserSignedRefunds(ctx, &pb.QueryUserSignedRefundsRequest{
		PaymentHash: bytes.Repeat([]byte{0x42}, 31),
	})
	require.ErrorContains(t, err, "payment hash must be 32 bytes")
}
