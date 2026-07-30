package task

import (
	"testing"

	"github.com/stretchr/testify/require"
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
