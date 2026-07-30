package partner

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	msdk "go.opentelemetry.io/otel/sdk/metric"
	md "go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
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

func TestPartnerJWTInterceptor_MalformedJWTEmitsFailureMetric(t *testing.T) {
	i := makeTestInterceptor(map[string]*testPartnerKeyEntry{}, map[string]uuid.UUID{})
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, "not.a.valid.jwt.token"))
	info := &grpc.UnaryServerInfo{FullMethod: "/spark.SparkService/start_transfer"}

	before := collectFailureCountsByReason(t, metricTestReader)
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	after := collectFailureCountsByReason(t, metricTestReader)

	require.NoError(t, err, "an unusable JWT must not fail the request")
	_, attributed := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, attributed)

	reason := string(AttributionFailureJWTInvalid)
	assert.Equal(t, int64(1), after[reason]-before[reason])
}

func TestPartnerJWTInterceptor_PartnerCreateFailureEmitsFailureMetric(t *testing.T) {
	priv, pub := makeSecp256k1Key(t)
	keyID := uuid.New()
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{"partner-a": {pubKey: pub, partnerKeyID: keyID}},
		map[string]uuid.UUID{},
	)
	token := makeES256KJWT(t, priv, "partner-a", testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))
	info := &grpc.UnaryServerInfo{FullMethod: "/spark.SparkService/start_transfer"}

	before := collectFailureCountsByReason(t, metricTestReader)
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	after := collectFailureCountsByReason(t, metricTestReader)

	require.NoError(t, err)
	pInfo, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	require.True(t, ok)
	assert.Equal(t, uuid.Nil, pInfo.PartnerDBID, "attribution should have been dropped")

	reason := string(AttributionFailurePartnerCreateFailed)
	assert.Equal(t, int64(1), after[reason]-before[reason])
}

func TestSaveTransferPartner_MissingDBEmitsFailureMetric(t *testing.T) {
	ctx := knobs.InjectKnobsService(t.Context(), knobs.New(knobs.NewStaticValuesProvider(map[string]float64{
		knobs.KnobEnablePartnerJWT: 100,
	})))
	ctx = context.WithValue(ctx, partnerContextKey, &PartnerInfo{
		PartnerDBID: uuid.New(),
		PartnerID:   "partner-a",
		Label:       testLabel,
	})

	before := collectFailureCountsByReason(t, metricTestReader)
	SaveTransferPartner(ctx, uuid.New(), schematype.TransferPartnerTypeTransfer)
	after := collectFailureCountsByReason(t, metricTestReader)

	reason := string(AttributionFailureDBContextMissing)
	assert.Equal(t, int64(1), after[reason]-before[reason])
}

// collectWriteCountsByType reads the spark_transfer_partner_writes counter from
// the reader and returns its cumulative per-transfer_type values.
func collectWriteCountsByType(t *testing.T, reader *msdk.ManualReader) map[string]int64 {
	t.Helper()
	return collectCounter(t, reader, "spark_transfer_partner_writes", "transfer_type")
}

func collectFailureCountsByReason(t *testing.T, reader *msdk.ManualReader) map[string]int64 {
	t.Helper()
	return collectCounter(t, reader, "spark_transfer_partner_attribution_failures", "reason")
}

func collectCounter(t *testing.T, reader *msdk.ManualReader, name, attr string) map[string]int64 {
	t.Helper()
	var rm md.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &rm))

	counts := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(md.Sum[int64])
			require.True(t, ok, "%s should be an int64 sum", name)
			for _, dp := range sum.DataPoints {
				v, ok := dp.Attributes.Value(attribute.Key(attr))
				require.True(t, ok, "%s data point missing %s attribute", name, attr)
				counts[v.AsString()] += dp.Value
			}
		}
	}
	return counts
}
