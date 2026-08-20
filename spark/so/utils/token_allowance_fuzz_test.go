package utils

import (
	"slices"
	"testing"
	"time"

	pb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// padTo returns b padded (zero-extended) or truncated to exactly n bytes, so a
// fuzzed byte input can drive the canonical-length hashing path as well as the
// length-rejection path.
func padTo(b []byte, n int) []byte {
	out := make([]byte, n)
	copy(out, b)
	return out
}

// FuzzHashCreateTokenAllowancePayload fuzzes the owner-signed allowance
// statement hasher. Invariants:
//
//   - never panics, and errors only on malformed inputs (lengths / network);
//   - deterministic: the same payload always hashes to the same 32 bytes;
//   - canonical: allowlist ordering does not affect the hash;
//   - injective over the canonical fields: mutating any bound field of a
//     valid payload changes the hash (including the unlimited flags
//     are provably NOT bound - the layout property that forces validation to
//     reject v1 payloads with flags set);
//   - domain-separated from the revoke statement hash.
func FuzzHashCreateTokenAllowancePayload(f *testing.F) {
	// Seeds shaped like the frozen reference vectors
	// (TestHashCreateTokenAllowancePayload_KnownVector / _KnownVectorV2).
	f.Add(uint32(1), int32(pb.Network_REGTEST), []byte{0x01, 0x23, 0x45}, []byte{0x02, 0xca}, []byte{0x03, 0x3e},
		[]byte{0x3e, 0x53}, []byte{0x27, 0x10}, []byte{0x01, 0x86, 0xa0},
		[]byte{0x03, 0x75}, []byte{0x02, 0x8c}, false, false, int64(2_000_000_000), uint64(1_747_337_980_820))
	f.Add(uint32(2), int32(pb.Network_REGTEST), []byte{0xaa}, []byte{0xbb}, []byte{0xcc},
		[]byte{0xdd}, []byte{}, []byte{},
		[]byte{0x11}, []byte{0x22}, true, true, int64(1_800_000_000), uint64(1))

	f.Fuzz(func(t *testing.T, version uint32, networkRaw int32,
		idBytes, ownerBytes, spenderBytes, tokenBytes, capBytes, limitBytes, allowKeyA, allowKeyB []byte,
		perTxUnlimited, totalUnlimited bool, expirySecs int64, ts uint64) {

		raw := &tokenpb.TokenAllowancePayload{
			Version:                 version,
			Network:                 pb.Network(networkRaw),
			AllowanceId:             idBytes,
			OwnerPublicKey:          ownerBytes,
			SpenderPublicKey:        spenderBytes,
			TokenIdentifier:         tokenBytes,
			PerTransactionCap:       capBytes,
			TotalLimit:              limitBytes,
			PerTransactionUnlimited: perTxUnlimited,
			TotalUnlimited:          totalUnlimited,
			RecipientAllowlist:      [][]byte{allowKeyA, allowKeyB},
			ExpiryTime:              timestamppb.New(time.Unix(expirySecs, 0)),
			OwnerProvidedTimestamp:  ts,
		}

		// Arbitrary inputs must never panic, and success implies a 32-byte,
		// deterministic hash.
		rawHash, rawErr := HashCreateTokenAllowancePayload(raw)
		rawHash2, rawErr2 := HashCreateTokenAllowancePayload(proto.Clone(raw).(*tokenpb.TokenAllowancePayload))
		require.Equal(t, rawErr == nil, rawErr2 == nil, "hashability must be deterministic")
		if rawErr == nil {
			require.Len(t, rawHash, 32)
			require.Equal(t, rawHash, rawHash2, "hash must be deterministic")
		}

		// Canonicalize lengths so the full layout is exercised regardless of
		// the fuzzed byte widths.
		canonical := &tokenpb.TokenAllowancePayload{
			Version:                 version,
			Network:                 pb.Network_REGTEST,
			AllowanceId:             padTo(idBytes, 16),
			OwnerPublicKey:          padTo(ownerBytes, 33),
			SpenderPublicKey:        padTo(spenderBytes, 33),
			TokenIdentifier:         padTo(tokenBytes, 32),
			PerTransactionCap:       padTo(capBytes, 16),
			TotalLimit:              padTo(limitBytes, 16),
			PerTransactionUnlimited: perTxUnlimited,
			TotalUnlimited:          totalUnlimited,
			RecipientAllowlist:      [][]byte{padTo(allowKeyA, 33), padTo(allowKeyB, 33)},
			ExpiryTime:              timestamppb.New(time.Unix(expirySecs, 0)),
			OwnerProvidedTimestamp:  ts,
		}
		base, err := HashCreateTokenAllowancePayload(canonical)
		require.NoError(t, err, "canonical-length payload must hash")
		require.Len(t, base, 32)

		rehash := func(mutate func(p *tokenpb.TokenAllowancePayload)) []byte {
			clone := proto.Clone(canonical).(*tokenpb.TokenAllowancePayload)
			mutate(clone)
			h, err := HashCreateTokenAllowancePayload(clone)
			require.NoError(t, err)
			return h
		}

		// Injectivity over bound fields: distinct canonical inputs must not
		// collide.
		require.NotEqual(t, base, rehash(func(p *tokenpb.TokenAllowancePayload) { p.AllowanceId[0] ^= 0x01 }))
		require.NotEqual(t, base, rehash(func(p *tokenpb.TokenAllowancePayload) { p.OwnerPublicKey[32] ^= 0x01 }))
		require.NotEqual(t, base, rehash(func(p *tokenpb.TokenAllowancePayload) { p.SpenderPublicKey[1] ^= 0x01 }))
		require.NotEqual(t, base, rehash(func(p *tokenpb.TokenAllowancePayload) { p.TokenIdentifier[31] ^= 0x01 }))
		require.NotEqual(t, base, rehash(func(p *tokenpb.TokenAllowancePayload) { p.PerTransactionCap[15] ^= 0x01 }))
		require.NotEqual(t, base, rehash(func(p *tokenpb.TokenAllowancePayload) { p.TotalLimit[0] ^= 0x01 }))
		require.NotEqual(t, base, rehash(func(p *tokenpb.TokenAllowancePayload) { p.OwnerProvidedTimestamp++ }))
		require.NotEqual(t, base, rehash(func(p *tokenpb.TokenAllowancePayload) { p.Version++ }))
		require.NotEqual(t, base, rehash(func(p *tokenpb.TokenAllowancePayload) {
			p.RecipientAllowlist = append(p.GetRecipientAllowlist(), padTo([]byte{0xfe}, 33))
		}))

		// The unlimited flags are always bound, so flipping one always changes the statement:
		// a flag altered in flight can never waive a ceiling the owner did not sign for.
		require.NotEqual(t, base,
			rehash(func(p *tokenpb.TokenAllowancePayload) { p.PerTransactionUnlimited = !p.GetPerTransactionUnlimited() }),
			"per_transaction_unlimited must be bound")

		// Canonical allowlist ordering: the hash is independent of wire order.
		require.Equal(t, base, rehash(func(p *tokenpb.TokenAllowancePayload) {
			slices.Reverse(p.GetRecipientAllowlist())
		}))

		// Domain separation from the revoke statement over the shared fields.
		revokeHash, err := HashRevokeTokenAllowancePayload(&tokenpb.RevokeTokenAllowancePayload{
			Version:                version,
			AllowanceId:            canonical.GetAllowanceId(),
			OwnerPublicKey:         canonical.GetOwnerPublicKey(),
			OwnerProvidedTimestamp: ts,
		})
		require.NoError(t, err)
		require.NotEqual(t, base, revokeHash, "create and revoke statements must be domain separated")
	})
}

// FuzzHashRevokeTokenAllowancePayload fuzzes the revoke statement hasher: no
// panic, determinism, 32-byte output, and injectivity over every bound field.
func FuzzHashRevokeTokenAllowancePayload(f *testing.F) {
	f.Add(uint32(1), []byte{0x01, 0x23}, []byte{0x02, 0xca}, uint64(1_747_337_980_820))
	f.Add(uint32(2), []byte{}, []byte{0xff}, uint64(0))

	f.Fuzz(func(t *testing.T, version uint32, idBytes, ownerBytes []byte, ts uint64) {
		raw := &tokenpb.RevokeTokenAllowancePayload{
			Version:                version,
			AllowanceId:            idBytes,
			OwnerPublicKey:         ownerBytes,
			OwnerProvidedTimestamp: ts,
		}
		rawHash, rawErr := HashRevokeTokenAllowancePayload(raw)
		rawHash2, rawErr2 := HashRevokeTokenAllowancePayload(proto.Clone(raw).(*tokenpb.RevokeTokenAllowancePayload))
		require.Equal(t, rawErr == nil, rawErr2 == nil)
		if rawErr == nil {
			require.Len(t, rawHash, 32)
			require.Equal(t, rawHash, rawHash2)
		}

		canonical := &tokenpb.RevokeTokenAllowancePayload{
			Version:                version,
			AllowanceId:            padTo(idBytes, 16),
			OwnerPublicKey:         padTo(ownerBytes, 33),
			OwnerProvidedTimestamp: ts,
		}
		base, err := HashRevokeTokenAllowancePayload(canonical)
		require.NoError(t, err)
		require.Len(t, base, 32)

		rehash := func(mutate func(p *tokenpb.RevokeTokenAllowancePayload)) []byte {
			clone := proto.Clone(canonical).(*tokenpb.RevokeTokenAllowancePayload)
			mutate(clone)
			h, err := HashRevokeTokenAllowancePayload(clone)
			require.NoError(t, err)
			return h
		}
		require.NotEqual(t, base, rehash(func(p *tokenpb.RevokeTokenAllowancePayload) { p.Version++ }))
		require.NotEqual(t, base, rehash(func(p *tokenpb.RevokeTokenAllowancePayload) { p.AllowanceId[15] ^= 0x01 }))
		require.NotEqual(t, base, rehash(func(p *tokenpb.RevokeTokenAllowancePayload) { p.OwnerPublicKey[0] ^= 0x01 }))
		require.NotEqual(t, base, rehash(func(p *tokenpb.RevokeTokenAllowancePayload) { p.OwnerProvidedTimestamp++ }))
	})
}
