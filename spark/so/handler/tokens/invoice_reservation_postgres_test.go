package tokens

import (
	"context"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

// These tests pin the invoice-reservation contract in PrepareTokenTransactionInternal: an invoice
// cannot attach to more than one token transaction. Both are deterministic — no timing windows.
// The lock test reaches below the application boundary (per the testing-philosophy
// concurrency-contract exception) because FOR UPDATE serialization is invisible from the handler's
// outputs. Needs Postgres — FOR UPDATE is a no-op on SQLite.

type invoiceSpec struct {
	invoiceStr string
	receiver   keys.Public
}

// encodeTokenInvoice builds a token-payment spark-invoice string. Expiry is set well beyond each
// transaction's expiry, which validateInvoiceFields requires; only the fields ParseSparkInvoice
// reads on the reservation path (id, receiver, expiry) matter.
func encodeTokenInvoice(t *testing.T, network btcnetwork.Network, receiver keys.Public, tokenIdentifier []byte, id uuid.UUID) string {
	t.Helper()
	fields := &sparkpb.SparkInvoiceFields{
		Version:    1,
		Id:         id[:],
		ExpiryTime: timestamppb.New(time.Now().Add(72 * time.Hour)),
		PaymentType: &sparkpb.SparkInvoiceFields_TokensPayment{
			TokensPayment: &sparkpb.TokensPayment{TokenIdentifier: tokenIdentifier},
		},
	}
	s, err := common.EncodeSparkAddress(receiver, network, fields)
	require.NoError(t, err)
	return s
}

// buildInvoicePayingTransfer mints a fresh finalized input owned by a per-call sender key and
// returns a valid transfer request spending it into one nil-amount output per invoice (each owned
// by that invoice's receiver), paying every invoice in the given attachment order. Runs in
// setupCtx; its minted input is committed with the rest of setup before any reservation runs.
func buildInvoicePayingTransfer(
	t *testing.T,
	setupCtx context.Context,
	dbtx *ent.Client,
	f *entfixtures.Fixtures,
	cfg *so.Config,
	network btcnetwork.Network,
	rng *rand.ChaCha8,
	tokenCreate *ent.TokenCreate,
	invoices []invoiceSpec,
) *tokeninternalpb.PrepareTransactionRequest {
	t.Helper()
	pbNet, err := network.MarshalProto()
	require.NoError(t, err)
	cfgVals := cfg.Lrc20Configs[strings.ToLower(network.String())]
	coordinator := testNonSelfCoordinator(t, cfg)

	senderKey := keys.MustGeneratePrivateKeyFromRand(rng)
	_, mintOutputs := f.CreateMintTransaction(
		tokenCreate,
		[]entfixtures.OutputSpec{{Amount: big.NewInt(int64(100 * len(invoices))), Owner: senderKey.Public()}},
		st.TokenTransactionStatusFinalized,
	)
	input := mintOutputs[0]

	amountBytes := make([]byte, 16)
	big.NewInt(100).FillBytes(amountBytes)
	outputs := make([]*tokenpb.TokenOutput, 0, len(invoices))
	attachments := make([]*tokenpb.InvoiceAttachment, 0, len(invoices))
	keyshareIDs := make([]string, 0, len(invoices))
	for _, inv := range invoices {
		ks := f.CreateKeyshare()
		_, err := dbtx.SigningKeyshare.UpdateOneID(ks.ID).SetCoordinatorIndex(coordinator.ID).Save(setupCtx)
		require.NoError(t, err)
		keyshareIDs = append(keyshareIDs, ks.ID.String())
		outputs = append(outputs, &tokenpb.TokenOutput{
			Id:                            new(uuid.Must(uuid.NewV7()).String()),
			OwnerPublicKey:                inv.receiver.Serialize(),
			TokenIdentifier:               tokenCreate.TokenIdentifier,
			TokenAmount:                   amountBytes,
			RevocationCommitment:          ks.PublicKey.Serialize(),
			WithdrawBondSats:              &cfgVals.WithdrawBondSats,
			WithdrawRelativeBlockLocktime: &cfgVals.WithdrawRelativeBlockLocktime,
		})
		attachments = append(attachments, &tokenpb.InvoiceAttachment{SparkInvoice: inv.invoiceStr})
	}

	now := time.Now()
	txProto := &tokenpb.TokenTransaction{
		Version: 2,
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: []*tokenpb.TokenOutputToSpend{{
					PrevTokenTransactionHash: input.CreatedTransactionFinalizedHash,
					PrevTokenTransactionVout: uint32(input.CreatedTransactionOutputVout),
				}},
			},
		},
		TokenOutputs:           outputs,
		Network:                pbNet,
		ExpiryTime:             timestamppb.New(now.Add(24 * time.Hour)),
		ClientCreatedTimestamp: timestamppb.New(now),
		InvoiceAttachments:     attachments,
	}
	for _, op := range cfg.GetSigningOperatorList() {
		txProto.SparkOperatorIdentityPublicKeys = append(txProto.SparkOperatorIdentityPublicKeys, op.GetPublicKey())
	}

	partialHash, err := utils.HashTokenTransaction(txProto, true)
	require.NoError(t, err)
	sig, err := schnorr.Sign(senderKey.ToBTCEC(), partialHash)
	require.NoError(t, err)

	return &tokeninternalpb.PrepareTransactionRequest{
		FinalTokenTransaction:      txProto,
		TokenTransactionSignatures: []*tokenpb.SignatureWithIndex{{InputIndex: 0, Signature: sig.Serialize()}},
		KeyshareIds:                keyshareIDs,
		CoordinatorPublicKey:       coordinator.IdentityPublicKey.Serialize(),
	}
}

// commitSetup commits the fixture-setup transaction so freshly-minted inputs are visible to the
// independent reservation sessions.
func commitSetup(t *testing.T, tc *db.TestContext) {
	t.Helper()
	setupTx := tc.Session.GetTxIfExists()
	require.NotNil(t, setupTx)
	require.NoError(t, setupTx.Commit())
}

func newInvoiceReservationFixture(t *testing.T, seed byte) (*db.TestContext, context.Context, *ent.Client, *entfixtures.Fixtures, *so.Config, *InternalPrepareTokenHandler, *ent.TokenCreate, *rand.ChaCha8) {
	t.Helper()
	rng := rand.NewChaCha8([32]byte{seed})
	cfg := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)
	dbtx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	f := entfixtures.New(t, ctx, dbtx).WithRNG(rng)
	handler := NewInternalPrepareTokenHandler(cfg)
	_, tokenCreate := f.CreateTokenCreateWithIssuer(btcnetwork.Regtest, f.RandomBytes(32), big.NewInt(1_000_000))
	return tc, ctx, dbtx, f, cfg, handler, tokenCreate, rng
}

// commitRepayableInvoice creates the invoice row committed and attached to a prior Started-but-
// expired transaction. The in-flight/finalized check treats an expired Started tx as reassignable,
// so the invoice is re-payable. Returns the invoice string.
func commitRepayableInvoice(t *testing.T, setupCtx context.Context, dbtx *ent.Client, rng *rand.ChaCha8, network btcnetwork.Network, receiver keys.Public, tokenIdentifier []byte, id uuid.UUID) string {
	t.Helper()
	invoiceStr := encodeTokenInvoice(t, network, receiver, tokenIdentifier, id)
	_, err := dbtx.SparkInvoice.Create().
		SetID(id).
		SetSparkInvoice(invoiceStr).
		SetReceiverPublicKey(receiver).
		Save(setupCtx)
	require.NoError(t, err)
	partialHash := make([]byte, 32)
	finalHash := make([]byte, 32)
	_, _ = rng.Read(partialHash)
	_, _ = rng.Read(finalHash)
	_, err = dbtx.TokenTransaction.Create().
		SetPartialTokenTransactionHash(partialHash).
		SetFinalizedTokenTransactionHash(finalHash).
		SetStatus(st.TokenTransactionStatusStarted).
		SetExpiryTime(time.Now().Add(-time.Hour)).
		AddSparkInvoiceIDs(id).
		Save(setupCtx)
	require.NoError(t, err)
	return invoiceStr
}

// TestReservationRejectsSecondAttachOfSameInvoice drives two prepares paying the same invoice
// sequentially through the handler: the first reserves and attaches it, the second is rejected by
// the in-flight/finalized check. Deterministic, and it exercises the real handler path — so it
// catches the reservation being dropped from the handler, which the lower-level lock test can't.
func TestReservationRejectsSecondAttachOfSameInvoice(t *testing.T) {
	t.Parallel()
	tc, ctx, dbtx, f, cfg, handler, tokenCreate, rng := newInvoiceReservationFixture(t, 11)
	network := btcnetwork.Regtest

	receiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	invoiceID := uuid.New()
	invoice := invoiceSpec{
		invoiceStr: encodeTokenInvoice(t, network, receiver, tokenCreate.TokenIdentifier, invoiceID),
		receiver:   receiver,
	}
	req1 := buildInvoicePayingTransfer(t, ctx, dbtx, f, cfg, network, rng, tokenCreate, []invoiceSpec{invoice})
	req2 := buildInvoicePayingTransfer(t, ctx, dbtx, f, cfg, network, rng, tokenCreate, []invoiceSpec{invoice})
	commitSetup(t, tc)

	sess1 := db.NewDefaultSessionFactory(tc.Client).NewSession(t.Context())
	ctx1 := ent.Inject(t.Context(), sess1)
	_, err := handler.PrepareTokenTransactionInternal(ctx1, req1)
	require.NoError(t, err)
	tx1 := sess1.GetTxIfExists()
	require.NotNil(t, tx1)
	require.NoError(t, tx1.Commit())

	sess2 := db.NewDefaultSessionFactory(tc.Client).NewSession(t.Context())
	ctx2 := ent.Inject(t.Context(), sess2)
	_, err = handler.PrepareTokenTransactionInternal(ctx2, req2)
	require.ErrorContains(t, err, "in flight or finalized")
	if tx2 := sess2.GetTxIfExists(); tx2 != nil {
		_ = tx2.Rollback()
	}
}

// TestReservationLockSerializesConcurrentReclaim proves the FOR UPDATE lock — not the create
// step — serializes concurrent reservations. The invoice row pre-exists (committed), so both
// sessions' idempotent CreateBulk no-ops and only the lock can serialize them. Deterministic:
// session B must block on A's lock (the assertion that fails if the lock is removed), then observe
// A's committed attachment and be rejected.
func TestReservationLockSerializesConcurrentReclaim(t *testing.T) {
	t.Parallel()
	tc, ctx, dbtx, _, _, _, tokenCreate, rng := newInvoiceReservationFixture(t, 41)
	network := btcnetwork.Regtest

	receiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	invoiceID := uuid.New()
	invoiceStr := commitRepayableInvoice(t, ctx, dbtx, rng, network, receiver, tokenCreate.TokenIdentifier, invoiceID)
	commitSetup(t, tc)

	txProto := &tokenpb.TokenTransaction{
		InvoiceAttachments: []*tokenpb.InvoiceAttachment{{SparkInvoice: invoiceStr}},
	}

	// A holds its transaction open (no commit) so the lock persists while B contends below.
	sessA := db.NewDefaultSessionFactory(tc.Client).NewSession(t.Context())
	ctxA := ent.Inject(t.Context(), sessA)
	_, err := ent.ReserveAndLockSparkInvoices(ctxA, txProto)
	require.NoError(t, err)
	dbA, err := ent.GetDbFromContext(ctxA)
	require.NoError(t, err)
	partialHash := make([]byte, 32)
	finalHash := make([]byte, 32)
	_, _ = rng.Read(partialHash)
	_, _ = rng.Read(finalHash)
	_, err = dbA.TokenTransaction.Create().
		SetPartialTokenTransactionHash(partialHash).
		SetFinalizedTokenTransactionHash(finalHash).
		SetStatus(st.TokenTransactionStatusStarted).
		SetExpiryTime(time.Now().Add(time.Hour)).
		AddSparkInvoiceIDs(invoiceID).
		Save(ctxA)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		sessB := db.NewDefaultSessionFactory(tc.Client).NewSession(t.Context())
		ctxB := ent.Inject(t.Context(), sessB)
		_, reserveErr := ent.ReserveAndLockSparkInvoices(ctxB, txProto)
		if tx := sessB.GetTxIfExists(); tx != nil {
			_ = tx.Rollback()
		}
		done <- reserveErr
	}()

	select {
	case err := <-done:
		t.Fatalf("session B did not block on the invoice lock (lock missing?); returned: %v", err)
	case <-time.After(time.Second):
		// B is correctly blocked on A's FOR UPDATE lock.
	}

	require.NoError(t, sessA.GetTxIfExists().Commit())
	require.ErrorContains(t, <-done, "in flight or finalized",
		"once A commits, B must observe A's attachment and be rejected")
}

// TestReservationRejectsDuplicateInvoiceIDs asserts a single request attaching the same invoice
// twice is rejected rather than silently deduped.
func TestReservationRejectsDuplicateInvoiceIDs(t *testing.T) {
	t.Parallel()
	tc, _, _, _, _, _, tokenCreate, rng := newInvoiceReservationFixture(t, 53)
	receiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	invoiceStr := encodeTokenInvoice(t, btcnetwork.Regtest, receiver, tokenCreate.TokenIdentifier, uuid.New())

	txProto := &tokenpb.TokenTransaction{
		InvoiceAttachments: []*tokenpb.InvoiceAttachment{
			{SparkInvoice: invoiceStr},
			{SparkInvoice: invoiceStr},
		},
	}
	sess := db.NewDefaultSessionFactory(tc.Client).NewSession(t.Context())
	rctx := ent.Inject(t.Context(), sess)
	_, err := ent.ReserveAndLockSparkInvoices(rctx, txProto)
	if tx := sess.GetTxIfExists(); tx != nil {
		_ = tx.Rollback()
	}
	require.ErrorContains(t, err, "duplicate spark invoice ID")
}

// TestReservationRejectsInvoiceIDReusedWithDifferentContent asserts that reusing an existing
// invoice's id with a different encoded invoice is rejected, rather than silently attaching the
// transaction to the stored (different-content) row.
func TestReservationRejectsInvoiceIDReusedWithDifferentContent(t *testing.T) {
	t.Parallel()
	tc, ctx, dbtx, _, _, _, tokenCreate, rng := newInvoiceReservationFixture(t, 67)
	network := btcnetwork.Regtest

	// Pre-existing committed invoice row: id X, receiver A.
	receiverA := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	invoiceID := uuid.New()
	commitRepayableInvoice(t, ctx, dbtx, rng, network, receiverA, tokenCreate.TokenIdentifier, invoiceID)
	commitSetup(t, tc)

	// A request reusing id X but encoding a different receiver — a different invoice string.
	receiverB := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	txProto := &tokenpb.TokenTransaction{
		InvoiceAttachments: []*tokenpb.InvoiceAttachment{
			{SparkInvoice: encodeTokenInvoice(t, network, receiverB, tokenCreate.TokenIdentifier, invoiceID)},
		},
	}
	sess := db.NewDefaultSessionFactory(tc.Client).NewSession(t.Context())
	rctx := ent.Inject(t.Context(), sess)
	_, err := ent.ReserveAndLockSparkInvoices(rctx, txProto)
	if tx := sess.GetTxIfExists(); tx != nil {
		_ = tx.Rollback()
	}
	require.ErrorContains(t, err, "conflicting invoices found")
}
