package handler

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/sparkinvoice"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/ent/transfer"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"google.golang.org/protobuf/proto"
)

const (
	maxSparkInvoiceLimit = 100
)

type SparkInvoiceHandler struct {
	config *so.Config
}

// NewSparkInvoiceHandler creates a new SparkInvoiceHandler.
func NewSparkInvoiceHandler(config *so.Config) *SparkInvoiceHandler {
	return &SparkInvoiceHandler{
		config: config,
	}
}

func (h *SparkInvoiceHandler) QuerySparkInvoices(ctx context.Context, req *sparkpb.QuerySparkInvoicesRequest) (*sparkpb.QuerySparkInvoicesResponse, error) {
	ctx, span := tracer.Start(ctx, "SparkInvoiceHandler.QuerySparkInvoices")
	defer span.End()

	if req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}

	if len(req.Invoice) > 0 {
		if len(req.Invoice) > maxSparkInvoiceLimit {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("too many invoice strings provided"))
		}
		// This is an explicit lookup path: the caller-provided invoice list is the
		// response boundary, while limit/offset only apply to paginated list APIs.
		return h.querySparkInvoicesByRawInvoice(ctx, req, len(req.Invoice))
	}

	return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("no invoice strings provided"))
}

// invoiceEntry is one queried invoice in request order. Status is resolved per
// entry rather than per id, so a batch containing multiple encodings of the same
// id is compared and reported position by position instead of being collapsed.
type invoiceEntry struct {
	raw    string
	parsed *common.ParsedSparkInvoice
	id     uuid.UUID
}

func (h *SparkInvoiceHandler) querySparkInvoicesByRawInvoice(ctx context.Context, req *sparkpb.QuerySparkInvoicesRequest, limit int) (*sparkpb.QuerySparkInvoicesResponse, error) {
	ctx, span := tracer.Start(ctx, "SparkInvoiceHandler.querySparkInvoicesByRawInvoice")
	defer span.End()
	entries := make([]invoiceEntry, 0, len(req.Invoice))
	satsInvoiceIDs := make([]uuid.UUID, 0, len(req.Invoice))
	tokenInvoiceIDs := make([]uuid.UUID, 0, len(req.Invoice))
	for _, invoice := range req.Invoice {
		decoded, err := common.ParseSparkInvoice(invoice)
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid invoice: %w", err))
		}
		entries = append(entries, invoiceEntry{raw: invoice, parsed: decoded, id: decoded.Id})
		switch decoded.Payment.Kind {
		case common.PaymentKindSats:
			satsInvoiceIDs = append(satsInvoiceIDs, decoded.Id)
		case common.PaymentKindTokens:
			tokenInvoiceIDs = append(tokenInvoiceIDs, decoded.Id)
		}
	}

	invoiceResponseMap := make(map[uuid.UUID]*sparkpb.InvoiceResponse)

	completedInvoiceMap, notCompletedSatsInvoiceIDs, notCompletedTokenInvoiceIDs, err := queryCompletedInvoices(ctx, satsInvoiceIDs, tokenInvoiceIDs, limit)
	if err != nil {
		return nil, err
	}
	for invoiceID := range completedInvoiceMap {
		invoiceResponseMap[invoiceID] = completedInvoiceMap[invoiceID]
	}

	var notCompletedOrPendingSatsInvoiceIDs []uuid.UUID
	var notCompletedOrPendingTokenInvoiceIDs []uuid.UUID
	if len(notCompletedSatsInvoiceIDs) > 0 || len(notCompletedTokenInvoiceIDs) > 0 {
		pendingInvoiceMap, notPendingSatsInvoiceIDs, notPendingTokenInvoiceIDs, err := queryPendingInvoices(ctx, notCompletedSatsInvoiceIDs, notCompletedTokenInvoiceIDs, limit)
		if err != nil {
			return nil, err
		}
		for invoiceID := range pendingInvoiceMap {
			invoiceResponseMap[invoiceID] = pendingInvoiceMap[invoiceID]
		}
		notCompletedOrPendingSatsInvoiceIDs = notPendingSatsInvoiceIDs
		notCompletedOrPendingTokenInvoiceIDs = notPendingTokenInvoiceIDs
	}

	notFoundOrReturnedInvoiceIDs := append(notCompletedOrPendingSatsInvoiceIDs, notCompletedOrPendingTokenInvoiceIDs...)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if len(notFoundOrReturnedInvoiceIDs) > 0 {
		notFoundOrReturnedInvoices, err := db.SparkInvoice.Query().
			Where(sparkinvoice.IDIn(notFoundOrReturnedInvoiceIDs...)).
			WithTokenTransaction(func(q *ent.TokenTransactionQuery) {
				q.Select(
					tokentransaction.FieldID,
					tokentransaction.FieldStatus,
					tokentransaction.FieldFinalizedTokenTransactionHash,
				).
					Order(ent.Desc(tokentransaction.FieldCreateTime)).
					Limit(1)
			}).
			WithTransfer(func(q *ent.TransferQuery) {
				q.Select(
					transfer.FieldID,
					transfer.FieldStatus,
				).
					WithSparkInvoice().
					Order(ent.Desc(transfer.FieldCreateTime)).
					Limit(1)
			}).
			Limit(limit).
			Select(sparkinvoice.FieldID, sparkinvoice.FieldSparkInvoice).
			All(ctx)
		if err != nil {
			return nil, err
		}

		for _, invoice := range notFoundOrReturnedInvoices {
			transferEdge := invoice.Edges.Transfer
			tokenTransactionEdge := invoice.Edges.TokenTransaction
			switch {
			case len(transferEdge) > 0:
				if len(transferEdge) > 1 {
					return nil, fmt.Errorf("multiple transfers found for invoice %s", invoice.ID)
				}
				if transferEdge[0] == nil {
					return nil, fmt.Errorf("transfer is nil for invoice %s", invoice.ID)
				}
				invoiceResponseMap[invoice.ID], err = buildSatsInvoiceResponse(invoice.Edges.Transfer[0], sparkpb.InvoiceStatus_RETURNED)
				if err != nil {
					return nil, err
				}
			case len(tokenTransactionEdge) > 0:
				invoiceResponseMap[invoice.ID], err = buildTokenInvoiceResponse(invoice, sparkpb.InvoiceStatus_RETURNED)
				if err != nil {
					return nil, err
				}
			}
		}
	}

	// Resolve a response per request position, not per id. A payment is looked up
	// by invoice id alone, but ids can be squatted: a batch may contain multiple
	// encodings of the same id, and each must be compared against the stored
	// invoice individually so a squatter's payment is never reported as the
	// caller's.
	invoiceResponseByRequestOrder := make([]*sparkpb.InvoiceResponse, 0, len(entries))
	for _, entry := range entries {
		base, ok := invoiceResponseMap[entry.id]
		if !ok {
			// No resolved payment for this id: NOT_FOUND, echoing this entry's own
			// encoding so duplicate ids with different encodings are each preserved.
			invoiceResponseByRequestOrder = append(invoiceResponseByRequestOrder, &sparkpb.InvoiceResponse{
				Invoice: entry.raw,
				Status:  sparkpb.InvoiceStatus_NOT_FOUND,
			})
			continue
		}
		// Clone so req.invoices sharing an id don't mutate invoiceResponseMap.
		response := proto.Clone(base).(*sparkpb.InvoiceResponse)
		if mismatchStatus, guarded := statusToMismatchedInvoiceStatus[response.Status]; guarded {
			responseParsed, err := common.ParseSparkInvoice(response.Invoice)
			if err != nil {
				return nil, fmt.Errorf("failed to parse stored invoice %s: %w", entry.id, err)
			}
			if !invoiceMoneyFieldsMatch(entry.parsed, responseParsed) {
				response.Status = mismatchStatus
			}
		}
		invoiceResponseByRequestOrder = append(invoiceResponseByRequestOrder, response)
	}

	return &sparkpb.QuerySparkInvoicesResponse{
		InvoiceStatuses: invoiceResponseByRequestOrder,
	}, nil
}

// statusToMismatchedInvoiceStatus maps a resolved payment status to the status
// reported when the queried encoding's money fields differ from the stored one.
// Membership also gates which statuses are subject to the encoding check.
var statusToMismatchedInvoiceStatus = map[sparkpb.InvoiceStatus]sparkpb.InvoiceStatus{
	sparkpb.InvoiceStatus_FINALIZED: sparkpb.InvoiceStatus_MISMATCHED_INVOICE_FINALIZED,
	sparkpb.InvoiceStatus_PENDING:   sparkpb.InvoiceStatus_MISMATCHED_INVOICE_PENDING,
	sparkpb.InvoiceStatus_RETURNED:  sparkpb.InvoiceStatus_MISMATCHED_INVOICE_RETURNED,
}

// invoiceMoneyFieldsMatch reports whether two encodings sharing an id agree on
// their money fields: receiver, payment kind, and the payment amounts/token
// identifier. Unset and set values are distinct (open-amount != fixed-amount).
// Non-money fields (memo, sender, expiry, signature) are intentionally ignored.
func invoiceMoneyFieldsMatch(queried, stored *common.ParsedSparkInvoice) bool {
	if !queried.ReceiverPublicKey.Equals(stored.ReceiverPublicKey) {
		return false
	}
	if queried.Payment.Kind != stored.Payment.Kind {
		return false
	}
	switch queried.Payment.Kind {
	case common.PaymentKindSats:
		return proto.Equal(queried.Payment.SatsPayment, stored.Payment.SatsPayment)
	case common.PaymentKindTokens:
		return proto.Equal(queried.Payment.TokensPayment, stored.Payment.TokensPayment)
	default:
		return false
	}
}

func queryCompletedInvoices(ctx context.Context, satsInvoiceIDs []uuid.UUID, tokenInvoiceIDs []uuid.UUID, limit int) (completedInvoiceMap map[uuid.UUID]*sparkpb.InvoiceResponse, notFoundSatsInvoiceIDs []uuid.UUID, notFoundTokenInvoiceIDs []uuid.UUID, err error) {
	completedSatsTransfers := make([]*ent.Transfer, 0, len(satsInvoiceIDs))
	completedTokenInvoices := make([]*ent.SparkInvoice, 0, len(tokenInvoiceIDs))

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(satsInvoiceIDs) > 0 {
		completedSatsTransfers, err = db.Transfer.Query().
			Where(
				transfer.HasSparkInvoiceWith(sparkinvoice.IDIn(satsInvoiceIDs...)),
				transfer.StatusIn(
					st.TransferStatusSenderKeyTweaked,
					st.TransferStatusReceiverKeyTweaked,
					st.TransferStatusReceiverKeyTweakLocked,
					st.TransferStatusReceiverKeyTweakApplied,
					st.TransferStatusReceiverRefundSigned,
					st.TransferStatusCompleted,
				),
			).
			WithSparkInvoice().
			Limit(limit).
			Select(transfer.FieldID).
			All(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	if len(tokenInvoiceIDs) > 0 {
		completedTokenInvoices, err = db.SparkInvoice.
			Query().
			Where(
				sparkinvoice.IDIn(tokenInvoiceIDs...),
				sparkinvoice.HasTokenTransactionWith(
					tokentransaction.StatusIn(
						st.TokenTransactionStatusRevealed,
						st.TokenTransactionStatusFinalized,
					),
				),
			).
			WithTokenTransaction(func(q *ent.TokenTransactionQuery) {
				q.Select(
					tokentransaction.FieldID,
					tokentransaction.FieldStatus,
					tokentransaction.FieldFinalizedTokenTransactionHash,
				).
					Where(tokentransaction.StatusIn(
						st.TokenTransactionStatusRevealed,
						st.TokenTransactionStatusFinalized,
					))
			}).
			Limit(limit).
			Select(sparkinvoice.FieldID, sparkinvoice.FieldSparkInvoice).
			All(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	return buildQueryResponseForStatus(completedSatsTransfers, completedTokenInvoices, satsInvoiceIDs, tokenInvoiceIDs, sparkpb.InvoiceStatus_FINALIZED)
}

func queryPendingInvoices(ctx context.Context, satsInvoiceIDs []uuid.UUID, tokenInvoiceIDs []uuid.UUID, limit int) (pendingInvoiceMap map[uuid.UUID]*sparkpb.InvoiceResponse, notFoundSatsInvoiceIDs []uuid.UUID, notFoundTokenInvoiceIDs []uuid.UUID, err error) {
	now := time.Now().UTC()
	pendingSatsTransfers := make([]*ent.Transfer, 0, len(satsInvoiceIDs))
	pendingTokenInvoices := make([]*ent.SparkInvoice, 0, len(tokenInvoiceIDs))

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(satsInvoiceIDs) > 0 {
		pendingSatsTransfers, err = db.Transfer.Query().
			Where(
				transfer.HasSparkInvoiceWith(sparkinvoice.IDIn(satsInvoiceIDs...)),
				transfer.StatusIn(
					st.TransferStatusSenderKeyTweakPending,
					st.TransferStatusSenderInitiatedCoordinator),
			).
			WithSparkInvoice().
			Limit(limit).
			Select(transfer.FieldID).
			All(ctx)
		if err != nil {
			return nil, satsInvoiceIDs, tokenInvoiceIDs, err
		}
	}
	if len(tokenInvoiceIDs) > 0 {
		pendingTokenInvoices, err = db.SparkInvoice.
			Query().
			Where(
				sparkinvoice.IDIn(tokenInvoiceIDs...),
				sparkinvoice.HasTokenTransactionWith(
					tokentransaction.StatusIn(
						st.TokenTransactionStatusStarted,
						st.TokenTransactionStatusSigned,
					),
					tokentransaction.ExpiryTimeGT(now),
				),
			).
			WithTokenTransaction(func(q *ent.TokenTransactionQuery) {
				q.Select(
					tokentransaction.FieldID,
					tokentransaction.FieldStatus,
					tokentransaction.FieldFinalizedTokenTransactionHash,
				).
					Where(
						tokentransaction.StatusIn(
							st.TokenTransactionStatusStarted,
							st.TokenTransactionStatusSigned,
						),
						tokentransaction.ExpiryTimeGT(now),
					)
			}).
			Limit(limit).
			Select(sparkinvoice.FieldID, sparkinvoice.FieldSparkInvoice).
			All(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	return buildQueryResponseForStatus(pendingSatsTransfers, pendingTokenInvoices, satsInvoiceIDs, tokenInvoiceIDs, sparkpb.InvoiceStatus_PENDING)
}

func buildQueryResponseForStatus(transferResponses []*ent.Transfer, invoiceResponses []*ent.SparkInvoice, queriedSatsInvoiceIDs []uuid.UUID, queriedTokensInvoiceIDs []uuid.UUID, status sparkpb.InvoiceStatus) (invoiceResponseMap map[uuid.UUID]*sparkpb.InvoiceResponse, notFoundSatsInvoiceIDs []uuid.UUID, notFoundTokenInvoiceIDs []uuid.UUID, err error) {
	invoiceResponseMap = make(map[uuid.UUID]*sparkpb.InvoiceResponse)
	notFoundSatsInvoiceMap := mapSliceToSet(queriedSatsInvoiceIDs)
	notFoundTokenInvoiceMap := mapSliceToSet(queriedTokensInvoiceIDs)
	for _, response := range transferResponses {
		delete(notFoundSatsInvoiceMap, response.Edges.SparkInvoice.ID)
		invoiceResponseMap[response.Edges.SparkInvoice.ID], err = buildSatsInvoiceResponse(response, status)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	for _, invoice := range invoiceResponses {
		delete(notFoundTokenInvoiceMap, invoice.ID)
		invoiceResponseMap[invoice.ID], err = buildTokenInvoiceResponse(invoice, status)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	notFoundSatsInvoiceIDs = setToSlice(notFoundSatsInvoiceMap)
	notFoundTokenInvoiceIDs = setToSlice(notFoundTokenInvoiceMap)
	return invoiceResponseMap, notFoundSatsInvoiceIDs, notFoundTokenInvoiceIDs, nil
}

func buildSatsInvoiceResponse(transfer *ent.Transfer, status sparkpb.InvoiceStatus) (*sparkpb.InvoiceResponse, error) {
	if transfer.Edges.SparkInvoice == nil {
		return nil, fmt.Errorf("spark invoice is nil for transfer %s", transfer.ID)
	}
	if len(transfer.Edges.SparkInvoice.SparkInvoice) == 0 {
		return nil, fmt.Errorf("spark invoice is empty for transfer %s", transfer.ID)
	}
	return &sparkpb.InvoiceResponse{
		Invoice: transfer.Edges.SparkInvoice.SparkInvoice,
		Status:  status,
		TransferType: &sparkpb.InvoiceResponse_SatsTransfer{
			SatsTransfer: &sparkpb.SatsTransfer{
				TransferId: transfer.ID[:],
			},
		},
	}, nil
}

func buildTokenInvoiceResponse(invoice *ent.SparkInvoice, status sparkpb.InvoiceStatus) (*sparkpb.InvoiceResponse, error) {
	tokenTxEdge := invoice.Edges.TokenTransaction
	if len(tokenTxEdge) == 0 || tokenTxEdge == nil {
		return nil, fmt.Errorf("no token transaction found for invoice %s", invoice.ID)
	}
	if len(tokenTxEdge) > 1 {
		return nil, fmt.Errorf("multiple token transactions found for invoice %s", invoice.ID)
	}
	if tokenTxEdge[0] == nil {
		return nil, fmt.Errorf("token transaction is nil for invoice %s", invoice.ID)
	}

	return &sparkpb.InvoiceResponse{
		Invoice: invoice.SparkInvoice,
		Status:  status,
		TransferType: &sparkpb.InvoiceResponse_TokenTransfer{
			TokenTransfer: &sparkpb.TokenTransfer{
				FinalTokenTransactionHash: tokenTxEdge[0].FinalizedTokenTransactionHash,
			},
		},
	}, nil
}

func mapSliceToSet(ids []uuid.UUID) map[uuid.UUID]struct{} {
	result := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		result[id] = struct{}{}
	}
	return result
}

func setToSlice(set map[uuid.UUID]struct{}) []uuid.UUID {
	result := make([]uuid.UUID, 0, len(set))
	for id := range set {
		result = append(result, id)
	}
	return result
}
