package partner

import (
	"context"

	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

var transferPartnerMeter = otel.Meter("so.partner")

var transferPartnerWrites metric.Int64Counter

func init() {
	var err error
	transferPartnerWrites, err = transferPartnerMeter.Int64Counter(
		"spark_transfer_partner_writes",
		metric.WithDescription("Partner attribution writes to the transfer_partners table, by transfer type"),
	)
	if err != nil {
		otel.Handle(err)
		if transferPartnerWrites == nil {
			transferPartnerWrites = noop.Int64Counter{}
		}
	}
}

// RecordTransferPartnerWrite increments the transfer_partners write counter for
// the given transfer type. Callers invoke it after a write to the table.
func RecordTransferPartnerWrite(ctx context.Context, t schematype.TransferPartnerType) {
	transferPartnerWrites.Add(ctx, 1, metric.WithAttributes(attribute.String("transfer_type", string(t))))
}
