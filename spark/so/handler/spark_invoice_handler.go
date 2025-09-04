package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/sparkinvoice"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
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
	invoiceParams := req.Invoice
	satsInvoiceIds := make([]uuid.UUID, len(invoiceParams))
	tokensInvoiceIds := make([]uuid.UUID, len(invoiceParams))
	for _, invoice := range invoiceParams {
		parsedInvoice, err := common.ParseSparkInvoice(invoice)
		if err != nil {
			return nil, err
		}
		switch parsedInvoice.Payment.Kind {
		case common.PaymentKindSats:
			satsInvoiceIds = append(satsInvoiceIds, parsedInvoice.Id)
		case common.PaymentKindTokens:
			tokensInvoiceIds = append(tokensInvoiceIds, parsedInvoice.Id)
		default:
			return nil, fmt.Errorf("unknown payment kind: %d", parsedInvoice.Payment.Kind)
		}
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db from context: %w", err)
	}
	baseQuery := db.SparkInvoice.Query()

	if len(satsInvoiceIds) > 0 {
		return nil, fmt.Errorf("sats invoices are not supported yet")
	}
	if len(tokensInvoiceIds) > 0 {
		baseQuery = baseQuery.Where(sparkinvoice.IDIn(tokensInvoiceIds...))
	}

	sparkInvoicesQuery := baseQuery.Where(
		sparkinvoice.HasTokenTransactionWith(
			tokentransaction.StatusEQ(st.TokenTransactionStatusFinalized),
		),
	)
	sparkInvoices, err := sparkInvoicesQuery.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get spark invoices: %w", err)
	}

	sparkInvoicesResponse := make([]*sparkpb.InvoiceResponse, 0)
	for _, sparkInvoice := range sparkInvoices {
		sparkInvoicesResponse = append(sparkInvoicesResponse, &sparkpb.InvoiceResponse{
			Invoice: sparkInvoice.SparkInvoice,
			Status:  sparkpb.InvoiceStatus_FINALIZED,
		})
	}

	return &sparkpb.QuerySparkInvoicesResponse{
		InvoiceStatuses: sparkInvoicesResponse,
	}, nil
}
