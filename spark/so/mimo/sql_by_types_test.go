package mimo_test

import (
	"strings"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/mimo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildByTypesQuerySender_DrivesEdgeComposite(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.ByTypesArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types:        []pb.TransferType{pb.TransferType_TRANSFER, pb.TransferType_PREIMAGE_SWAP},
		Limit:        100,
		Offset:       0,
	}
	query, sqlArgs, err := mimo.BuildByTypesQuerySender(args)
	require.NoError(t, err)

	assert.Contains(t, query, "FROM transfer_senders s")
	assert.Contains(t, query, "INNER JOIN transfers t ON t.id = s.transfer_id")
	assert.Contains(t, query, "s.identity_pubkey = $1")
	// Each per-type sub-query has its own single-value type equality.
	assert.Contains(t, query, "s.transfer_type = $6")
	assert.Contains(t, query, "s.transfer_type = $7")
	// Multi-value ANY on transfer_type would defeat top-N pushdown on the composite.
	assert.NotContains(t, query, "transfer_type = ANY(")
	assert.Contains(t, query, "t.network = $5")
	// Per-arm LIMIT $4 inside each sub-query; outer LIMIT/OFFSET on $2/$3.
	assert.Contains(t, query, "LIMIT $4)")
	assert.Contains(t, query, "LIMIT $2 OFFSET $3")
	// N-1 UNION ALL separators for N sub-queries.
	assert.Equal(t, 1, strings.Count(query, "UNION ALL"))
	assert.Contains(t, query, "ORDER BY s.create_time DESC, s.transfer_id DESC")
	assert.Contains(t, query, "ORDER BY ct DESC, id DESC")

	// pubkey, Limit, Offset, perArmLimit, Network, 2 types.
	assert.Len(t, sqlArgs, 7)
}

func TestBuildByTypesQueryReceiver_DrivesEdgeComposite(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.ByTypesArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types:        []pb.TransferType{pb.TransferType_COOPERATIVE_EXIT},
		Limit:        50,
		Offset:       10,
	}
	query, sqlArgs, err := mimo.BuildByTypesQueryReceiver(args)
	require.NoError(t, err)

	assert.Contains(t, query, "FROM transfer_receivers r")
	assert.Contains(t, query, "r.identity_pubkey = $1")
	assert.Contains(t, query, "r.transfer_type = $6")
	assert.NotContains(t, query, "transfer_type = ANY(")
	assert.Contains(t, query, "ORDER BY r.create_time DESC, r.transfer_id DESC")
	assert.Contains(t, query, "ORDER BY ct DESC, id DESC")
	// Single type → no UNION ALL between sub-queries.
	assert.NotContains(t, query, "UNION ALL")

	// pubkey, Limit, Offset, perArmLimit, Network, 1 type.
	assert.Len(t, sqlArgs, 6)
}

func TestBuildByTypesQuerySenderOrReceiver_PerArmUnionAllPlusCrossArmDedup(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.ByTypesArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_REGTEST,
		Types: []pb.TransferType{
			pb.TransferType_TRANSFER,
			pb.TransferType_PREIMAGE_SWAP,
			pb.TransferType_COOPERATIVE_EXIT,
			pb.TransferType_UTXO_SWAP,
		},
		Limit:  100,
		Offset: 0,
	}
	query, _, err := mimo.BuildByTypesQuerySenderOrReceiver(args)
	require.NoError(t, err)

	assert.Contains(t, query, "FROM transfer_senders s")
	assert.Contains(t, query, "FROM transfer_receivers r")
	// 4 types per arm → 3 UNION ALL separators per arm → 6 total.
	assert.Equal(t, 6, strings.Count(query, "UNION ALL"))
	// Exactly one cross-arm UNION (distinct) for self-transfer dedup.
	assert.Equal(t, 1, strings.Count(query, "UNION\n"))
	assert.Contains(t, query, "ORDER BY s.create_time DESC, s.transfer_id DESC")
	assert.Contains(t, query, "ORDER BY r.create_time DESC, r.transfer_id DESC")
	assert.Contains(t, query, "ORDER BY ct DESC, id DESC")
}

func TestBuildByTypesQuery_AscendingOrder(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.ByTypesArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types:        []pb.TransferType{pb.TransferType_TRANSFER},
		Order:        pb.Order_ASCENDING,
		Limit:        100,
	}
	q, _, err := mimo.BuildByTypesQuerySender(args)
	require.NoError(t, err)
	assert.Contains(t, q, "ORDER BY s.create_time ASC, s.transfer_id ASC")
	assert.Contains(t, q, "ORDER BY ct ASC, id ASC")

	q, _, err = mimo.BuildByTypesQueryReceiver(args)
	require.NoError(t, err)
	assert.Contains(t, q, "ORDER BY r.create_time ASC, r.transfer_id ASC")
	assert.Contains(t, q, "ORDER BY ct ASC, id ASC")

	q, _, err = mimo.BuildByTypesQuerySenderOrReceiver(args)
	require.NoError(t, err)
	assert.Contains(t, q, "ORDER BY ct ASC, id ASC")
}

func TestBuildByTypesQuery_DedupesRepeatedTypes(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.ByTypesArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Types: []pb.TransferType{
			pb.TransferType_TRANSFER,
			pb.TransferType_TRANSFER,
			pb.TransferType_PREIMAGE_SWAP,
			pb.TransferType_TRANSFER,
		},
		Limit: 100,
	}
	query, sqlArgs, err := mimo.BuildByTypesQuerySender(args)
	require.NoError(t, err)

	// One sub-query per unique type, not per input occurrence.
	assert.Equal(t, 1, strings.Count(query, "s.transfer_type = $6"))
	assert.Equal(t, 1, strings.Count(query, "s.transfer_type = $7"))
	assert.NotContains(t, query, "s.transfer_type = $8")
	// pubkey, Limit, Offset, perArmLimit, Network, 2 unique types.
	assert.Len(t, sqlArgs, 7)
}

func TestBuildByTypesQuery_RequiresTypes(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.ByTypesArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Limit:        100,
	}
	_, _, err := mimo.BuildByTypesQuerySender(args)
	require.Error(t, err)

	_, _, err = mimo.BuildByTypesQueryReceiver(args)
	require.Error(t, err)

	_, _, err = mimo.BuildByTypesQuerySenderOrReceiver(args)
	require.Error(t, err)
}
