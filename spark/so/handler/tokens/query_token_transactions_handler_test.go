package tokens

import (
	"testing"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/stretchr/testify/require"
)

func TestValidateQueryTokenTransactionsRequest_FilterLimits(t *testing.T) {
	t.Run("output ids over limit", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			OutputIds: make([]string, MaxTokenTransactionFilterValues+1),
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.Error(t, err)
		require.ErrorContains(t, err, "too many output ids in filter")
	})

	t.Run("owner public keys over limit", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			OwnerPublicKeys: make([][]byte, MaxTokenTransactionFilterValues+1),
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.Error(t, err)
		require.ErrorContains(t, err, "too many owner public keys in filter")
	})

	t.Run("issuer public keys over limit", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			IssuerPublicKeys: make([][]byte, MaxTokenTransactionFilterValues+1),
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.Error(t, err)
		require.ErrorContains(t, err, "too many issuer public keys in filter")
	})

	t.Run("token identifiers over limit", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			TokenIdentifiers: make([][]byte, MaxTokenTransactionFilterValues+1),
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.Error(t, err)
		require.ErrorContains(t, err, "too many token identifiers in filter")
	})

	t.Run("token transaction hashes over limit", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			TokenTransactionHashes: make([][]byte, MaxTokenTransactionFilterValues+1),
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.Error(t, err)
		require.ErrorContains(t, err, "too many token transaction hashes in filter")
	})

	t.Run("within limits succeeds", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			OutputIds:              make([]string, MaxTokenTransactionFilterValues),
			OwnerPublicKeys:        make([][]byte, MaxTokenTransactionFilterValues),
			IssuerPublicKeys:       make([][]byte, MaxTokenTransactionFilterValues),
			TokenIdentifiers:       make([][]byte, MaxTokenTransactionFilterValues),
			TokenTransactionHashes: make([][]byte, MaxTokenTransactionFilterValues),
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.NoError(t, err)
	})
}
