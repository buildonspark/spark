package task

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// A cursor that silently fails to persist degrades a scan to "head of the table
// forever", which is invisible in the task's logs, so the URI parsing that decides
// whether it persists at all is worth pinning down directly.
func TestParseScanCursorMemcacheAddrs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		cacheURI      string
		expectedAddrs []string
	}{
		{name: "empty", cacheURI: "", expectedAddrs: []string{}},
		{name: "scheme only", cacheURI: "memcaches://", expectedAddrs: []string{}},
		{name: "single host without scheme", cacheURI: "host:11211", expectedAddrs: []string{"host:11211"}},
		{name: "single host with scheme", cacheURI: "memcaches://host:11211", expectedAddrs: []string{"host:11211"}},
		{name: "insecure scheme", cacheURI: "memcache://host:11211", expectedAddrs: []string{"host:11211"}},
		{
			name:          "multiple hosts",
			cacheURI:      "memcaches://host:11211,host2:11211",
			expectedAddrs: []string{"host:11211", "host2:11211"},
		},
		{
			name:          "multiple hosts with whitespace and empty entries",
			cacheURI:      "memcaches://host:11211, ,host2:11211 ",
			expectedAddrs: []string{"host:11211", "host2:11211"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.expectedAddrs, parseScanCursorMemcacheAddrs(test.cacheURI))
		})
	}
}

func TestPositiveIntKnob(t *testing.T) {
	t.Parallel()

	const knob = "spark.so.test_positive_int_knob"
	tests := []struct {
		name          string
		knobValue     *float64
		expectedValue int
		expectedOK    bool
	}{
		{name: "unset falls back to the default", expectedValue: 1000, expectedOK: true},
		{name: "positive override", knobValue: new(float64(25)), expectedValue: 25, expectedOK: true},
		{name: "zero skips the run", knobValue: new(float64(0)), expectedOK: false},
		{name: "negative skips the run", knobValue: new(float64(-1)), expectedOK: false},
		// Truncation toward zero means a fractional value below 1 is not a small
		// batch, it is no batch at all, so it has to be rejected rather than floored.
		{name: "fraction below one skips the run", knobValue: new(float64(0.5)), expectedOK: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			values := map[string]float64{}
			if test.knobValue != nil {
				values[knob] = *test.knobValue
			}

			value, ok := positiveIntKnob(zap.NewNop().Sugar(), knobs.NewFixedKnobs(values), "test_task", knob, 1000)
			require.Equal(t, test.expectedOK, ok)
			require.Equal(t, test.expectedValue, value)
		})
	}
}

// persist owns the save-or-delete policy for every scan task, so its branches are
// worth pinning directly. The warn-only error handling is only observable through
// the logger, which is why the cursor is built by hand here rather than through
// newScanCursor.
func TestScanCursorPersist(t *testing.T) {
	t.Parallel()

	next := uuid.New()

	t.Run("no cache configured is silent", func(t *testing.T) {
		t.Parallel()
		core, logs := observer.New(zapcore.WarnLevel)
		cursor := &scanCursor{taskName: "test_task", key: "test_key", mc: nil, sugar: zap.New(core).Sugar()}

		cursor.persist(&next)
		cursor.persist(nil)
		require.Zero(t, logs.Len(), "a task with no cursor cache must not warn every run")
	})

	t.Run("unreachable cache warns per branch", func(t *testing.T) {
		t.Parallel()
		// Port 1 resolves but refuses, so Set and Delete fail without needing a server.
		mc, err := newScanCursorMemcacheClient("127.0.0.1:1")
		require.NoError(t, err)

		core, logs := observer.New(zapcore.WarnLevel)
		cursor := &scanCursor{taskName: "test_task", key: "test_key", mc: mc, sugar: zap.New(core).Sugar()}

		cursor.persist(&next)
		require.Equal(t, 1, logs.FilterMessageSnippet("failed to persist cursor").Len())

		cursor.persist(nil)
		require.Equal(t, 1, logs.FilterMessageSnippet("failed to clear cursor").Len())
	})
}

// The cursor key is derived from the task name, so a rename silently orphans the
// persisted cursor and the next run restarts at the oldest row. These are the keys
// in use before the derivation replaced explicit per-task prefix constants; they
// must not drift.
func TestScanCursorKeyIsStable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		taskName    string
		expectedKey string
	}{
		{taskName: "purge_dangling_signing_keyshare_secrets", expectedKey: "purge_dangling_signing_keyshare_secrets_cursor:0"},
		{taskName: "reconcile_signing_keyshare_secret_pointers", expectedKey: "reconcile_signing_keyshare_secret_pointers_cursor:0"},
	}
	for _, test := range tests {
		t.Run(test.taskName, func(t *testing.T) {
			t.Parallel()
			cursor := newScanCursor(t.Context(), test.taskName, &so.Config{})
			require.Equal(t, test.expectedKey, cursor.key)
		})
	}
}

// A CacheURI yielding no server must produce no client, because nil is the value
// the load/save/delete helpers read as "no cache available". A client over an empty
// server list instead fails every operation with ErrNoServers, which those helpers
// would surface as a cursor warning on every run.
//
// Addresses are literal IPs: ServerList.SetServers resolves eagerly, so a hostname
// here would make the test depend on DNS in the environment running it.
func TestNewScanCursorMemcacheClient_NilWithoutServers(t *testing.T) {
	t.Parallel()

	for _, cacheURI := range []string{"", "memcaches://", "memcache://", " , "} {
		client, err := newScanCursorMemcacheClient(cacheURI)
		require.NoError(t, err, "a URI with no server is not an error, just no cache")
		require.Nil(t, client, "no configured server must yield no client: %q", cacheURI)
	}

	client, err := newScanCursorMemcacheClient("memcaches://127.0.0.1:11211")
	require.NoError(t, err)
	require.NotNil(t, client)
}

// An unresolvable address must surface as an error naming that address, so an
// operator can tell which host to fix. Resolution is all-or-nothing, so one bad
// entry in a multi-host URI costs the whole run.
//
// The two addresses are deliberately not substrings of one another, and the healthy
// one is asserted absent. Without that, an error that merely echoed the configured
// list would satisfy the assertion whether or not resolution actually attributed
// the failure.
func TestNewScanCursorMemcacheClient_ReportsUnresolvableAddress(t *testing.T) {
	t.Parallel()

	const (
		healthyAddr      = "127.0.0.1:11211"
		unresolvableAddr = "192.0.2.1:1:2"
	)

	client, err := newScanCursorMemcacheClient("memcaches://" + healthyAddr + "," + unresolvableAddr)
	require.Error(t, err)
	require.Nil(t, client, "an unusable server list must yield no client")
	require.Contains(t, err.Error(), unresolvableAddr, "the error must name the address that failed to resolve")
	require.NotContains(t, err.Error(), healthyAddr, "the error must attribute the failure, not echo every configured address")
}
