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

var (
	transferPartnerWrites             metric.Int64Counter
	transferPartnerAttributionFailure metric.Int64Counter
)

type AttributionFailure string

const (
	AttributionFailureJWTInvalid          AttributionFailure = "jwt_invalid"
	AttributionFailurePartnerCreateFailed AttributionFailure = "partner_create_failed"
	AttributionFailureDBContextMissing    AttributionFailure = "db_context_missing"
	AttributionFailureWriteFailed         AttributionFailure = "write_failed"
)

func init() {
	transferPartnerWrites = counterOrNoop(
		"spark_transfer_partner_writes",
		"Partner attribution writes to the transfer_partners table, by transfer type",
	)
	transferPartnerAttributionFailure = counterOrNoop(
		"spark_transfer_partner_attribution_failures",
		"Requests that lost their partner attribution without failing, by reason",
	)
}

func counterOrNoop(name, description string) metric.Int64Counter {
	counter, err := transferPartnerMeter.Int64Counter(name, metric.WithDescription(description))
	if err != nil {
		otel.Handle(err)
		if counter == nil {
			return noop.Int64Counter{}
		}
	}
	return counter
}

// RecordTransferPartnerWrite increments the transfer_partners write counter for
// the given transfer type. Callers invoke it after a write to the table.
func RecordTransferPartnerWrite(ctx context.Context, t schematype.TransferPartnerType) {
	transferPartnerWrites.Add(ctx, 1, metric.WithAttributes(attribute.String("transfer_type", string(t))))
}

func RecordAttributionFailure(ctx context.Context, reason AttributionFailure) {
	transferPartnerAttributionFailure.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", string(reason))))
}
