package task

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

func createRetypeTestTransfer(t *testing.T, ctx context.Context, client *ent.Client, sender, receiver keys.Public, network btcnetwork.Network, tType st.TransferType) *ent.Transfer {
	t.Helper()
	tr, err := client.Transfer.Create().
		SetSenderIdentityPubkey(sender).
		SetReceiverIdentityPubkey(receiver).
		SetNetwork(network).
		SetTotalValue(1000).
		SetStatus(st.TransferStatusCompleted).
		SetType(tType).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	return tr
}

// TestRetypeSSPCompensation exercises the (wallet, SSP, network) tuple match
// added to guard against cross-network and cross-SSP false positives, and
// checks the happy path for both hardcoded SSPs.
func TestRetypeSSPCompensation(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)
	cfg.Index = 0
	cfg.CacheURI = "" // disable memcache so the cursor seeds fresh

	rng := rand.NewChaCha8([32]byte{3})
	newWallet := func() keys.Public {
		return keys.MustGeneratePrivateKeyFromRand(rng).Public()
	}

	mainnetSSP := sspPubkeys[0]
	regtestSSP := sspPubkeys[1]

	type scenario struct {
		name         string
		candSender   keys.Public
		candReceiver keys.Public
		candNetwork  btcnetwork.Network
		hasSwap      bool
		swapSender   keys.Public
		swapReceiver keys.Public
		swapNetwork  btcnetwork.Network
		swapType     st.TransferType
		shouldRetype bool
	}

	w := make([]keys.Public, 7)
	for i := range w {
		w[i] = newWallet()
	}

	scenarios := []scenario{
		{
			name:         "happy_mainnet_swap",
			candSender:   mainnetSSP,
			candReceiver: w[0],
			candNetwork:  btcnetwork.Mainnet,
			hasSwap:      true,
			swapSender:   w[0],
			swapReceiver: mainnetSSP,
			swapNetwork:  btcnetwork.Mainnet,
			swapType:     st.TransferTypeSwap,
			shouldRetype: true,
		},
		{
			name:         "happy_regtest_primary_swap_v3",
			candSender:   regtestSSP,
			candReceiver: w[1],
			candNetwork:  btcnetwork.Regtest,
			hasSwap:      true,
			swapSender:   w[1],
			swapReceiver: regtestSSP,
			swapNetwork:  btcnetwork.Regtest,
			swapType:     st.TransferTypePrimarySwapV3,
			shouldRetype: true,
		},
		{
			name:         "cross_network_mainnet_candidate_regtest_swap",
			candSender:   mainnetSSP,
			candReceiver: w[2],
			candNetwork:  btcnetwork.Mainnet,
			hasSwap:      true,
			swapSender:   w[2],
			swapReceiver: regtestSSP,
			swapNetwork:  btcnetwork.Regtest,
			swapType:     st.TransferTypeSwap,
			shouldRetype: false,
		},
		{
			name:         "cross_ssp_same_network",
			candSender:   mainnetSSP,
			candReceiver: w[3],
			candNetwork:  btcnetwork.Mainnet,
			hasSwap:      true,
			// Synthetic: regtest SSP pubkey on Mainnet network. Exercises the
			// pure SSP-pair check independent of network.
			swapSender:   w[3],
			swapReceiver: regtestSSP,
			swapNetwork:  btcnetwork.Mainnet,
			swapType:     st.TransferTypeSwap,
			shouldRetype: false,
		},
		{
			name:         "no_swap",
			candSender:   mainnetSSP,
			candReceiver: w[4],
			candNetwork:  btcnetwork.Mainnet,
			hasSwap:      false,
			shouldRetype: false,
		},
		{
			name:         "non_ssp_sender",
			candSender:   w[5],
			candReceiver: w[6],
			candNetwork:  btcnetwork.Mainnet,
			hasSwap:      true,
			swapSender:   w[6],
			swapReceiver: mainnetSSP,
			swapNetwork:  btcnetwork.Mainnet,
			swapType:     st.TransferTypeSwap,
			shouldRetype: false,
		},
	}

	transferIDs := make(map[string]uuid.UUID, len(scenarios))
	for _, s := range scenarios {
		cand := createRetypeTestTransfer(t, ctx, client, s.candSender, s.candReceiver, s.candNetwork, st.TransferTypeTransfer)
		transferIDs[s.name] = cand.ID
		if s.hasSwap {
			createRetypeTestTransfer(t, ctx, client, s.swapSender, s.swapReceiver, s.swapNetwork, s.swapType)
		}
	}

	// Already-retyped row: ensure it stays COUNTER_SWAP and doesn't count toward retype total.
	alreadyReceiver := newWallet()
	alreadyRetyped := createRetypeTestTransfer(t, ctx, client, mainnetSSP, alreadyReceiver, btcnetwork.Mainnet, st.TransferTypeCounterSwap)
	createRetypeTestTransfer(t, ctx, client, alreadyReceiver, mainnetSSP, btcnetwork.Mainnet, st.TransferTypeSwap)

	updated, err := retypeSSPCompensationTransfers(ctx, cfg, client, retypeSSPCompensationDefaultBatchSize)
	require.NoError(t, err)

	expected := 0
	for _, s := range scenarios {
		if s.shouldRetype {
			expected++
		}
	}
	require.Equal(t, expected, updated, "unexpected retype count")

	for _, s := range scenarios {
		got, err := client.Transfer.Get(ctx, transferIDs[s.name])
		require.NoError(t, err)
		if s.shouldRetype {
			require.Equalf(t, st.TransferTypeCounterSwap, got.Type, "scenario %q: expected retype to COUNTER_SWAP", s.name)
		} else {
			require.Equalf(t, st.TransferTypeTransfer, got.Type, "scenario %q: expected row to remain TRANSFER", s.name)
		}
	}

	got, err := client.Transfer.Get(ctx, alreadyRetyped.ID)
	require.NoError(t, err)
	require.Equal(t, st.TransferTypeCounterSwap, got.Type)
}
