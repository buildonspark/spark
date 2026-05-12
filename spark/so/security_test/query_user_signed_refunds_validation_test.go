package securitytest

import (
	"bytes"
	"fmt"
	"testing"

	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestQueryUserSignedRefunds_MalformedPaymentHashRejected(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)

	conn, err := config.NewCoordinatorGRPCConnection()
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)
	client := sparkpb.NewSparkServiceClient(conn)

	for _, hashLen := range []int{0, 1, 31, 33, 64} {
		t.Run(fmt.Sprintf("len_%d", hashLen), func(t *testing.T) {
			_, err := client.QueryUserSignedRefunds(ctx, &sparkpb.QueryUserSignedRefundsRequest{
				PaymentHash:       bytes.Repeat([]byte{0x42}, hashLen),
				IdentityPublicKey: config.IdentityPublicKey().Serialize(),
			})
			require.Error(t, err)

			st, ok := status.FromError(err)
			require.True(t, ok, "expected gRPC status error, got %T: %v", err, err)
			require.Equal(t, codes.InvalidArgument, st.Code())
		})
	}
}
