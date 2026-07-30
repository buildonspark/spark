package task

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entephemeral"
	entephemeraltest "github.com/lightsparkdev/spark/so/entephemeral/enttest"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	msdk "go.opentelemetry.io/otel/sdk/metric"
	md "go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// The task is read-only, so its outcome counter is the whole observable result.
// These tests therefore run the task the way the scheduler does and read the
// counter back out of an in-memory reader.
//
// The meter provider is installed once for the package: OTel's global delegation
// binds each instrument the first time it is used, so re-setting the provider
// inside a test would not re-bind it and would break under `go test -count=2`.
// Tests assert on deltas, and the metric-asserting ones do not run in parallel
// because they share the counter's attribute values.
//
// Deliberately never restored or shut down. Restoring the previous provider would
// be hygiene theatre: the package's counters are sync.OnceValue, so they are
// already bound to this one and would keep writing to it. Shutting the reader down
// would be worse than a no-op — it is pull-based, with no exporter goroutine or
// connection to leak, and a shut-down reader rejects the later Collect calls the
// remaining tests depend on.
var reconcileSigningKeyshareSecretPointersMetricReader = msdk.NewManualReader()

func init() {
	otel.SetMeterProvider(msdk.NewMeterProvider(msdk.WithReader(reconcileSigningKeyshareSecretPointersMetricReader)))
}

// The SP-3668 prod shape: main's pointer sits exactly one above the ephemeral
// latest, because the rotation's commit hook retired the version it had just
// written.
func TestReconcileSigningKeyshareSecretPointers_ReportsPointerOneAboveEphemeralLatest(t *testing.T) {
	env := newReconcileSigningKeyshareSecretPointersEnv(t)

	keyshareID := env.createKeyshare(t, reconcileKeyshareFixture{SecretVersion: new(int32(4))})
	createEphemeralSigningKeyshareSecret(t, env.ctx, env.ephemeralClient, keyshareID, 3, time.Now().Add(-time.Hour))

	outcomes := env.runTask(t)
	require.Equal(t, int64(1), outcomes["dangling"])
	require.Equal(t, int64(0), outcomes["dangling_with_main_fallback"])
	require.Equal(t, int64(1), outcomes["pass_complete"])
}

// The cohort purge_dangling_signing_keyshare_secrets is structurally blind to:
// with no ephemeral rows left, an ephemeral-side scan has nothing to examine.
func TestReconcileSigningKeyshareSecretPointers_ReportsKeyshareWithNoEphemeralRows(t *testing.T) {
	env := newReconcileSigningKeyshareSecretPointersEnv(t)

	env.createKeyshare(t, reconcileKeyshareFixture{SecretVersion: new(int32(0))})

	outcomes := env.runTask(t)
	require.Equal(t, int64(1), outcomes["dangling"])
	require.Equal(t, int64(1), outcomes["pass_complete"])
}

func TestReconcileSigningKeyshareSecretPointers_IgnoresResolvablePointer(t *testing.T) {
	env := newReconcileSigningKeyshareSecretPointersEnv(t)

	keyshareID := env.createKeyshare(t, reconcileKeyshareFixture{SecretVersion: new(int32(4))})
	createEphemeralSigningKeyshareSecret(t, env.ctx, env.ephemeralClient, keyshareID, 3, time.Now().Add(-2*time.Hour))
	createEphemeralSigningKeyshareSecret(t, env.ctx, env.ephemeralClient, keyshareID, 4, time.Now().Add(-time.Hour))

	outcomes := env.runTask(t)
	require.Equal(t, int64(0), outcomes["dangling"])
	require.Equal(t, int64(0), outcomes["dangling_with_main_fallback"])
	require.Equal(t, int64(1), outcomes["pass_complete"])
}

// Both keyshares lost the same ephemeral secret, but only the one whose
// secret_share column has already been cleared is unusable today. The attribute
// separates "broken now" from "breaks once clear_signing_keyshare_secret_shares
// reaches it".
func TestReconcileSigningKeyshareSecretPointers_SeparatesKeysharesStillCoveredByMainFallback(t *testing.T) {
	env := newReconcileSigningKeyshareSecretPointersEnv(t)

	env.createKeyshare(t, reconcileKeyshareFixture{SecretVersion: new(int32(2)), KeepMainSecret: true})
	env.createKeyshare(t, reconcileKeyshareFixture{SecretVersion: new(int32(2))})

	outcomes := env.runTask(t)
	require.Equal(t, int64(1), outcomes["dangling"])
	require.Equal(t, int64(1), outcomes["dangling_with_main_fallback"])
}

// A keyshare with a null secret_version has no pointer to dangle: its secret
// lives in the main DB and the legacy read path serves it.
func TestReconcileSigningKeyshareSecretPointers_SkipsKeysharesWithoutSecretVersion(t *testing.T) {
	env := newReconcileSigningKeyshareSecretPointersEnv(t)

	env.createKeyshare(t, reconcileKeyshareFixture{KeepMainSecret: true})

	outcomes := env.runTask(t)
	require.Equal(t, int64(0), outcomes["dangling"])
	require.Equal(t, int64(0), outcomes["dangling_with_main_fallback"])
}

// A keyshare updated inside the grace period may simply be mid-rotation, and
// reporting it would be a false positive indistinguishable from the real bug.
func TestReconcileSigningKeyshareSecretPointers_SkipsKeysharesUpdatedInsideGracePeriod(t *testing.T) {
	env := newReconcileSigningKeyshareSecretPointersEnv(t)

	env.createKeyshare(t, reconcileKeyshareFixture{
		SecretVersion: new(int32(4)),
		UpdateTime:    time.Now(),
	})

	outcomes := env.runTask(t)
	require.Equal(t, int64(0), outcomes["dangling"])
	require.Equal(t, int64(0), outcomes["dangling_with_main_fallback"])
}

// The scan ends its main-DB transaction after each page so the confirmation
// re-read opens a fresh one, which is what keeps the guard sound above READ
// COMMITTED. Forcing several pages exercises that commit-and-reopen cycle: a
// client captured across the commit would be bound to a closed transaction and
// fail here rather than in prod.
func TestReconcileSigningKeyshareSecretPointers_DetectsAcrossMultiplePages(t *testing.T) {
	env := newReconcileSigningKeyshareSecretPointersEnv(t)

	for i := range 3 {
		keyshareID := env.createKeyshare(t, reconcileKeyshareFixture{SecretVersion: new(int32(0))})
		createEphemeralSigningKeyshareSecret(t, env.ctx, env.ephemeralClient, keyshareID, 0, time.Now().Add(time.Duration(-60+i)*time.Minute))
	}
	env.createKeyshare(t, reconcileKeyshareFixture{SecretVersion: new(int32(9))})

	outcomes := env.runTaskWithKnobs(t, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobReconcileSigningKeyshareSecretPointersBatchSize: 1,
	}))
	require.Equal(t, int64(1), outcomes["dangling"])
	require.Equal(t, int64(1), outcomes["pass_complete"])
}

func TestReconcileSigningKeyshareSecretPointers_NoOpWithoutEphemeralSession(t *testing.T) {
	t.Parallel()
	mainClient := db.NewTestSQLiteClient(t)
	cfg := sparktesting.TestConfig(t)

	reconcileTask := getScheduledTaskByName(t, "reconcile_signing_keyshare_secret_pointers")
	err := reconcileTask.RunOnce(t.Context(), cfg, mainClient, nil, knobs.NewEmptyFixedKnobs())
	require.NoError(t, err)
}

// Cursor handoff is not observable at the task boundary: the cursor round-trips
// through memcache, and with no cache configured every run restarts at the
// oldest row. These two scans stand in for two scheduled runs sharing a cursor.
func TestReconcileSigningKeyshareSecretPointersScan_CursorAdvancesUntilDanglingKeyshareFound(t *testing.T) {
	t.Parallel()
	env := newReconcileSigningKeyshareSecretPointersEnv(t)

	maxScanCount := 4
	for i := range maxScanCount {
		keyshareID := env.createKeyshare(t, reconcileKeyshareFixture{SecretVersion: new(int32(0))})
		createEphemeralSigningKeyshareSecret(t, env.ctx, env.ephemeralClient, keyshareID, 0, time.Now().Add(time.Duration(-60+i)*time.Minute))
	}
	danglingID := env.createKeyshare(t, reconcileKeyshareFixture{SecretVersion: new(int32(7))})

	cutoffTime := time.Now()
	firstResult, err := reconcileSigningKeyshareSecretPointersScan(env.ctx, cutoffTime, 2, maxScanCount, nil)
	require.NoError(t, err)
	require.Equal(t, maxScanCount, firstResult.ScannedCount)
	require.Empty(t, firstResult.Dangling)
	require.NotNil(t, firstResult.NextCursor, "cursor should advance when the budget is exhausted mid-table")

	secondResult, err := reconcileSigningKeyshareSecretPointersScan(env.ctx, cutoffTime, 2, maxScanCount, firstResult.NextCursor)
	require.NoError(t, err)
	require.Equal(t, 1, secondResult.ScannedCount)
	require.Equal(t, []danglingSigningKeysharePointer{{
		SigningKeyshareID: danglingID,
		SecretVersion:     7,
	}}, secondResult.Dangling)
	require.Nil(t, secondResult.NextCursor, "cursor should reset once the scan reaches the end of the eligible data")
}

// pass_complete is derived from a nil NextCursor, so a scan that stops exactly on
// its budget must not look like a completed pass — otherwise a cursor that never
// persists would still report full coverage.
func TestReconcileSigningKeyshareSecretPointersScan_BudgetExhaustedOnFinalRowAdvancesCursor(t *testing.T) {
	t.Parallel()
	env := newReconcileSigningKeyshareSecretPointersEnv(t)

	for i := range 2 {
		keyshareID := env.createKeyshare(t, reconcileKeyshareFixture{SecretVersion: new(int32(0))})
		createEphemeralSigningKeyshareSecret(t, env.ctx, env.ephemeralClient, keyshareID, 0, time.Now().Add(time.Duration(-60+i)*time.Minute))
	}

	result, err := reconcileSigningKeyshareSecretPointersScan(env.ctx, time.Now(), 2, 2, nil)
	require.NoError(t, err)
	require.Equal(t, 2, result.ScannedCount)
	require.NotNil(t, result.NextCursor)
}

type reconcileSigningKeyshareSecretPointersEnv struct {
	ctx             context.Context
	config          *so.Config
	mainClient      *ent.Client
	ephemeralClient *entephemeral.Client
}

func newReconcileSigningKeyshareSecretPointersEnv(t *testing.T) *reconcileSigningKeyshareSecretPointersEnv {
	t.Helper()

	mainClient := db.NewTestSQLiteClient(t)
	ephemeralClient := entephemeraltest.Open(t, "sqlite3", fmt.Sprintf(
		"file:%s?mode=memory&_fk=1",
		strings.ReplaceAll(t.Name(), "/", "_"),
	))

	t.Cleanup(func() {
		require.NoError(t, ephemeralClient.Close())
		require.NoError(t, mainClient.Close())
	})

	ctx := ent.Inject(t.Context(), db.NewReadOnlySession(t.Context(), mainClient))
	ctx = entephemeral.Inject(ctx, db.NewReadOnlyEphemeralSession(t.Context(), ephemeralClient))

	return &reconcileSigningKeyshareSecretPointersEnv{
		ctx:             ctx,
		config:          sparktesting.TestConfig(t),
		mainClient:      mainClient,
		ephemeralClient: ephemeralClient,
	}
}

// runTask runs the scheduled task the way the scheduler does and returns the
// outcome counts it emitted. Deltas are taken because the counter is cumulative
// across the package's test binary.
func (e *reconcileSigningKeyshareSecretPointersEnv) runTask(t *testing.T) map[string]int64 {
	t.Helper()
	return e.runTaskWithKnobs(t, knobs.NewEmptyFixedKnobs())
}

func (e *reconcileSigningKeyshareSecretPointersEnv) runTaskWithKnobs(t *testing.T, knobsService knobs.Knobs) map[string]int64 {
	t.Helper()

	before := collectSigningKeysharePointerReconciliationOutcomes(t)
	reconcileTask := getScheduledTaskByName(t, "reconcile_signing_keyshare_secret_pointers")
	require.NoError(t, reconcileTask.RunOnce(t.Context(), e.config, e.mainClient, e.ephemeralClient, knobsService))
	after := collectSigningKeysharePointerReconciliationOutcomes(t)

	delta := make(map[string]int64, len(after))
	for outcome, count := range after {
		delta[outcome] = count - before[outcome]
	}
	return delta
}

type reconcileKeyshareFixture struct {
	// SecretVersion is the main-DB pointer into the ephemeral store; nil leaves
	// the keyshare on the legacy main-DB-only path.
	SecretVersion *int32
	// UpdateTime defaults to well outside the grace period, so a fixture is in
	// scope for the scan unless a test deliberately makes it recent.
	UpdateTime time.Time
	// KeepMainSecret populates the legacy secret_share column. Cleared by default,
	// which is the shape a dangling pointer actually breaks.
	KeepMainSecret bool
}

func (e *reconcileSigningKeyshareSecretPointersEnv) createKeyshare(t *testing.T, fixture reconcileKeyshareFixture) uuid.UUID {
	t.Helper()

	secret := keys.GeneratePrivateKey()
	updateTime := fixture.UpdateTime
	if updateTime.IsZero() {
		updateTime = time.Now().Add(-reconcileSigningKeyshareSecretPointersGracePeriod - time.Hour)
	}

	create := e.mainClient.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetPublicShares(map[string]keys.Public{}).
		SetPublicKey(secret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		SetUpdateTime(updateTime)
	if fixture.SecretVersion != nil {
		create = create.SetSecretVersion(*fixture.SecretVersion)
	}
	if fixture.KeepMainSecret {
		create = create.SetSecretShare(secret)
	}

	row, err := create.Save(e.ctx)
	require.NoError(t, err)
	return row.ID
}

func collectSigningKeysharePointerReconciliationOutcomes(t *testing.T) map[string]int64 {
	t.Helper()

	var rm md.ResourceMetrics
	require.NoError(t, reconcileSigningKeyshareSecretPointersMetricReader.Collect(t.Context(), &rm))

	counts := map[string]int64{}
	for _, scopeMetrics := range rm.ScopeMetrics {
		for _, m := range scopeMetrics.Metrics {
			if m.Name != "spark_so_task_signing_keyshare_pointer_reconciliation_outcomes_total" {
				continue
			}
			sum, ok := m.Data.(md.Sum[int64])
			require.True(t, ok, "reconciliation outcomes should be an int64 sum")
			for _, dataPoint := range sum.DataPoints {
				outcome, ok := dataPoint.Attributes.Value("outcome")
				require.True(t, ok, "data point missing outcome attribute")
				counts[outcome.AsString()] += dataPoint.Value
			}
		}
	}
	return counts
}
