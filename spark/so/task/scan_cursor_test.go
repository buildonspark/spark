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

func TestNewScanCursorMemcacheClient_NilWithoutServers(t *testing.T) {
	t.Parallel()
	require.Nil(t, newScanCursorMemcacheClient(""), "no configured server must yield no client, which the cursor helpers treat as no cache available")
	require.NotNil(t, newScanCursorMemcacheClient("memcaches://host:11211"))
}
