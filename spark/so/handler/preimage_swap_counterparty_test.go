package handler

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// v3 could not get this wrong: one field was both the counterparty and the sole receiver. v4 splits
// them, so these cases pin that the swap's counterparty is actually paid by the transfer it rides.
func TestAssertCounterpartyIsPaid(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{81})
	counterparty := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	other := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	leafA := uuid.New()
	leafB := uuid.New()

	routing := func(t *testing.T, receiverByLeafID map[string]keys.Public) leafDestinations {
		t.Helper()
		destinations, err := perLeafDestinations(receiverByLeafID)
		require.NoError(t, err)
		return destinations
	}

	t.Run("the counterparty's own leaf satisfies it", func(t *testing.T) {
		err := assertCounterpartyIsPaid(t.Context(), counterparty,
			map[string]*ent.TreeNode{leafA.String(): {Value: 1000}},
			routing(t, map[string]keys.Public{leafA.String(): counterparty}),
			pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE)

		require.NoError(t, err)
	})

	t.Run("sharing the transfer with fee receivers is fine", func(t *testing.T) {
		err := assertCounterpartyIsPaid(t.Context(), counterparty,
			map[string]*ent.TreeNode{leafA.String(): {Value: 900}, leafB.String(): {Value: 100}},
			routing(t, map[string]keys.Public{leafA.String(): counterparty, leafB.String(): other}),
			pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE)

		require.NoError(t, err)
	})

	t.Run("a non-canonical spelling of a moved leaf still counts", func(t *testing.T) {
		err := assertCounterpartyIsPaid(t.Context(), counterparty,
			map[string]*ent.TreeNode{leafA.String(): {Value: 1000}},
			routing(t, map[string]keys.Public{strings.ToUpper(leafA.String()): counterparty}),
			pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE)

		require.NoError(t, err)
	})

	// The case the check exists for: a caller names a counterparty and routes the money elsewhere.
	t.Run("a counterparty that receives nothing is refused", func(t *testing.T) {
		err := assertCounterpartyIsPaid(t.Context(), counterparty,
			map[string]*ent.TreeNode{leafA.String(): {Value: 900}, leafB.String(): {Value: 100}},
			routing(t, map[string]keys.Public{leafA.String(): other, leafB.String(): other}),
			pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE)

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "receives no value")
	})

	// A leaf count reads this as paid; the value it carries is what says otherwise.
	t.Run("a counterparty routed only a valueless leaf is refused", func(t *testing.T) {
		err := assertCounterpartyIsPaid(t.Context(), counterparty,
			map[string]*ent.TreeNode{leafA.String(): {Value: 0}, leafB.String(): {Value: 100_000}},
			routing(t, map[string]keys.Public{leafA.String(): counterparty, leafB.String(): other}),
			pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE)

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "receives no value")
	})

	// A routing entry for a leaf outside the transfer is free to write, so it cannot stand in for payment.
	t.Run("a phantom leaf naming the counterparty does not satisfy it", func(t *testing.T) {
		err := assertCounterpartyIsPaid(t.Context(), counterparty,
			map[string]*ent.TreeNode{leafA.String(): {Value: 1000}},
			routing(t, map[string]keys.Public{
				leafA.String():      other,
				uuid.NewString():    counterparty,
				"not-even-a-uuid-1": counterparty,
			}),
			pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE)

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "receives no value")
	})

	t.Run("an empty transfer is refused", func(t *testing.T) {
		err := assertCounterpartyIsPaid(t.Context(), counterparty, map[string]*ent.TreeNode{},
			routing(t, map[string]keys.Public{}), pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE)

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	// An unrouted leaf has no destination to attribute, so it cannot be assumed to pay anyone.
	t.Run("a moved leaf no routing entry names is refused", func(t *testing.T) {
		err := assertCounterpartyIsPaid(t.Context(), counterparty,
			map[string]*ent.TreeNode{leafA.String(): {Value: 1000}, leafB.String(): {Value: 1000}},
			routing(t, map[string]keys.Public{leafA.String(): counterparty}),
			pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE)

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("it holds on send as well as receive", func(t *testing.T) {
		err := assertCounterpartyIsPaid(t.Context(), counterparty,
			map[string]*ent.TreeNode{leafA.String(): {Value: 1000}},
			routing(t, map[string]keys.Public{leafA.String(): other}),
			pbspark.InitiatePreimageSwapRequest_REASON_SEND)

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})
}
