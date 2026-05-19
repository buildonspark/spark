package mimo_test

import (
	"strings"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/mimo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// activeCounterSwapStatuses mirrors the SDK's ACTIVE_COUNTER_SWAP_STATUSES
// — the 9-state set queryCounterSwapTransfers always sends.
func activeCounterSwapStatuses() []st.TransferStatus {
	return []st.TransferStatus{
		st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusApplyingSenderKeyTweak,
		st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusSenderKeyTweaked,
		st.TransferStatusReceiverKeyTweaked,
		st.TransferStatusReceiverKeyTweakLocked,
		st.TransferStatusReceiverKeyTweakApplied,
		st.TransferStatusReceiverRefundSigned,
	}
}

// TestBuildCounterSwapQuery_FullSDKShape locks in the SQL shape for the
// canonical SDK queryCounterSwapTransfers call — sender-or-receiver
// participant + both counter-swap types + all 9 ACTIVE_COUNTER_SWAP_STATUSES.
func TestBuildCounterSwapQuery_FullSDKShape(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.CounterSwapArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types:        []pb.TransferType{pb.TransferType_COUNTER_SWAP, pb.TransferType_COUNTER_SWAP_V3},
		Statuses:     activeCounterSwapStatuses(),
		Limit:        50,
	}
	query, sqlArgs, err := mimo.BuildCounterSwapQuery(args)
	require.NoError(t, err)

	// Sender arm: 9 per-status sub-queries driving sender_identity_pubkey.
	assert.Equal(t, 9, strings.Count(query, "t.sender_identity_pubkey = $1"),
		"expected one per-status sub-query per input status")
	assert.Contains(t, query, "FROM transfers t",
		"sender arm should drive transfers directly (column-based)")
	// Each sender sub-query uses single-status equality (=), not multi-value ANY.
	assert.NotContains(t, query, "t.status = ANY(",
		"sender arm must use per-status equality to avoid multi-value-ANY pathology")

	// Receiver arm: per-type × per-bucket UNION ALL.
	assert.Contains(t, query, "FROM transfer_receivers r")
	assert.Contains(t, query, "INNER JOIN transfers t ON t.id = r.transfer_id")

	// Narrowing redundancy detection: input contains all 4 sender-pending
	// statuses → t.status narrowing is dropped from collapsing sub-queries.
	assert.NotContains(t, query, "t.status = ANY(",
		"narrowing predicate should be dropped when input covers all sender-pending")

	// Cross-arm: sender UNION ALL chain joined to receiver UNION ALL chain
	// via UNION (distinct).
	assert.Contains(t, query, "UNION\n", "cross-arm distinct union must be present")
	assert.Contains(t, query, "LIMIT $2 OFFSET $3", "outer LIMIT/OFFSET must be present")

	// 9 sender + 4 receiver = 13 sub-queries → 11 UNION ALL + 1 UNION.
	assert.Equal(t, 11, strings.Count(query, "UNION ALL"),
		"expected 8 sender UNION ALL + 3 receiver UNION ALL")

	// Args layout: $1 pubkey, $2 limit, $3 offset, $4 perArmLimit, $5 network,
	// 9 sender statuses, types array, pure-postTweakActive bucket,
	// collapsing-remainder bucket, 2 type values = 19 args total
	// (no narrowing array since narrowing is redundant).
	assert.Len(t, sqlArgs, 19)
}

// TestBuildCounterSwapQuery_PartialUmbrellaKeepsNarrowing verifies that a
// partial-umbrella caller (subset of sender-pending statuses) keeps the
// t.status narrowing predicate for correctness, even though it's a slower
// plan. The narrowing-redundancy optimization fires only when ALL 4
// sender-pending values are present.
func TestBuildCounterSwapQuery_PartialUmbrellaKeepsNarrowing(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.CounterSwapArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types:        []pb.TransferType{pb.TransferType_COUNTER_SWAP},
		// Only 1 of the 4 sender-pending statuses — narrowing must NOT be dropped.
		Statuses: []st.TransferStatus{st.TransferStatusSenderInitiated},
		Limit:    50,
	}
	query, _, err := mimo.BuildCounterSwapQuery(args)
	require.NoError(t, err)

	// With a partial subset, the collapsing-remainder sub-query KEEPS the
	// t.status narrowing for correctness (slow but correct plan).
	assert.Contains(t, query, "t.status = ANY(",
		"narrowing must be preserved when not all sender-pending statuses are present")
}

// TestBuildCounterSwapQuery_PartialTerminalKeepsNarrowing covers the
// asymmetric collapse: input has ALL 4 sender-pending values (INITIATED
// prereqs satisfied) AND one of {EXPIRED, RETURNED} but not both
// (CANCELLED prereqs NOT satisfied). Dropping the t.status narrowing
// here would over-match — the CANCELLED bucket would surface RETURNED
// transfers when the caller asked for EXPIRED only. Narrowing must be
// preserved.
func TestBuildCounterSwapQuery_PartialTerminalKeepsNarrowing(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.CounterSwapArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types:        []pb.TransferType{pb.TransferType_COUNTER_SWAP},
		Statuses: append(
			activeCounterSwapStatuses(),
			st.TransferStatusExpired, // EXPIRED only — RETURNED absent
		),
		Limit: 50,
	}
	query, _, err := mimo.BuildCounterSwapQuery(args)
	require.NoError(t, err)

	assert.Contains(t, query, "t.status = ANY(",
		"narrowing must be preserved when CANCELLED collapse target is present but not all of its prereqs (EXPIRED+RETURNED) are in the input")
}

// TestBuildCounterSwapQuery_FullTerminalDropsNarrowing covers the "both
// prereq sets satisfied" case: ALL 4 sender-pending + BOTH terminal
// values. Narrowing for the INITIATED collapse and the CANCELLED collapse
// is redundant; both can be dropped.
func TestBuildCounterSwapQuery_FullTerminalDropsNarrowing(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.CounterSwapArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types:        []pb.TransferType{pb.TransferType_COUNTER_SWAP},
		Statuses: append(
			activeCounterSwapStatuses(),
			st.TransferStatusExpired,
			st.TransferStatusReturned,
		),
		Limit: 50,
	}
	query, _, err := mimo.BuildCounterSwapQuery(args)
	require.NoError(t, err)

	assert.NotContains(t, query, "t.status = ANY(",
		"narrowing should be dropped when all prereqs for every collapsing target are satisfied")
}

// TestBuildCounterSwapQuery_SingleReceiverAxisStatus locks in the shape when
// the caller asks for one receiver-axis-pure status only — receiver arm
// emits a single pure-postTweakActive sub-query per type; the collapsing
// bucket is empty.
func TestBuildCounterSwapQuery_SingleReceiverAxisStatus(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.CounterSwapArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types:        []pb.TransferType{pb.TransferType_COUNTER_SWAP},
		Statuses:     []st.TransferStatus{st.TransferStatusReceiverKeyTweaked},
		Limit:        50,
	}
	query, _, err := mimo.BuildCounterSwapQuery(args)
	require.NoError(t, err)

	// 1 sender per-status sub-query + 1 receiver pure-postTweakActive sub-query
	// (× 1 type) = 2 sub-queries total → 0 UNION ALL within arms + 1 UNION cross-arm.
	assert.Equal(t, 0, strings.Count(query, "UNION ALL"))
	assert.Contains(t, query, "UNION\n", "still have cross-arm UNION DISTINCT")
}

// TestBuildCounterSwapQuery_NetworkRequired guards against silently dropping
// the network filter — would risk cross-network data leakage.
func TestBuildCounterSwapQuery_NetworkRequired(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.CounterSwapArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_UNSPECIFIED,
		Types:        []pb.TransferType{pb.TransferType_COUNTER_SWAP},
		Statuses:     []st.TransferStatus{st.TransferStatusSenderInitiated},
		Limit:        50,
	}
	_, _, err := mimo.BuildCounterSwapQuery(args)
	assert.Error(t, err)
}

// TestBuildCounterSwapQuery_EmptyStatuses validates the routing predicate's
// invariant — non-empty statuses required.
func TestBuildCounterSwapQuery_EmptyStatuses(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.CounterSwapArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types:        []pb.TransferType{pb.TransferType_COUNTER_SWAP},
		Statuses:     nil,
		Limit:        50,
	}
	_, _, err := mimo.BuildCounterSwapQuery(args)
	assert.Error(t, err)
}
