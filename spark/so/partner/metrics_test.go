package partner

import (
	"context"
	"testing"

	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	msdk "go.opentelemetry.io/otel/sdk/metric"
	md "go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// metricTestReader captures spark_transfer_partner_writes. The meter provider is
// installed once for the package so the package-init counter delegates to it
// exactly once — OTel's global delegation fires a single time per process, so
// re-setting the provider inside a test would not re-bind the counter and would
// break under go test -count=2. Tests assert on deltas so repeated runs and
// increments from other tests in the package don't interfere.
var metricTestReader = msdk.NewManualReader()

func init() {
	otel.SetMeterProvider(msdk.NewMeterProvider(msdk.WithReader(metricTestReader)))
}

// TestSaveTransferPartner_EmitsWriteMetric verifies that a successful write to
// the transfer_partners table increments spark_transfer_partner_writes, with one
// data point per transfer type. Metrics are captured through an in-memory
// ManualReader rather than the /metrics endpoint.
func TestSaveTransferPartner_EmitsWriteMetric(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.New(knobs.NewStaticValuesProvider(map[string]float64{
		knobs.KnobEnablePartnerJWT: 100,
	})))
	dbClient := getDB(t, ctx)

	p := createTestPartner(t, ctx, dbClient, "partner-a", "client-1")
	ctx = context.WithValue(ctx, partnerContextKey, &PartnerInfo{
		PartnerDBID: p.ID,
		PartnerID:   "partner-a",
		Label:       "client-1",
	})

	before := collectWriteCountsByType(t, metricTestReader)

	depositTransfer := createTestTransfer(t, ctx, dbClient)
	coopExitTransfer := createTestTransfer(t, ctx, dbClient)
	SaveTransferPartner(ctx, depositTransfer, schematype.TransferPartnerTypeDeposit)
	SaveTransferPartner(ctx, coopExitTransfer, schematype.TransferPartnerTypeCooperativeExit)

	after := collectWriteCountsByType(t, metricTestReader)
	deposit := string(schematype.TransferPartnerTypeDeposit)
	coopExit := string(schematype.TransferPartnerTypeCooperativeExit)
	assert.Equal(t, int64(1), after[deposit]-before[deposit])
	assert.Equal(t, int64(1), after[coopExit]-before[coopExit])
}

// collectWriteCountsByType reads the spark_transfer_partner_writes counter from
// the reader and returns its cumulative per-transfer_type values.
func collectWriteCountsByType(t *testing.T, reader *msdk.ManualReader) map[string]int64 {
	t.Helper()
	var rm md.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &rm))

	counts := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "spark_transfer_partner_writes" {
				continue
			}
			sum, ok := m.Data.(md.Sum[int64])
			require.True(t, ok, "spark_transfer_partner_writes should be an int64 sum")
			for _, dp := range sum.DataPoints {
				v, ok := dp.Attributes.Value("transfer_type")
				require.True(t, ok, "data point missing transfer_type attribute")
				counts[v.AsString()] += dp.Value
			}
		}
	}
	return counts
}
