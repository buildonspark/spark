package tokens

import (
	"testing"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestQueryTokenMetadataRejectsFilterResourceExhaustionBeforeDB(t *testing.T) {
	handler := NewQueryTokenMetadataHandler(nil)

	tests := []struct {
		name string
		req  *tokenpb.QueryTokenMetadataRequest
		want string
	}{
		{
			name: "token identifiers over limit",
			req: &tokenpb.QueryTokenMetadataRequest{
				TokenIdentifiers: make([][]byte, MaxTokenMetadataFilterValues+1),
			},
			want: "too many token identifiers in filter",
		},
		{
			name: "issuer public keys over limit",
			req: &tokenpb.QueryTokenMetadataRequest{
				IssuerPublicKeys: make([][]byte, MaxTokenMetadataFilterValues+1),
			},
			want: "too many issuer public keys in filter",
		},
		{
			name: "missing filters",
			req:  &tokenpb.QueryTokenMetadataRequest{},
			want: "must provide at least one token identifier or issuer public key",
		},
		{
			name: "nil request",
			req:  nil,
			want: "request is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resp, err := handler.QueryTokenMetadata(t.Context(), test.req)

			require.Nil(t, resp)
			require.Error(t, err)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestValidateQueryTokenMetadataRequestAcceptsMaxFilterValues(t *testing.T) {
	req := &tokenpb.QueryTokenMetadataRequest{
		TokenIdentifiers: make([][]byte, MaxTokenMetadataFilterValues),
		IssuerPublicKeys: make([][]byte, MaxTokenMetadataFilterValues),
	}

	require.NoError(t, validateQueryTokenMetadataRequest(req))
}
