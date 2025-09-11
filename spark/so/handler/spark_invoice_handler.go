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
	limit := maxSparkInvoiceLimit
	if req.Limit > 0 {
		limit = min(maxSparkInvoiceLimit, int(req.Limit))
	}

	if len(req.Invoice) > 0 {
		return h.querySparkInvoicesByRawInvoice(ctx, req, limit)
	}

	return nil, sparkerrors.InvalidUserInputErrorf("no invoice strings provided")
}

func (h *SparkInvoiceHandler) querySparkInvoicesByRawInvoice(ctx context.Context, req *sparkpb.QuerySparkInvoicesRequest, limit int) (*sparkpb.QuerySparkInvoicesResponse, error) {
	ctx, span := tracer.Start(ctx, "SparkInvoiceHandler.querySparkInvoicesByRawInvoice")
	defer span.End()
	invoiceIDsInOrder := make([]uuid.UUID, 0, len(req.Invoice))
	idToInvoiceMap := make(map[uuid.UUID]string)
	satsInvoiceIDs := make([]uuid.UUID, 0, len(req.Invoice))
	tokenInvoiceIDs := make([]uuid.UUID, 0, len(req.Invoice))
	for _, invoice := range req.Invoice {
		decoded, err := common.ParseSparkInvoice(invoice)
		if err != nil {
			return nil, sparkerrors.InvalidUserInputErrorf("invalid invoice: %w", err)
		}
		idToInvoiceMap[decoded.Id] = invoice
		invoiceIDsInOrder = append(invoiceIDsInOrder, decoded.Id)
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
					Order(ent.Desc(transfer.FieldCreateTime)).
					Limit(1)
			}).
			Limit(limit).
			Select(sparkinvoice.FieldID, sparkinvoice.FieldSparkInvoice).
			All(ctx)
		if err != nil {
			return nil, err
		}

		notFoundOrReturnedInvoiceMap := mapSliceToSet(notFoundOrReturnedInvoiceIDs)
		for _, invoice := range notFoundOrReturnedInvoices {
			delete(notFoundOrReturnedInvoiceMap, invoice.ID)
			if invoice.Edges.Transfer != nil {
				transferEdge := invoice.Edges.Transfer
				if len(transferEdge) == 0 {
					return nil, fmt.Errorf("no transfers found for invoice %s", invoice.ID)
				}
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
			} else if invoice.Edges.TokenTransaction != nil {
				invoiceResponseMap[invoice.ID], err = buildTokenInvoiceResponse(invoice, sparkpb.InvoiceStatus_RETURNED)
				if err != nil {
					return nil, err
				}
			}
		}
		notFoundInvoiceIDs := setToSlice(notFoundOrReturnedInvoiceMap)
		for _, invoiceID := range notFoundInvoiceIDs {
			invoiceResponseMap[invoiceID] = &sparkpb.InvoiceResponse{
				Invoice: idToInvoiceMap[invoiceID],
				Status:  sparkpb.InvoiceStatus_NOT_FOUND,
			}
		}
	}

	invoiceResponseByRequestOrder := make([]*sparkpb.InvoiceResponse, 0, len(invoiceIDsInOrder))
	for _, id := range invoiceIDsInOrder {
		invoiceResponseByRequestOrder = append(invoiceResponseByRequestOrder, invoiceResponseMap[id])
	}

	return &sparkpb.QuerySparkInvoicesResponse{
		InvoiceStatuses: invoiceResponseByRequestOrder,
	}, nil
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
	for _, transfer := range transferResponses {
		delete(notFoundSatsInvoiceMap, transfer.Edges.SparkInvoice.ID)
		invoiceResponseMap[transfer.Edges.SparkInvoice.ID], err = buildSatsInvoiceResponse(transfer, status)
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
