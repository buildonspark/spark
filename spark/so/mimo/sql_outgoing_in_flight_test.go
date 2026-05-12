package mimo_test

import (
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/mimo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildOutgoingInFlightQuery_RequiresStatuses(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.OutgoingInFlightArgs{
		WalletPubkey: pk.Public(),
		Network:      pb.Network_MAINNET,
		Limit:        100,
	}
	_, _, err := mimo.BuildOutgoingInFlightQuery(args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one status")
}

func TestBuildOutgoingInFlightQuery_MinimalShape(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.OutgoingInFlightArgs{
		WalletPubkey: pk.Public(),
		Statuses:     []st.TransferStatus{st.TransferStatusSenderInitiated},
		Network:      pb.Network_MAINNET,
		Limit:        100,
		Offset:       0,
	}
	query, sqlArgs, err := mimo.BuildOutgoingInFlightQuery(args)
	require.NoError(t, err)

	assert.Contains(t, query, "FROM transfers t")
	assert.Contains(t, query, "t.sender_identity_pubkey = $1")
	assert.Contains(t, query, "t.status = ANY($2::text[])")
	assert.Contains(t, query, "t.network = $5")
	assert.Contains(t, query, "ORDER BY t.create_time DESC, t.id DESC")
	assert.Contains(t, query, "LIMIT $3 OFFSET $4")

	// $1 pubkey, $2 statuses, $3 limit, $4 offset, $5 network
	assert.Len(t, sqlArgs, 5)
}

func TestBuildOutgoingInFlightQuery_AscendingOrder(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.OutgoingInFlightArgs{
		WalletPubkey: pk.Public(),
		Statuses:     []st.TransferStatus{st.TransferStatusSenderInitiated},
		Network:      pb.Network_MAINNET,
		Order:        pb.Order_ASCENDING,
		Limit:        100,
	}
	query, _, err := mimo.BuildOutgoingInFlightQuery(args)
	require.NoError(t, err)
	assert.Contains(t, query, "ORDER BY t.create_time ASC, t.id ASC")
}

func TestBuildOutgoingInFlightQuery_WithTypesAndTimeBounds(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	args := mimo.OutgoingInFlightArgs{
		WalletPubkey: pk.Public(),
		Statuses: []st.TransferStatus{
			st.TransferStatusSenderInitiated,
			st.TransferStatusSenderKeyTweakPending,
		},
		Network:         pb.Network_MAINNET,
		Types:           []pb.TransferType{pb.TransferType_TRANSFER},
		HasCreatedAfter: true,
		Limit:           50,
		Offset:          200,
	}
	query, sqlArgs, err := mimo.BuildOutgoingInFlightQuery(args)
	require.NoError(t, err)

	assert.Contains(t, query, "t.type = ANY")
	assert.Contains(t, query, "t.create_time >")
	// $1 pubkey, $2 statuses, $3 limit, $4 offset, $5 network, $6 types, $7 created_after
	assert.Len(t, sqlArgs, 7)
}
