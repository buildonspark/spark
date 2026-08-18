package utils

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Valid compressed secp256k1 public keys reused across the allowance tests.
const (
	allowanceOwnerKeyHex      = "02ca75659458529755b77663f18282f4aa130313e098fac40deffb1208207a2ffe"
	allowanceSpenderKeyHex    = "033e40d72117ee89f7bda15d2b3d779843e6721e8e4c5078c192b50fb3782de2f5"
	allowanceRecipient1Hex    = "0375a9121cd7c3684ca1941978cc0dc42ce316fddf70261643f17ba3eeca6d10f2"
	allowanceRecipient2Hex    = "028c094a432d46a0ac95349d792c2e3730bd60c29188db716f56a99e39b95338b4"
	allowanceOtherKeyHex      = "037f699d5b77668b847d92a3d4ad199af4d11ebc2069cf78d7694b08be0a6b381d"
	allowanceIDHex            = "0123456789abcdef0123456789abcdef"
	allowanceTokenIDHex       = "3e534a8d9798fe5e20516f9b1aa05f5d78d718ece893e8af89d678c3d88f2451"
	allowancePerTxCapHex      = "00000000000000000000000000002710" // 10000
	allowanceTotalLimitHex    = "000000000000000000000000000186a0" // 100000
	allowanceZeroUint128Hex   = "00000000000000000000000000000000"
	allowanceProvidedTsMillis = 1747337980820
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

func pubKeyBytes(t *testing.T, hexKey string) []byte {
	t.Helper()
	return keys.MustParsePublicKeyHex(hexKey).Serialize()
}

// deterministicCreatePayload returns a fully populated allowance payload with fixed field values
// so its statement hash is stable across runs. Used by the hashing tests.
func deterministicCreatePayload(t *testing.T) *tokenpb.TokenAllowancePayload {
	t.Helper()
	return &tokenpb.TokenAllowancePayload{
		Version:                1,
		AllowanceId:            mustHex(t, allowanceIDHex),
		OwnerPublicKey:         pubKeyBytes(t, allowanceOwnerKeyHex),
		SpenderPublicKey:       pubKeyBytes(t, allowanceSpenderKeyHex),
		TokenIdentifier:        mustHex(t, allowanceTokenIDHex),
		PerTransactionCap:      mustHex(t, allowancePerTxCapHex),
		TotalLimit:             mustHex(t, allowanceTotalLimitHex),
		RecipientAllowlist:     [][]byte{pubKeyBytes(t, allowanceRecipient1Hex), pubKeyBytes(t, allowanceRecipient2Hex)},
		ExpiryTime:             timestamppb.New(time.Unix(2000000000, 0)),
		Network:                pb.Network_REGTEST,
		OwnerProvidedTimestamp: allowanceProvidedTsMillis,
	}
}

// validCreatePayload returns a payload that passes ValidateTokenAllowancePayload (future expiry).
func validCreatePayload(t *testing.T) *tokenpb.TokenAllowancePayload {
	t.Helper()
	payload := deterministicCreatePayload(t)
	payload.ExpiryTime = timestamppb.New(time.Now().Add(24 * time.Hour))
	return payload
}

func TestHashCreateTokenAllowancePayload_Deterministic(t *testing.T) {
	payload := deterministicCreatePayload(t)

	first, err := HashCreateTokenAllowancePayload(payload)
	require.NoError(t, err)
	second, err := HashCreateTokenAllowancePayload(deterministicCreatePayload(t))
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.Len(t, first, 32)
}

func TestHashCreateTokenAllowancePayload_EveryFieldChangesHash(t *testing.T) {
	base := deterministicCreatePayload(t)
	baseHash, err := HashCreateTokenAllowancePayload(base)
	require.NoError(t, err)

	mutations := map[string]func(*tokenpb.TokenAllowancePayload){
		"version":            func(p *tokenpb.TokenAllowancePayload) { p.Version = 2 },
		"network":            func(p *tokenpb.TokenAllowancePayload) { p.Network = pb.Network_MAINNET },
		"allowance_id":       func(p *tokenpb.TokenAllowancePayload) { p.AllowanceId = mustHex(t, "ffffffffffffffffffffffffffffffff") },
		"owner_public_key":   func(p *tokenpb.TokenAllowancePayload) { p.OwnerPublicKey = pubKeyBytes(t, allowanceOtherKeyHex) },
		"spender_public_key": func(p *tokenpb.TokenAllowancePayload) { p.SpenderPublicKey = pubKeyBytes(t, allowanceOtherKeyHex) },
		"token_identifier": func(p *tokenpb.TokenAllowancePayload) {
			p.TokenIdentifier = mustHex(t, "00534a8d9798fe5e20516f9b1aa05f5d78d718ece893e8af89d678c3d88f2451")
		},
		"per_transaction_cap": func(p *tokenpb.TokenAllowancePayload) {
			p.PerTransactionCap = mustHex(t, "00000000000000000000000000002711")
		},
		"total_limit": func(p *tokenpb.TokenAllowancePayload) { p.TotalLimit = mustHex(t, "000000000000000000000000000186a1") },
		"recipient_allowlist": func(p *tokenpb.TokenAllowancePayload) {
			p.RecipientAllowlist = [][]byte{pubKeyBytes(t, allowanceRecipient1Hex)}
		},
		"expiry_time":              func(p *tokenpb.TokenAllowancePayload) { p.ExpiryTime = timestamppb.New(time.Unix(2000000001, 0)) },
		"owner_provided_timestamp": func(p *tokenpb.TokenAllowancePayload) { p.OwnerProvidedTimestamp = allowanceProvidedTsMillis + 1 },
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			mutated := proto.CloneOf(base)
			mutate(mutated)
			mutatedHash, err := HashCreateTokenAllowancePayload(mutated)
			require.NoError(t, err)
			assert.NotEqual(t, baseHash, mutatedHash, "mutating %s must change the hash", name)
		})
	}
}

func TestHashCreateTokenAllowancePayload_AllowlistOrderCanonical(t *testing.T) {
	ascending := deterministicCreatePayload(t)
	ascending.RecipientAllowlist = [][]byte{pubKeyBytes(t, allowanceRecipient1Hex), pubKeyBytes(t, allowanceRecipient2Hex)}

	descending := deterministicCreatePayload(t)
	descending.RecipientAllowlist = [][]byte{pubKeyBytes(t, allowanceRecipient2Hex), pubKeyBytes(t, allowanceRecipient1Hex)}

	ascendingHash, err := HashCreateTokenAllowancePayload(ascending)
	require.NoError(t, err)
	descendingHash, err := HashCreateTokenAllowancePayload(descending)
	require.NoError(t, err)

	assert.Equal(t, ascendingHash, descendingHash, "allowlist ordering must not affect the hash")
}

func TestValidateTokenAllowancePayload_RejectsSpenderEqualsOwner(t *testing.T) {
	supportedNetworks := []btcnetwork.Network{btcnetwork.Regtest}
	payload := validCreatePayload(t)
	payload.SpenderPublicKey = pubKeyBytes(t, allowanceOwnerKeyHex)

	err := ValidateTokenAllowancePayload(payload, supportedNetworks)
	require.Error(t, err)
	assert.ErrorContains(t, err, "spender_public_key must differ from owner_public_key")
}

// TestValidateTokenAllowancePayload_RejectsPolicyViolations pins the remaining
// load-bearing policy rejections: unsupported payload versions, missing or
// non-future expiry, and cap/limit inversions (including the zero cases).
func TestValidateTokenAllowancePayload_RejectsPolicyViolations(t *testing.T) {
	supportedNetworks := []btcnetwork.Network{btcnetwork.Regtest}

	tests := []struct {
		name           string
		mutate         func(*tokenpb.TokenAllowancePayload)
		errorSubstring string
	}{
		{
			name:           "unsupported version",
			mutate:         func(p *tokenpb.TokenAllowancePayload) { p.Version = 99 },
			errorSubstring: "unsupported token allowance version",
		},
		{
			name:           "missing expiry",
			mutate:         func(p *tokenpb.TokenAllowancePayload) { p.ExpiryTime = nil },
			errorSubstring: "expiry_time is required",
		},
		{
			name: "expiry in the past",
			mutate: func(p *tokenpb.TokenAllowancePayload) {
				p.ExpiryTime = timestamppb.New(time.Now().Add(-time.Minute))
			},
			errorSubstring: "expiry_time must be in the future",
		},
		{
			name: "per-transaction cap exceeds total limit",
			mutate: func(p *tokenpb.TokenAllowancePayload) {
				p.PerTransactionCap = mustHex(t, "000000000000000000000000000186a0")
				p.TotalLimit = mustHex(t, "00000000000000000000000000002710")
			},
			errorSubstring: "per_transaction_cap must not exceed total_limit",
		},
		{
			name: "zero per-transaction cap",
			mutate: func(p *tokenpb.TokenAllowancePayload) {
				p.PerTransactionCap = mustHex(t, allowanceZeroUint128Hex)
			},
			errorSubstring: "per_transaction_cap must be greater than 0",
		},
		{
			name: "zero total limit",
			mutate: func(p *tokenpb.TokenAllowancePayload) {
				p.TotalLimit = mustHex(t, allowanceZeroUint128Hex)
			},
			errorSubstring: "total_limit must be greater than 0",
		},
		{
			name: "malformed allowance_id length",
			mutate: func(p *tokenpb.TokenAllowancePayload) {
				p.AllowanceId = mustHex(t, "0102030405060708090a0b0c0d0e0f")
			},
			errorSubstring: "allowance_id must be 16 bytes",
		},
		{
			name: "malformed token_identifier length",
			mutate: func(p *tokenpb.TokenAllowancePayload) {
				p.TokenIdentifier = mustHex(t, "3e534a8d9798fe5e20516f9b1aa05f5d78d718ece893e8af89d678c3d88f24")
			},
			errorSubstring: "token_identifier must be 32 bytes",
		},
		{
			name: "malformed recipient allowlist entry",
			mutate: func(p *tokenpb.TokenAllowancePayload) {
				p.RecipientAllowlist = [][]byte{mustHex(t, "00")}
			},
			errorSubstring: "invalid recipient allowlist key",
		},
		{
			name: "recipient allowlist contains owner",
			mutate: func(p *tokenpb.TokenAllowancePayload) {
				p.RecipientAllowlist = [][]byte{pubKeyBytes(t, allowanceOwnerKeyHex)}
			},
			errorSubstring: "recipient allowlist must not contain the owner public key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := validCreatePayload(t)
			tt.mutate(payload)
			err := ValidateTokenAllowancePayload(payload, supportedNetworks)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.errorSubstring)
		})
	}
}

// TestHashCreateTokenAllowancePayload_KnownVector pins the wire format of the create statement
// hash. If this changes, the TypeScript SDK's implementation must be updated to match, and any
// already-signed allowances would be invalidated. The vector was computed once from
// deterministicCreatePayload; do not edit the expected value without a deliberate format change.
func TestHashCreateTokenAllowancePayload_KnownVector(t *testing.T) {
	const frozenHashHex = "1e0addb8b11fba061de99149900a1606fc9c314d9bbed8d65ccd81c10778f706"

	hash, err := HashCreateTokenAllowancePayload(deterministicCreatePayload(t))
	require.NoError(t, err)
	assert.Equal(t, frozenHashHex, hex.EncodeToString(hash))
}

// TestValidateTokenAllowancePayload_AllowlistSizeCap: the recipient allowlist
// is bounded at 256 entries; an oversized list is rejected before any per-key
// work (DoS hardening), while a list exactly at the cap validates.
func TestValidateTokenAllowancePayload_AllowlistSizeCap(t *testing.T) {
	supportedNetworks := []btcnetwork.Network{btcnetwork.Regtest}

	buildAllowlist := func(n int) [][]byte {
		allowlist := make([][]byte, 0, n)
		for range n {
			allowlist = append(allowlist, keys.GeneratePrivateKey().Public().Serialize())
		}
		return allowlist
	}

	atCap := validCreatePayload(t)
	atCap.RecipientAllowlist = buildAllowlist(maxRecipientAllowlistEntries)
	require.NoError(t, ValidateTokenAllowancePayload(atCap, supportedNetworks))

	overCap := validCreatePayload(t)
	overCap.RecipientAllowlist = buildAllowlist(maxRecipientAllowlistEntries + 1)
	err := ValidateTokenAllowancePayload(overCap, supportedNetworks)
	require.Error(t, err)
	require.ErrorContains(t, err, "recipient_allowlist has 257 entries")
}
