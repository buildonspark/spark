package tokens

import (
	"testing"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/stretchr/testify/require"
)

func TestValidateQueryTokenTransactionsRequest_FilterLimits(t *testing.T) {
	t.Run("output ids over limit", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			OutputIds: make([]string, maxTokenTransactionFilterValues+1),
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.Error(t, err)
		require.ErrorContains(t, err, "too many output ids in filter")
	})

	t.Run("owner public keys over limit", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			OwnerPublicKeys: make([][]byte, maxTokenTransactionFilterValues+1),
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.Error(t, err)
		require.ErrorContains(t, err, "too many owner public keys in filter")
	})

	t.Run("issuer public keys over limit", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			IssuerPublicKeys: make([][]byte, maxTokenTransactionFilterValues+1),
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.Error(t, err)
		require.ErrorContains(t, err, "too many issuer public keys in filter")
	})

	t.Run("token identifiers over limit", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			TokenIdentifiers: make([][]byte, maxTokenTransactionFilterValues+1),
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.Error(t, err)
		require.ErrorContains(t, err, "too many token identifiers in filter")
	})

	t.Run("token transaction hashes over limit", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			TokenTransactionHashes: make([][]byte, maxTokenTransactionFilterValues+1),
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.Error(t, err)
		require.ErrorContains(t, err, "too many token transaction hashes in filter")
	})

	t.Run("by_tx_hash over limit", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			QueryType: &tokenpb.QueryTokenTransactionsRequest_ByTxHash{
				ByTxHash: &tokenpb.QueryTokenTransactionsByTxHash{
					TokenTransactionHashes: make([][]byte, maxTokenTransactionFilterValues+1),
				},
			},
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.Error(t, err)
		require.ErrorContains(t, err, "too many token transaction hashes in filter")
	})

	t.Run("by_filters output ids over limit", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			QueryType: &tokenpb.QueryTokenTransactionsRequest_ByFilters{
				ByFilters: &tokenpb.QueryTokenTransactionsByFilters{
					OutputIds: make([]string, maxTokenTransactionFilterValues+1),
				},
			},
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.Error(t, err)
		require.ErrorContains(t, err, "too many output ids in filter")
	})

	t.Run("by_filters owner public keys over limit", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			QueryType: &tokenpb.QueryTokenTransactionsRequest_ByFilters{
				ByFilters: &tokenpb.QueryTokenTransactionsByFilters{
					OwnerPublicKeys: make([][]byte, maxTokenTransactionFilterValues+1),
				},
			},
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.Error(t, err)
		require.ErrorContains(t, err, "too many owner public keys in filter")
	})

	t.Run("by_filters issuer public keys over limit", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			QueryType: &tokenpb.QueryTokenTransactionsRequest_ByFilters{
				ByFilters: &tokenpb.QueryTokenTransactionsByFilters{
					IssuerPublicKeys: make([][]byte, maxTokenTransactionFilterValues+1),
				},
			},
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.Error(t, err)
		require.ErrorContains(t, err, "too many issuer public keys in filter")
	})

	t.Run("by_filters token identifiers over limit", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			QueryType: &tokenpb.QueryTokenTransactionsRequest_ByFilters{
				ByFilters: &tokenpb.QueryTokenTransactionsByFilters{
					TokenIdentifiers: make([][]byte, maxTokenTransactionFilterValues+1),
				},
			},
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.Error(t, err)
		require.ErrorContains(t, err, "too many token identifiers in filter")
	})

	t.Run("within limits succeeds", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			OutputIds:              make([]string, maxTokenTransactionFilterValues),
			OwnerPublicKeys:        make([][]byte, maxTokenTransactionFilterValues),
			IssuerPublicKeys:       make([][]byte, maxTokenTransactionFilterValues),
			TokenIdentifiers:       make([][]byte, maxTokenTransactionFilterValues),
			TokenTransactionHashes: make([][]byte, maxTokenTransactionFilterValues),
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.NoError(t, err)
	})

	t.Run("by_tx_hash within limits succeeds", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			QueryType: &tokenpb.QueryTokenTransactionsRequest_ByTxHash{
				ByTxHash: &tokenpb.QueryTokenTransactionsByTxHash{
					TokenTransactionHashes: make([][]byte, maxTokenTransactionHashValues),
				},
			},
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.NoError(t, err)
	})

	t.Run("by_filters within limits succeeds", func(t *testing.T) {
		req := &tokenpb.QueryTokenTransactionsRequest{
			QueryType: &tokenpb.QueryTokenTransactionsRequest_ByFilters{
				ByFilters: &tokenpb.QueryTokenTransactionsByFilters{
					OutputIds:        make([]string, maxTokenTransactionFilterValues),
					OwnerPublicKeys:  make([][]byte, maxTokenTransactionFilterValues),
					IssuerPublicKeys: make([][]byte, maxTokenTransactionFilterValues),
					TokenIdentifiers: make([][]byte, maxTokenTransactionFilterValues),
				},
			},
		}

		err := validateQueryTokenTransactionsRequest(req)
		require.NoError(t, err)
	})
}
