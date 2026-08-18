package grpctest

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/bolt11"
	"github.com/lightsparkdev/spark/common/keys"
	spark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	transferent "github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/transferreceiver"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func enableInitiatePreimageSwapV4Knobs(t *testing.T, kc *sparktesting.KnobController) {
	t.Helper()
	require.NoError(t, kc.SetKnob(t, knobs.KnobInitiatePreimageSwapV4Enabled, 1))
}

func transferReceiverStatus(t *testing.T, client *ent.Client, transferID uuid.UUID, receiver keys.Public) st.TransferReceiverStatus {
	t.Helper()
	row, err := client.TransferReceiver.Query().
		Where(
			transferreceiver.TransferIDEQ(transferID),
			transferreceiver.IdentityPubkeyEQ(receiver),
		).Only(t.Context())
	require.NoError(t, err)
	return row.Status
}

func transferReceiverStatusesByPubKey(t *testing.T, client *ent.Client, transferID uuid.UUID) map[keys.Public]st.TransferReceiverStatus {
	t.Helper()
	rows, err := client.TransferReceiver.Query().
		Where(transferreceiver.TransferIDEQ(transferID)).
		All(t.Context())
	require.NoError(t, err)
	statuses := make(map[keys.Public]st.TransferReceiverStatus, len(rows))
	for _, row := range rows {
		statuses[row.IdentityPubkey] = row.Status
	}
	return statuses
}

type receiveLeg struct {
	name string
	sats uint64
	// Routes this leg back to the SSP's own identity key, making the SSP simultaneously the
	// transfer's sole sender and one of its receivers.
	selfSend bool

	config            *wallet.TestWalletConfig
	leaf              *spark.TreeNode
	newSigningPrivKey keys.Private
}

func (l *receiveLeg) receiver() keys.Public { return l.config.IdentityPublicKey() }

// v4Receive is the shared fixture: preimage shares distributed across the SOs make the swap
// non-HODL, and legs[0] is the counterparty, who must be the one owning the invoice.
type v4Receive struct {
	sspConfig   *wallet.TestWalletConfig
	userConfig  *wallet.TestWalletConfig
	userPrivKey keys.Private
	preimage    [32]byte
	paymentHash [32]byte
	invoice     string
	invoiceSats uint64
	legs        []*receiveLeg
	leaves      []wallet.LeafKeyTweak
}

// The fake invoice creator returns one fixed bolt11 for every non-zero amount, so this is the only
// amount a non-zero-amount fixture can claim. The SO enforces the stored invoice, not the request.
const fakeInvoiceSats = 12_345

func newV4Receive(t *testing.T) *v4Receive {
	t.Helper()
	return newV4ReceiveWithLegs(t, fakeInvoiceSats, []*receiveLeg{
		{name: "counterparty", sats: 12345},
		{name: "fee recipient", sats: 2345},
	})
}

// The leaves must cover invoiceSats: the SO enforces the amount from the stored bolt11 rather than
// the request, so the invoice, not the caller, decides what the routing has to add up to.
func newV4ReceiveWithLegs(t *testing.T, invoiceSats uint64, legs []*receiveLeg) *v4Receive {
	t.Helper()
	require.NotEmpty(t, legs, "a receive needs at least the counterparty leg")

	sspConfig := wallet.NewTestWalletConfig(t)
	userPrivKey := keys.GeneratePrivateKey()
	userConfig := wallet.NewTestWalletConfigWithIdentityKey(t, userPrivKey)

	preimage, paymentHash := testPreimageHash(t, invoiceSats)
	invoice, err := wallet.CreateLightningInvoiceWithPreimage(
		t.Context(), userConfig, NewFakeLightningInvoiceCreator(), invoiceSats, "test", preimage)
	require.NoError(t, err)
	require.NotEmpty(t, invoice)

	// The creator ignores the requested amount, so without this a fixture can claim a gross the SO
	// never enforces and any floor the leaves were sized against silently stops binding.
	parsedInvoice, err := bolt11.Parse(invoice, paymentHash[:])
	require.NoError(t, err)
	require.Equal(t, int64(invoiceSats), parsedInvoice.MilliSatoshi()/1000,
		"the fixture invoice must actually carry invoiceSats, or the amount floor is untested")

	leaves := make([]wallet.LeafKeyTweak, 0, len(legs))
	for i, leg := range legs {
		switch {
		case i == 0:
			leg.config = userConfig
		case leg.selfSend:
			leg.config = sspConfig
		default:
			leg.config = wallet.NewTestWalletConfigWithIdentityKey(t, keys.GeneratePrivateKey())
		}

		leafPrivKey := keys.GeneratePrivateKey()
		leaf, err := wallet.CreateNewTree(sspConfig, faucet, leafPrivKey, int64(leg.sats))
		require.NoError(t, err, "unable to fund the %s leaf", leg.name)
		leg.leaf = leaf
		leg.newSigningPrivKey = keys.GeneratePrivateKey()
		leaves = append(leaves, wallet.LeafKeyTweak{
			Leaf: leaf, SigningPrivKey: leafPrivKey, NewSigningPrivKey: leg.newSigningPrivKey,
		})
	}

	return &v4Receive{
		sspConfig:   sspConfig,
		userConfig:  userConfig,
		userPrivKey: userPrivKey,
		preimage:    preimage,
		paymentHash: paymentHash,
		invoice:     invoice,
		invoiceSats: invoiceSats,
		legs:        legs,
		leaves:      leaves,
	}
}

func (f *v4Receive) leg(t *testing.T, name string) *receiveLeg {
	t.Helper()
	for _, leg := range f.legs {
		if leg.name == name {
			return leg
		}
	}
	require.FailNowf(t, "unknown leg", "no leg named %q in this fixture", name)
	return nil
}

func (f *v4Receive) leafReceivers() map[string]keys.Public {
	receivers := make(map[string]keys.Public, len(f.legs))
	for _, leg := range f.legs {
		receivers[leg.leaf.GetId()] = leg.receiver()
	}
	return receivers
}

func (f *v4Receive) claimPendingByReceiver() map[keys.Public]st.TransferReceiverStatus {
	statuses := make(map[keys.Public]st.TransferReceiverStatus, len(f.legs))
	for _, leg := range f.legs {
		statuses[leg.receiver()] = st.TransferReceiverStatusReceiverClaimPending
	}
	return statuses
}

func (f *v4Receive) swap(edges []*spark.ManifestEdge) wallet.PreimageSwapV4 {
	return wallet.PreimageSwapV4{
		Leaves:              f.leaves,
		LeafReceivers:       f.leafReceivers(),
		CounterpartyPrivKey: f.userPrivKey,
		PaymentHash:         f.paymentHash[:],
		Invoice:             f.invoice,
		AmountSats:          f.invoiceSats,
		Reason:              spark.InitiatePreimageSwapRequest_REASON_RECEIVE,
		Edges:               edges,
	}
}

func claimLeg(t *testing.T, transferID uuid.UUID, leg *receiveLeg) {
	t.Helper()

	token, err := wallet.AuthenticateWithServer(t.Context(), leg.config)
	require.NoError(t, err)
	receiverCtx := wallet.ContextWithToken(t.Context(), token)

	pending, err := wallet.QueryPendingTransfers(receiverCtx, leg.config)
	require.NoError(t, err)
	require.Len(t, pending.GetTransfers(), 1, "%s must have exactly one transfer to claim", leg.name)
	pendingTransfer := pending.GetTransfers()[0]
	require.Equal(t, transferID.String(), pendingTransfer.GetId())
	require.Len(t, pendingTransfer.GetLeaves(), 1, "%s must be routed only its own leaf", leg.name)

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(receiverCtx, leg.config, pendingTransfer)
	require.NoError(t, err)
	require.Equal(t, map[string]keys.Private{leg.leaf.GetId(): leg.newSigningPrivKey}, leafPrivKeyMap)

	_, err = wallet.ClaimTransferV2(receiverCtx, pendingTransfer, leg.config, []wallet.LeafKeyTweak{{
		Leaf:              pendingTransfer.GetLeaves()[0].GetLeaf(),
		SigningPrivKey:    leg.newSigningPrivKey,
		NewSigningPrivKey: keys.GeneratePrivateKey(),
	}})
	require.NoError(t, err)
}

// TestInitiatePreimageSwapV4_Consensus_MultiReceiverReceiveHappyPath drives a non-HODL lightning
// receive that pays the swap counterparty and a separate fee recipient in one v4 transfer.
func TestInitiatePreimageSwapV4_Consensus_MultiReceiverReceiveHappyPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot route through consensus engine: %v", err)
	}
	enableInitiatePreimageSwapV4Knobs(t, kc)

	fixture := newV4Receive(t)
	defer cleanUp(t, fixture.userConfig, fixture.paymentHash)

	response, transferID, err := wallet.InitiatePreimageSwapV4(t.Context(), fixture.sspConfig, fixture.swap(nil))
	require.NoError(t, err, "multi-receiver v4 receive swap should succeed")

	assert.Equal(t, fixture.preimage[:], response.GetPreimage())
	senderTransfer := response.GetTransfer()
	require.Equal(t, transferID.String(), senderTransfer.GetId())
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, senderTransfer.GetStatus())

	counterparty := fixture.leg(t, "counterparty")
	feeRecipient := fixture.leg(t, "fee recipient")
	for _, i := range operatorIndicesFromConfig(fixture.sspConfig) {
		entClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		t.Cleanup(func() { _ = entClient.Close() })

		row, err := entClient.Transfer.Query().Where(transferent.IDEQ(transferID)).Only(t.Context())
		require.NoError(t, err, "operator %d missing transfer row after v4 receive swap", i)
		assert.Equal(t, st.TransferStatusSenderKeyTweaked, row.Status, "operator %d transfer status mismatch", i)

		assert.Equal(t, fixture.claimPendingByReceiver(), transferReceiverStatusesByPubKey(t, entClient, transferID),
			"operator %d must route one claim-pending receiver per destination", i)
	}

	coordinatorClient := db.NewPostgresEntClientForIntegrationTest(t, fixture.sspConfig.CoordinatorDatabaseURI)
	t.Cleanup(func() { _ = coordinatorClient.Close() })

	claimLeg(t, transferID, counterparty)

	// Per-receiver status is the only status that answers "did this receiver get paid": the
	// parent stays at SENDER_KEY_TWEAKED whenever receiver status is authoritative.
	assert.Equal(t, st.TransferReceiverStatusCompleted,
		transferReceiverStatus(t, coordinatorClient, transferID, counterparty.receiver()))
	assert.Equal(t, st.TransferReceiverStatusReceiverClaimPending,
		transferReceiverStatus(t, coordinatorClient, transferID, feeRecipient.receiver()),
		"the fee recipient must still be claim-pending after the counterparty's claim")

	claimLeg(t, transferID, feeRecipient)

	assert.Equal(t, map[keys.Public]st.TransferReceiverStatus{
		counterparty.receiver(): st.TransferReceiverStatusCompleted,
		feeRecipient.receiver(): st.TransferReceiverStatusCompleted,
	}, transferReceiverStatusesByPubKey(t, coordinatorClient, transferID))
}

// Markup-on-net: the invoice goes out for the gross while the counterparty is owed only the net, so
// the three cuts must exhaust the markup and the gross must equal what the fixture invoice really
// carries — otherwise the leaves over-fund it and the SO's amount floor never binds.
//
// The bps is far above any real markup because those two constraints fight each other here: the
// fixture's invoice amount is fixed, and each fee leg funds its own leaf, so every cut has to clear
// the dust floor. A realistic markup on this gross would put all three legs under it.
const (
	feeBearingNetSats       = 9_011
	feeBearingMarkupBps     = 3_700
	feeBearingLightsparkCut = 1_000
	feeBearingPartnerCut    = 1_334
	feeBearingAffiliateCut  = 1_000
)

// The self-send leg is why this case is worth its own test: the SSP is the transfer's sole sender
// and simultaneously one of its own receivers, so the sender role must not shadow the receiver.
func TestInitiatePreimageSwapV4_Consensus_FeeBearingReceiveHappyPath(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot route through consensus engine: %v", err)
	}
	enableInitiatePreimageSwapV4Knobs(t, kc)

	markupSats := uint64(feeBearingNetSats * feeBearingMarkupBps / 10_000)
	require.Equal(t, markupSats, uint64(feeBearingLightsparkCut+feeBearingPartnerCut+feeBearingAffiliateCut),
		"the three fee legs must exhaust the markup, or the invoice is not the gross")
	grossSats := uint64(feeBearingNetSats) + markupSats
	require.Equal(t, uint64(fakeInvoiceSats), grossSats,
		"the gross must equal the invoice the fixture can actually issue, or the leaves over-fund the floor")

	fixture := newV4ReceiveWithLegs(t, grossSats, []*receiveLeg{
		{name: "counterparty", sats: feeBearingNetSats},
		{name: "partner", sats: feeBearingPartnerCut},
		{name: "affiliate", sats: feeBearingAffiliateCut},
		{name: "lightspark cut", sats: feeBearingLightsparkCut, selfSend: true},
	})
	defer cleanUp(t, fixture.userConfig, fixture.paymentHash)

	lightsparkCut := fixture.leg(t, "lightspark cut")
	require.Equal(t, fixture.sspConfig.IdentityPublicKey(), lightsparkCut.receiver(),
		"the lightspark cut must settle to the SSP's own identity key")
	require.Len(t, fixture.claimPendingByReceiver(), 4,
		"the self-send must be a distinct receiver, not folded into the sender")

	response, transferID, err := wallet.InitiatePreimageSwapV4(t.Context(), fixture.sspConfig, fixture.swap(nil))
	require.NoError(t, err, "fee-bearing v4 receive swap should succeed")

	assert.Equal(t, fixture.preimage[:], response.GetPreimage())
	senderTransfer := response.GetTransfer()
	require.Equal(t, transferID.String(), senderTransfer.GetId())
	assert.Equal(t, spark.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, senderTransfer.GetStatus())
	assert.Equal(t, grossSats, senderTransfer.GetTotalValue(),
		"the transfer must move the gross, fee legs included")

	for _, i := range operatorIndicesFromConfig(fixture.sspConfig) {
		entClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		t.Cleanup(func() { _ = entClient.Close() })

		row, err := entClient.Transfer.Query().Where(transferent.IDEQ(transferID)).Only(t.Context())
		require.NoError(t, err, "operator %d missing transfer row after fee-bearing receive", i)
		assert.Equal(t, st.TransferStatusSenderKeyTweaked, row.Status, "operator %d transfer status mismatch", i)

		assert.Equal(t, fixture.claimPendingByReceiver(), transferReceiverStatusesByPubKey(t, entClient, transferID),
			"operator %d must route a claim-pending receiver per fee leg and the counterparty", i)
	}

	coordinatorClient := db.NewPostgresEntClientForIntegrationTest(t, fixture.sspConfig.CoordinatorDatabaseURI)
	t.Cleanup(func() { _ = coordinatorClient.Close() })

	// Self-send last, so a stuck SSP receiver cannot be blamed on the legs around it.
	claimOrder := []*receiveLeg{
		fixture.leg(t, "counterparty"),
		fixture.leg(t, "partner"),
		fixture.leg(t, "affiliate"),
		lightsparkCut,
	}
	for i, leg := range claimOrder {
		claimLeg(t, transferID, leg)
		assert.Equal(t, st.TransferReceiverStatusCompleted,
			transferReceiverStatus(t, coordinatorClient, transferID, leg.receiver()),
			"%s must complete on its own claim", leg.name)
		for _, unclaimed := range claimOrder[i+1:] {
			assert.Equal(t, st.TransferReceiverStatusReceiverClaimPending,
				transferReceiverStatus(t, coordinatorClient, transferID, unclaimed.receiver()),
				"%s must still be claim-pending after the %s claim", unclaimed.name, leg.name)
		}
	}

	completedByReceiver := make(map[keys.Public]st.TransferReceiverStatus, len(claimOrder))
	for _, leg := range claimOrder {
		completedByReceiver[leg.receiver()] = st.TransferReceiverStatusCompleted
	}
	assert.Equal(t, completedByReceiver, transferReceiverStatusesByPubKey(t, coordinatorClient, transferID))
}

// TestInitiatePreimageSwapV4_Consensus_RefusesManifestNotCoveringExecutedLeaves omits the fee
// recipient's edge, so the manifest the counterparty and sender both signed covers only one of
// the two leaves the transfer moves.
func TestInitiatePreimageSwapV4_Consensus_RefusesManifestNotCoveringExecutedLeaves(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}
	kc, err := sparktesting.NewKnobController(t)
	if err != nil {
		t.Skipf("knob controller unavailable, cannot route through consensus engine: %v", err)
	}
	enableInitiatePreimageSwapV4Knobs(t, kc)

	fixture := newV4Receive(t)
	defer cleanUp(t, fixture.userConfig, fixture.paymentHash)

	counterparty := fixture.leg(t, "counterparty")
	counterpartyOnlyEdges := []*spark.ManifestEdge{{
		SenderIdentityPublicKey:   fixture.sspConfig.IdentityPublicKey().Serialize(),
		ReceiverIdentityPublicKey: counterparty.receiver().Serialize(),
		Amount: &spark.ManifestAmount{
			Amount: &spark.ManifestAmount_Sats{Sats: counterparty.leaf.GetValue()},
		},
	}}

	_, transferID, err := wallet.InitiatePreimageSwapV4(t.Context(), fixture.sspConfig, fixture.swap(counterpartyOnlyEdges))
	require.ErrorContains(t, err, "executed leaves have no matching manifest edge")

	leafIDs := make([]uuid.UUID, 0, len(fixture.leaves))
	for _, leaf := range fixture.leaves {
		leafID, err := uuid.Parse(leaf.Leaf.GetId())
		require.NoError(t, err)
		leafIDs = append(leafIDs, leafID)
	}

	for _, i := range operatorIndicesFromConfig(fixture.sspConfig) {
		entClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
		t.Cleanup(func() { _ = entClient.Close() })

		leafRows, err := entClient.TreeNode.Query().Where(treenode.IDIn(leafIDs...)).All(t.Context())
		require.NoError(t, err)
		require.Len(t, leafRows, len(leafIDs), "operator %d is missing leaf rows", i)
		for _, leafRow := range leafRows {
			assert.Equal(t, st.TreeNodeStatusAvailable, leafRow.Status,
				"operator %d left leaf %s locked after refusing the manifest", i, leafRow.ID)
		}

		// Prepare rolls its own transaction back, so the refusal normally leaves no row at all;
		// a row that survived a rollback fanout must at least be terminal.
		row, err := entClient.Transfer.Query().Where(transferent.IDEQ(transferID)).Only(t.Context())
		if ent.IsNotFound(err) {
			continue
		}
		require.NoError(t, err)
		assert.Equal(t, st.TransferStatusReturned, row.Status,
			"operator %d settled a transfer whose manifest does not cover its leaves", i)
	}
}
