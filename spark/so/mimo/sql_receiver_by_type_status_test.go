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

func TestBuildReceiverByTypeStatusQuery_FullCounterSwapActive(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.ReceiverByTypeStatusArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types:        []pb.TransferType{pb.TransferType_COUNTER_SWAP_V3, pb.TransferType_COUNTER_SWAP},
		Statuses: []st.TransferStatus{
			st.TransferStatusSenderInitiated,
			st.TransferStatusSenderInitiatedCoordinator,
			st.TransferStatusApplyingSenderKeyTweak,
			st.TransferStatusSenderKeyTweakPending,
			st.TransferStatusSenderKeyTweaked,
			st.TransferStatusReceiverKeyTweakLocked,
			st.TransferStatusReceiverKeyTweakApplied,
			st.TransferStatusReceiverKeyTweaked,
			st.TransferStatusReceiverRefundSigned,
		},
		Limit: 50,
	}
	query, sqlArgs, err := mimo.BuildReceiverByTypeStatusQuery(args)
	require.NoError(t, err)

	assert.Contains(t, query, "FROM transfer_receivers r")
	assert.Contains(t, query, "INNER JOIN transfers t ON t.id = r.transfer_id")
	assert.Contains(t, query, "r.identity_pubkey = $1")
	assert.Contains(t, query, "r.status = ANY(")
	assert.Contains(t, query, "t.status = ANY(")
	assert.NotContains(t, query, "transfer_type = ANY(")
	// No OR-of-two-tables predicate — that pattern tipped the planner to
	// Parallel Bitmap Heap Scan with lossy heap recheck on heavy fixtures.
	assert.NotContains(t, query, "OR t.status")
	assert.Contains(t, query, "LIMIT $2 OFFSET $3")
	// 2 types × (pure postTweakActive + collapsing remainder) = 4 sub-queries → 3 UNION ALL.
	assert.Equal(t, 3, strings.Count(query, "UNION ALL"))
	assert.Equal(t, 4, strings.Count(query, "r.transfer_type ="))

	// Args: pubkey, Limit, Offset, perArmLimit, network, narrowing, purePostTweak, collapsingRemainder, type1, type2.
	assert.Len(t, sqlArgs, 10)
}

func TestBuildReceiverByTypeStatusQuery_ShapeAPureReceiverNamed(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.ReceiverByTypeStatusArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types:        []pb.TransferType{pb.TransferType_SWAP},
		Statuses: []st.TransferStatus{
			st.TransferStatusSenderKeyTweaked,
			st.TransferStatusReceiverKeyTweaked,
			st.TransferStatusReceiverKeyTweakLocked,
			st.TransferStatusReceiverKeyTweakApplied,
			st.TransferStatusReceiverRefundSigned,
		},
		Limit: 50,
	}
	query, sqlArgs, err := mimo.BuildReceiverByTypeStatusQuery(args)
	require.NoError(t, err)

	// All 5 inputs are pure 1:1 → purePostTweakActive only. No collapsing
	// inputs → no narrowing predicate at all.
	assert.NotContains(t, query, "UNION ALL")
	assert.NotContains(t, query, "t.status = ANY(")
	assert.Equal(t, 1, strings.Count(query, "r.transfer_type ="))

	// pubkey, Limit, Offset, perArmLimit, network, purePostTweak, 1 type.
	assert.Len(t, sqlArgs, 7)
}

func TestBuildReceiverByTypeStatusQuery_ShapeBSenderPendingOnly(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.ReceiverByTypeStatusArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types:        []pb.TransferType{pb.TransferType_COOPERATIVE_EXIT},
		Statuses: []st.TransferStatus{
			st.TransferStatusSenderInitiated,
			st.TransferStatusSenderKeyTweakPending,
		},
		Limit: 50,
	}
	query, sqlArgs, err := mimo.BuildReceiverByTypeStatusQuery(args)
	require.NoError(t, err)

	// Only INITIATED in rIndexSet (collapsing target) → collapsingRemainder only,
	// no pure sub-queries. 1 type × 1 bucket = 1 sub-query. t.status narrowing
	// IS present because the input is partial-umbrella.
	assert.NotContains(t, query, "UNION ALL")
	assert.Contains(t, query, "t.status = ANY(")
	assert.Equal(t, 1, strings.Count(query, "r.transfer_type ="))

	// pubkey, Limit, Offset, perArmLimit, network, narrowing, collapsingRemainder, 1 type.
	assert.Len(t, sqlArgs, 8)
}

func TestBuildReceiverByTypeStatusQuery_TerminalCompletedOnly(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.ReceiverByTypeStatusArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types:        []pb.TransferType{pb.TransferType_TRANSFER},
		Statuses:     []st.TransferStatus{st.TransferStatusCompleted},
		Limit:        50,
	}
	query, _, err := mimo.BuildReceiverByTypeStatusQuery(args)
	require.NoError(t, err)

	// COMPLETED is outside idx_transferreceiver_claim_pending_pubkey_time's
	// partial WHERE → remainder bucket only.
	assert.Contains(t, query, "r.transfer_type =")
	assert.Equal(t, 1, strings.Count(query, "r.transfer_type ="))
}

func TestBuildReceiverByTypeStatusQuery_DedupesDuplicateTypes(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.ReceiverByTypeStatusArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types: []pb.TransferType{
			pb.TransferType_COUNTER_SWAP,
			pb.TransferType_COUNTER_SWAP,
			pb.TransferType_COUNTER_SWAP_V3,
		},
		Statuses: []st.TransferStatus{st.TransferStatusReceiverKeyTweaked},
		Limit:    50,
	}
	query, _, err := mimo.BuildReceiverByTypeStatusQuery(args)
	require.NoError(t, err)

	// 2 distinct types × 1 bucket = 2 sub-queries → 1 UNION ALL.
	assert.Equal(t, 1, strings.Count(query, "UNION ALL"))
	assert.Equal(t, 2, strings.Count(query, "r.transfer_type ="))
}

func TestBuildReceiverByTypeStatusQuery_RejectsEmptyStatuses(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.ReceiverByTypeStatusArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types:        []pb.TransferType{pb.TransferType_TRANSFER},
		Limit:        50,
	}
	_, _, err := mimo.BuildReceiverByTypeStatusQuery(args)
	assert.Error(t, err)
}

func TestBuildReceiverByTypeStatusQuery_RejectsEmptyTypes(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.ReceiverByTypeStatusArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Statuses:     []st.TransferStatus{st.TransferStatusReceiverKeyTweaked},
		Limit:        50,
	}
	_, _, err := mimo.BuildReceiverByTypeStatusQuery(args)
	assert.Error(t, err)
}

func TestBuildReceiverByTypeStatusQuery_RejectsUnspecifiedNetwork(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.ReceiverByTypeStatusArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_UNSPECIFIED,
		Types:        []pb.TransferType{pb.TransferType_TRANSFER},
		Statuses:     []st.TransferStatus{st.TransferStatusReceiverKeyTweaked},
		Limit:        50,
	}
	_, _, err := mimo.BuildReceiverByTypeStatusQuery(args)
	assert.Error(t, err)
}
