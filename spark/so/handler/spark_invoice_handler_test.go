package handler

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

// createTestSatsInvoice inserts a sats-denominated SparkInvoice into the test DB
// and returns the encoded invoice string together with its UUID.
func createTestSatsInvoice(t *testing.T, ctx context.Context, tc *db.TestContext) (string, uuid.UUID) {
	t.Helper()
	invoiceID := uuid.New()
	receiverKey := keys.GeneratePrivateKey().Public()
	senderKey := keys.GeneratePrivateKey().Public()
	network := btcnetwork.Regtest
	expiryTime := time.Now().Add(10 * time.Minute)
	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceID[:],
		new(uint64(1_000)),
		nil,
		senderKey,
		&expiryTime,
	)
	invoiceStr, err := common.EncodeSparkAddress(receiverKey, network, invoiceFields)
	require.NoError(t, err)

	_, err = tc.Client.SparkInvoice.Create().
		SetID(invoiceID).
		SetSparkInvoice(invoiceStr).
		SetExpiryTime(expiryTime).
		SetReceiverPublicKey(receiverKey).
		Save(ctx)
	require.NoError(t, err)

	return invoiceStr, invoiceID
}

// createTransferForInvoice inserts a Transfer with the given status linked to
// an existing SparkInvoice.
func createTransferForInvoice(t *testing.T, ctx context.Context, tc *db.TestContext, invoiceID uuid.UUID, status st.TransferStatus) {
	t.Helper()
	senderKey := keys.GeneratePrivateKey().Public()
	receiverKey := keys.GeneratePrivateKey().Public()
	_, err := tc.Client.Transfer.Create().
		SetID(uuid.New()).
		SetSenderIdentityPubkey(senderKey).
		SetReceiverIdentityPubkey(receiverKey).
		SetStatus(status).
		SetType(st.TransferTypeTransfer).
		SetNetwork(btcnetwork.Regtest).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(10 * time.Minute)).
		SetSparkInvoiceID(invoiceID).
		Save(ctx)
	require.NoError(t, err)
}

func createTestTokenInvoice(
	t *testing.T,
	ctx context.Context,
	tc *db.TestContext,
	status st.TokenTransactionStatus,
	expiryTime time.Time,
) (string, []byte) {
	t.Helper()
	invoiceID := uuid.New()
	receiverKey := keys.GeneratePrivateKey().Public()
	senderKey := keys.GeneratePrivateKey().Public()
	network := btcnetwork.Regtest
	tokenIdentifier := make([]byte, 32)
	tokenIdentifier[31] = 1
	amount := []byte{0x03, 0xe8}

	invoiceFields := common.CreateTokenSparkInvoiceFields(
		invoiceID[:],
		tokenIdentifier,
		amount,
		nil,
		senderKey,
		&expiryTime,
	)
	invoiceStr, err := common.EncodeSparkAddress(receiverKey, network, invoiceFields)
	require.NoError(t, err)

	_, err = tc.Client.SparkInvoice.Create().
		SetID(invoiceID).
		SetSparkInvoice(invoiceStr).
		SetExpiryTime(expiryTime).
		SetReceiverPublicKey(receiverKey).
		Save(ctx)
	require.NoError(t, err)

	partialHash := repeatedUUIDBytes(t)
	finalHash := repeatedUUIDBytes(t)
	_, err = tc.Client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(partialHash).
		SetFinalizedTokenTransactionHash(finalHash).
		SetStatus(status).
		SetExpiryTime(expiryTime).
		AddSparkInvoiceIDs(invoiceID).
		Save(ctx)
	require.NoError(t, err)

	return invoiceStr, finalHash
}

func repeatedUUIDBytes(t *testing.T) []byte {
	t.Helper()
	id := uuid.New()
	out := make([]byte, 0, 32)
	out = append(out, id[:]...)
	out = append(out, id[:]...)
	return out
}

// buildSatsInvoiceStr encodes a sats invoice for the given id/receiver/amount
// without persisting it. The sender key is randomized because it is not part of
// the money-field comparison.
func buildSatsInvoiceStr(t *testing.T, id uuid.UUID, receiver keys.Public, amount *uint64, memo *string) string {
	t.Helper()
	fields := common.CreateSatsSparkInvoiceFields(id[:], amount, memo, keys.GeneratePrivateKey().Public(), nil)
	s, err := common.EncodeSparkAddress(receiver, btcnetwork.Regtest, fields)
	require.NoError(t, err)
	return s
}

func buildTokenInvoiceStr(t *testing.T, id uuid.UUID, receiver keys.Public, tokenIdentifier, amount []byte) string {
	t.Helper()
	fields := common.CreateTokenSparkInvoiceFields(id[:], tokenIdentifier, amount, nil, keys.GeneratePrivateKey().Public(), nil)
	s, err := common.EncodeSparkAddress(receiver, btcnetwork.Regtest, fields)
	require.NoError(t, err)
	return s
}

func storeInvoiceRow(t *testing.T, ctx context.Context, tc *db.TestContext, id uuid.UUID, receiver keys.Public, invoiceStr string) {
	t.Helper()
	_, err := tc.Client.SparkInvoice.Create().
		SetID(id).
		SetSparkInvoice(invoiceStr).
		SetReceiverPublicKey(receiver).
		Save(ctx)
	require.NoError(t, err)
}

func createTokenTxForInvoice(t *testing.T, ctx context.Context, tc *db.TestContext, invoiceID uuid.UUID, status st.TokenTransactionStatus, expiry time.Time) []byte {
	t.Helper()
	finalHash := repeatedUUIDBytes(t)
	_, err := tc.Client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(repeatedUUIDBytes(t)).
		SetFinalizedTokenTransactionHash(finalHash).
		SetStatus(status).
		SetExpiryTime(expiry).
		AddSparkInvoiceIDs(invoiceID).
		Save(ctx)
	require.NoError(t, err)
	return finalHash
}

func querySingleStatus(t *testing.T, ctx context.Context, config *so.Config, queried string) *sparkpb.InvoiceResponse {
	t.Helper()
	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{queried},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 1)
	return resp.InvoiceStatuses[0]
}

// TestQuerySparkInvoicesAmountMismatch verifies that querying with an encoding
// that shares the stored invoice's id but differs in sats amount yields the
// MISMATCHED_INVOICE_* twin of the underlying payment state, while still
// returning the stored canonical encoding and transfer details.
func TestQuerySparkInvoicesAmountMismatch(t *testing.T) {
	for _, tt := range []struct {
		name           string
		transferStatus st.TransferStatus
		want           sparkpb.InvoiceStatus
	}{
		{"finalized", st.TransferStatusSenderKeyTweaked, sparkpb.InvoiceStatus_MISMATCHED_INVOICE_FINALIZED},
		{"pending", st.TransferStatusSenderInitiatedCoordinator, sparkpb.InvoiceStatus_MISMATCHED_INVOICE_PENDING},
		{"returned", st.TransferStatusReturned, sparkpb.InvoiceStatus_MISMATCHED_INVOICE_RETURNED},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config := sparktesting.TestConfig(t)
			ctx, tc := db.ConnectToTestPostgres(t)

			id := uuid.New()
			receiver := keys.GeneratePrivateKey().Public()
			storedAmount := uint64(1_000)
			storedStr := buildSatsInvoiceStr(t, id, receiver, &storedAmount, nil)
			storeInvoiceRow(t, ctx, tc, id, receiver, storedStr)
			createTransferForInvoice(t, ctx, tc, id, tt.transferStatus)

			queriedAmount := uint64(2_000)
			queriedStr := buildSatsInvoiceStr(t, id, receiver, &queriedAmount, nil)

			got := querySingleStatus(t, ctx, config, queriedStr)
			require.Equal(t, tt.want, got.Status)
			require.Equal(t, storedStr, got.Invoice, "mismatch response must keep the stored canonical encoding")
			require.NotNil(t, got.GetSatsTransfer(), "mismatch response must keep transfer details")
		})
	}
}

// TestQuerySparkInvoicesReceiverMismatch verifies a differing receiver pubkey
// (same id, same amount) is treated as a mismatch.
func TestQuerySparkInvoicesReceiverMismatch(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	id := uuid.New()
	storedReceiver := keys.GeneratePrivateKey().Public()
	amount := uint64(1_000)
	storedStr := buildSatsInvoiceStr(t, id, storedReceiver, &amount, nil)
	storeInvoiceRow(t, ctx, tc, id, storedReceiver, storedStr)
	createTransferForInvoice(t, ctx, tc, id, st.TransferStatusSenderKeyTweaked)

	queriedStr := buildSatsInvoiceStr(t, id, keys.GeneratePrivateKey().Public(), &amount, nil)

	got := querySingleStatus(t, ctx, config, queriedStr)
	require.Equal(t, sparkpb.InvoiceStatus_MISMATCHED_INVOICE_FINALIZED, got.Status)
}

// TestQuerySparkInvoicesOpenAmountMismatch verifies that an open-amount queried
// encoding (amount unset) does not match a stored fixed-amount encoding.
func TestQuerySparkInvoicesOpenAmountMismatch(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	id := uuid.New()
	receiver := keys.GeneratePrivateKey().Public()
	storedAmount := uint64(1_000)
	storedStr := buildSatsInvoiceStr(t, id, receiver, &storedAmount, nil)
	storeInvoiceRow(t, ctx, tc, id, receiver, storedStr)
	createTransferForInvoice(t, ctx, tc, id, st.TransferStatusSenderKeyTweaked)

	queriedStr := buildSatsInvoiceStr(t, id, receiver, nil, nil)

	got := querySingleStatus(t, ctx, config, queriedStr)
	require.Equal(t, sparkpb.InvoiceStatus_MISMATCHED_INVOICE_FINALIZED, got.Status)
}

// TestQuerySparkInvoicesMatchingEncodingUnchanged verifies that querying with an
// encoding whose money fields match the stored one leaves the status untouched,
// even when an excluded field (memo) differs.
func TestQuerySparkInvoicesMatchingEncodingUnchanged(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	id := uuid.New()
	receiver := keys.GeneratePrivateKey().Public()
	amount := uint64(1_000)
	storedStr := buildSatsInvoiceStr(t, id, receiver, &amount, nil)
	storeInvoiceRow(t, ctx, tc, id, receiver, storedStr)
	createTransferForInvoice(t, ctx, tc, id, st.TransferStatusSenderKeyTweaked)

	memo := "different memo"
	queriedStr := buildSatsInvoiceStr(t, id, receiver, &amount, &memo)

	got := querySingleStatus(t, ctx, config, queriedStr)
	require.Equal(t, sparkpb.InvoiceStatus_FINALIZED, got.Status,
		"matching money fields must not be flagged even when memo differs")
}

// TestQuerySparkInvoicesTokenAmountMismatch verifies token money-field mismatch
// detection for a finalized token transaction.
func TestQuerySparkInvoicesTokenAmountMismatch(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	id := uuid.New()
	receiver := keys.GeneratePrivateKey().Public()
	tokenIdentifier := make([]byte, 32)
	tokenIdentifier[31] = 1
	storedStr := buildTokenInvoiceStr(t, id, receiver, tokenIdentifier, []byte{0x03, 0xe8})
	storeInvoiceRow(t, ctx, tc, id, receiver, storedStr)
	createTokenTxForInvoice(t, ctx, tc, id, st.TokenTransactionStatusFinalized, time.Now().Add(10*time.Minute))

	queriedStr := buildTokenInvoiceStr(t, id, receiver, tokenIdentifier, []byte{0x07, 0xd0})

	got := querySingleStatus(t, ctx, config, queriedStr)
	require.Equal(t, sparkpb.InvoiceStatus_MISMATCHED_INVOICE_FINALIZED, got.Status)
}

// TestQuerySparkInvoicesDuplicateIDMatchingAndMismatched verifies that a single
// request containing two encodings of the same id is resolved per request
// position: the matching encoding reports FINALIZED and the mismatched one
// reports MISMATCHED_INVOICE_FINALIZED, regardless of order.
func TestQuerySparkInvoicesDuplicateIDMatchingAndMismatched(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	id := uuid.New()
	receiver := keys.GeneratePrivateKey().Public()
	storedAmount := uint64(1_000)
	matching := buildSatsInvoiceStr(t, id, receiver, &storedAmount, nil)
	storeInvoiceRow(t, ctx, tc, id, receiver, matching)
	createTransferForInvoice(t, ctx, tc, id, st.TransferStatusSenderKeyTweaked)

	mismatchedAmount := uint64(2_000)
	mismatched := buildSatsInvoiceStr(t, id, receiver, &mismatchedAmount, nil)

	handler := NewSparkInvoiceHandler(config)

	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{matching, mismatched},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 2)
	require.Equal(t, sparkpb.InvoiceStatus_FINALIZED, resp.InvoiceStatuses[0].Status)
	require.Equal(t, sparkpb.InvoiceStatus_MISMATCHED_INVOICE_FINALIZED, resp.InvoiceStatuses[1].Status)

	reversed, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{mismatched, matching},
	})
	require.NoError(t, err)
	require.Len(t, reversed.InvoiceStatuses, 2)
	require.Equal(t, sparkpb.InvoiceStatus_MISMATCHED_INVOICE_FINALIZED, reversed.InvoiceStatuses[0].Status)
	require.Equal(t, sparkpb.InvoiceStatus_FINALIZED, reversed.InvoiceStatuses[1].Status)
}

// TestQuerySparkInvoicesDuplicateIDBothNotFound verifies that two different
// encodings of the same unpaid id each return NOT_FOUND echoing their own
// encoding, rather than collapsing to a single last-wins entry.
func TestQuerySparkInvoicesDuplicateIDBothNotFound(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)

	id := uuid.New()
	receiver := keys.GeneratePrivateKey().Public()
	amountA := uint64(1_000)
	amountB := uint64(2_000)
	encA := buildSatsInvoiceStr(t, id, receiver, &amountA, nil)
	encB := buildSatsInvoiceStr(t, id, receiver, &amountB, nil)
	require.NotEqual(t, encA, encB)

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{encA, encB},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 2)
	require.Equal(t, sparkpb.InvoiceStatus_NOT_FOUND, resp.InvoiceStatuses[0].Status)
	require.Equal(t, sparkpb.InvoiceStatus_NOT_FOUND, resp.InvoiceStatuses[1].Status)
	require.Equal(t, encA, resp.InvoiceStatuses[0].Invoice)
	require.Equal(t, encB, resp.InvoiceStatuses[1].Invoice)
}

// TestQuerySparkInvoicesReturnsPendingStatus verifies that an invoice attached to
// a transfer in SenderInitiatedCoordinator state is reported as PENDING.
//
// This is the status assigned immediately when StartTransferV2 is received before
// the key-tweak processing begins.  It was previously untested: a regression that
// returned NOT_FOUND here would cause clients to believe in-flight payments had
// never been attempted.
func TestQuerySparkInvoicesReturnsPendingStatus(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	invoiceStr, invoiceID := createTestSatsInvoice(t, ctx, tc)
	createTransferForInvoice(t, ctx, tc, invoiceID, st.TransferStatusSenderInitiatedCoordinator)

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{invoiceStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 1)
	require.Equal(t, sparkpb.InvoiceStatus_PENDING, resp.InvoiceStatuses[0].Status,
		"expected PENDING for SenderInitiatedCoordinator transfer, got %s",
		resp.InvoiceStatuses[0].Status)
}

func TestQuerySparkInvoicesRejectsNilRequest(t *testing.T) {
	config := sparktesting.TestConfig(t)
	handler := NewSparkInvoiceHandler(config)

	_, err := handler.QuerySparkInvoices(t.Context(), nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "request is required")
}

func TestQuerySparkInvoicesRejectsMalformedInvoiceString(t *testing.T) {
	config := sparktesting.TestConfig(t)
	handler := NewSparkInvoiceHandler(config)

	_, err := handler.QuerySparkInvoices(t.Context(), &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{"spark:invalid-invoice"},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid invoice")
}

func TestQuerySparkInvoicesReturnsNotFoundForStoredInvoiceWithoutPaymentEdge(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	invoiceStr, _ := createTestSatsInvoice(t, ctx, tc)

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{invoiceStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 1)
	require.Equal(t, invoiceStr, resp.InvoiceStatuses[0].Invoice)
	require.Equal(t, sparkpb.InvoiceStatus_NOT_FOUND, resp.InvoiceStatuses[0].Status)
	require.Nil(t, resp.InvoiceStatuses[0].GetTransferType())
}

func TestQuerySparkInvoicesReturnsTokenInvoiceStatuses(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	pendingStr, pendingFinalHash := createTestTokenInvoice(t, ctx, tc, st.TokenTransactionStatusStarted, time.Now().Add(10*time.Minute))
	returnedStr, returnedFinalHash := createTestTokenInvoice(t, ctx, tc, st.TokenTransactionStatusSignedCancelled, time.Now().Add(10*time.Minute))

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{returnedStr, pendingStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 2)

	byInvoice := make(map[string]*sparkpb.InvoiceResponse, 2)
	for _, invoiceStatus := range resp.InvoiceStatuses {
		byInvoice[invoiceStatus.Invoice] = invoiceStatus
	}

	require.Contains(t, byInvoice, returnedStr)
	require.Equal(t, sparkpb.InvoiceStatus_RETURNED, byInvoice[returnedStr].Status)
	require.Equal(t, returnedFinalHash, byInvoice[returnedStr].GetTokenTransfer().GetFinalTokenTransactionHash())

	require.Contains(t, byInvoice, pendingStr)
	require.Equal(t, sparkpb.InvoiceStatus_PENDING, byInvoice[pendingStr].Status)
	require.Equal(t, pendingFinalHash, byInvoice[pendingStr].GetTokenTransfer().GetFinalTokenTransactionHash())
}

func TestQuerySparkInvoicesLimitDoesNotMisclassifyExplicitInvoiceList(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	firstStr, firstID := createTestSatsInvoice(t, ctx, tc)
	secondStr, secondID := createTestSatsInvoice(t, ctx, tc)
	createTransferForInvoice(t, ctx, tc, firstID, st.TransferStatusSenderKeyTweaked)
	createTransferForInvoice(t, ctx, tc, secondID, st.TransferStatusSenderKeyTweaked)

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Limit:   1,
		Invoice: []string{firstStr, secondStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 2)

	byInvoice := make(map[string]sparkpb.InvoiceStatus, 2)
	for _, invoiceStatus := range resp.InvoiceStatuses {
		byInvoice[invoiceStatus.Invoice] = invoiceStatus.Status
	}
	require.Equal(t, sparkpb.InvoiceStatus_FINALIZED, byInvoice[firstStr])
	require.Equal(t, sparkpb.InvoiceStatus_FINALIZED, byInvoice[secondStr])
}

func TestQuerySparkInvoicesRejectsOversizedExplicitInvoiceList(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	invoiceStr, _ := createTestSatsInvoice(t, ctx, tc)
	invoices := make([]string, maxSparkInvoiceLimit+1)
	for i := range invoices {
		invoices[i] = invoiceStr
	}

	handler := NewSparkInvoiceHandler(config)
	_, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: invoices,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "too many invoice strings provided")
}

// TestQuerySparkInvoicesReturnsPendingStatusForKeyTweakPending checks the second
// PENDING-eligible status: SenderKeyTweakPending.
func TestQuerySparkInvoicesReturnsPendingStatusForKeyTweakPending(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	invoiceStr, invoiceID := createTestSatsInvoice(t, ctx, tc)
	createTransferForInvoice(t, ctx, tc, invoiceID, st.TransferStatusSenderKeyTweakPending)

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{invoiceStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 1)
	require.Equal(t, sparkpb.InvoiceStatus_PENDING, resp.InvoiceStatuses[0].Status,
		"expected PENDING for SenderKeyTweakPending transfer, got %s",
		resp.InvoiceStatuses[0].Status)
}

// TestQuerySparkInvoicesReturnsReturnedStatus verifies that an invoice with an
// associated transfer that was returned to the sender (TransferStatusReturned) is
// reported as RETURNED — not NOT_FOUND.
//
// RETURNED means the invoice was used but the underlying transfer did not
// complete; funds went back to the sender.  Without this test, a regression that
// collapsed RETURNED into NOT_FOUND would be invisible to clients.
func TestQuerySparkInvoicesReturnsReturnedStatus(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	invoiceStr, invoiceID := createTestSatsInvoice(t, ctx, tc)
	// TransferStatusReturned sits outside both the PENDING set
	// (SenderKeyTweakPending / SenderInitiatedCoordinator) and the FINALIZED set
	// (SenderKeyTweaked … Completed), so it exercises the RETURNED branch.
	createTransferForInvoice(t, ctx, tc, invoiceID, st.TransferStatusReturned)

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{invoiceStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 1)
	require.Equal(t, sparkpb.InvoiceStatus_RETURNED, resp.InvoiceStatuses[0].Status,
		"expected RETURNED for a returned transfer, got %s",
		resp.InvoiceStatuses[0].Status)
}

// TestQuerySparkInvoicesReturnsReturnedStatusForExpiredTransfer checks that an
// expired transfer (TransferStatusExpired) also maps to RETURNED, not NOT_FOUND.
func TestQuerySparkInvoicesReturnsReturnedStatusForExpiredTransfer(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	invoiceStr, invoiceID := createTestSatsInvoice(t, ctx, tc)
	createTransferForInvoice(t, ctx, tc, invoiceID, st.TransferStatusExpired)

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{invoiceStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 1)
	require.Equal(t, sparkpb.InvoiceStatus_RETURNED, resp.InvoiceStatuses[0].Status,
		"expected RETURNED for an expired transfer, got %s",
		resp.InvoiceStatuses[0].Status)
}

// TestQuerySparkInvoicesDistinguishesPendingAndFinalized ensures that a single
// batch query correctly returns different statuses for a PENDING and a FINALIZED
// invoice simultaneously.
func TestQuerySparkInvoicesDistinguishesPendingAndFinalized(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	pendingStr, pendingID := createTestSatsInvoice(t, ctx, tc)
	finalizedStr, finalizedID := createTestSatsInvoice(t, ctx, tc)

	createTransferForInvoice(t, ctx, tc, pendingID, st.TransferStatusSenderInitiatedCoordinator)
	// SenderKeyTweaked is the first status in the FINALIZED set.
	createTransferForInvoice(t, ctx, tc, finalizedID, st.TransferStatusSenderKeyTweaked)

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{pendingStr, finalizedStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 2)

	statusByInvoice := make(map[string]sparkpb.InvoiceStatus, 2)
	for _, s := range resp.InvoiceStatuses {
		statusByInvoice[s.Invoice] = s.Status
	}
	require.Equal(t, sparkpb.InvoiceStatus_PENDING, statusByInvoice[pendingStr],
		"pending invoice should be PENDING")
	require.Equal(t, sparkpb.InvoiceStatus_FINALIZED, statusByInvoice[finalizedStr],
		"finalized invoice should be FINALIZED")
}
