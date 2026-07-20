package handler

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/require"
)

func TestSameTransferID(t *testing.T) {
	id := uuid.New()
	canonical := id.String()

	// Same UUID in different textual forms compares equal — the prepare op stores
	// the client's verbatim id while decision payloads canonicalize via
	// uuid.Parse(...).String().
	require.True(t, sameTransferID(canonical, canonical))
	require.True(t, sameTransferID(strings.ToUpper(canonical), canonical), "uppercase form is the same UUID")
	require.True(t, sameTransferID("{"+canonical+"}", canonical), "braced form is the same UUID")

	// Different UUIDs, and unparseable input on either side, are mismatches
	// (fail-closed).
	require.False(t, sameTransferID(canonical, uuid.NewString()))
	require.False(t, sameTransferID("not-a-uuid", canonical))
	require.False(t, sameTransferID(canonical, ""))
}

func TestSamePublicKey(t *testing.T) {
	key := keys.GeneratePrivateKey().Public()
	compressed := key.Serialize()
	uncompressed := key.ToBTCEC().SerializeUncompressed()

	// The same point in compressed vs uncompressed encodings compares equal.
	require.True(t, samePublicKey(compressed, compressed))
	require.True(t, samePublicKey(uncompressed, compressed), "uncompressed encoding is the same point")

	// A different key, and unparseable bytes on either side, are mismatches
	// (fail-closed).
	require.False(t, samePublicKey(compressed, keys.GeneratePrivateKey().Public().Serialize()))
	require.False(t, samePublicKey([]byte{0x01, 0x02}, compressed))
	require.False(t, samePublicKey(compressed, nil))
}
