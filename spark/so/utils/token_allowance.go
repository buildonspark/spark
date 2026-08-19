package utils

import (
	"bytes"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/hashstructure"
	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

const (
	// tokenAllowancePayloadVersion is the only currently supported allowance payload version.
	tokenAllowancePayloadVersion uint32 = 1
	// maxRecipientAllowlistEntries bounds recipient_allowlist. 256 recipients is
	// orders of magnitude beyond realistic merchant/payout sets while keeping the
	// stored row (~8.4KB of keys) and the per-spend allowlist scan bounded — an
	// unbounded list would let one owner-signed payload amplify storage and
	// validation cost on every SO. Mirrored by the proto max_items rule.
	maxRecipientAllowlistEntries = 256

	allowanceIDLength     = 16
	allowancePubKeyLength = 33
	tokenIdentifierLength = 32
	uint128Length         = 16
)

// createTokenAllowanceHashTag domain-separates the statement hash so a signature over an
// allowance creation can never be replayed as any other statement. Bump the version component
// on any layout change: the hash is what the owner signs.
var createTokenAllowanceHashTag = []string{"spark", "token", "create_token_allowance", "v1"}

// revokeTokenAllowanceHashTag domain-separates the statement hash so a signature over an
// allowance revocation can never be replayed as any other statement. Bump the version component
// on any layout change: the hash is what the owner signs.
var revokeTokenAllowanceHashTag = []string{"spark", "token", "revoke_token_allowance", "v1"}

// HashCreateTokenAllowancePayload returns the canonical statement hash the owner signs to
// authorize a token allowance. Values are added, in order: version, the lowercase network name,
// allowance_id, owner/spender public keys, token_identifier, per_transaction_cap, total_limit,
// the allowlist entry count followed by each 33-byte key sorted ascending bytewise, the expiry
// as Unix seconds (0 when unset), and owner_provided_timestamp.
//
// The layout is consumed cross-language: the TypeScript SDK MUST produce an identical hash. See
// TestHashCreateTokenAllowancePayload_KnownVector for the frozen reference vector.
func HashCreateTokenAllowancePayload(payload *tokenpb.TokenAllowancePayload) ([]byte, error) {
	if payload == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("token allowance payload cannot be nil"))
	}

	network, err := btcnetwork.FromProtoNetwork(payload.GetNetwork())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to convert network: %w", err))
	}

	if err := requireByteLen("allowance_id", payload.GetAllowanceId(), allowanceIDLength); err != nil {
		return nil, err
	}
	if err := requireByteLen("owner_public_key", payload.GetOwnerPublicKey(), allowancePubKeyLength); err != nil {
		return nil, err
	}
	if err := requireByteLen("spender_public_key", payload.GetSpenderPublicKey(), allowancePubKeyLength); err != nil {
		return nil, err
	}
	if err := requireByteLen("token_identifier", payload.GetTokenIdentifier(), tokenIdentifierLength); err != nil {
		return nil, err
	}
	if err := requireByteLen("per_transaction_cap", payload.GetPerTransactionCap(), uint128Length); err != nil {
		return nil, err
	}
	if err := requireByteLen("total_limit", payload.GetTotalLimit(), uint128Length); err != nil {
		return nil, err
	}
	sortedAllowlist, err := sortedAllowlistKeys(payload.GetRecipientAllowlist())
	if err != nil {
		return nil, err
	}

	hasher := hashstructure.NewHasher(createTokenAllowanceHashTag).
		AddUint32(payload.GetVersion()).
		AddString(strings.ToLower(network.String())).
		AddBytes(payload.GetAllowanceId()).
		AddBytes(payload.GetOwnerPublicKey()).
		AddBytes(payload.GetSpenderPublicKey()).
		AddBytes(payload.GetTokenIdentifier()).
		AddBytes(payload.GetPerTransactionCap()).
		AddBytes(payload.GetTotalLimit()).
		AddUint32(uint32(len(sortedAllowlist)))
	for _, key := range sortedAllowlist {
		hasher.AddBytes(key)
	}
	return hasher.
		AddUint64(expiryUnixSeconds(payload)).
		AddUint64(payload.GetOwnerProvidedTimestamp()).
		Hash(), nil
}

// HashRevokeTokenAllowancePayload returns the canonical statement hash the owner signs to revoke
// an allowance. Values are added, in order: version, allowance_id, owner_public_key, and
// owner_provided_timestamp.
//
// The layout is consumed cross-language: the TypeScript SDK MUST produce an identical hash. See
// TestHashRevokeTokenAllowancePayload_KnownVector for the frozen reference vector.
func HashRevokeTokenAllowancePayload(payload *tokenpb.RevokeTokenAllowancePayload) ([]byte, error) {
	if payload == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("revoke token allowance payload cannot be nil"))
	}

	if err := requireByteLen("allowance_id", payload.GetAllowanceId(), allowanceIDLength); err != nil {
		return nil, err
	}
	if err := requireByteLen("owner_public_key", payload.GetOwnerPublicKey(), allowancePubKeyLength); err != nil {
		return nil, err
	}

	return hashstructure.NewHasher(revokeTokenAllowanceHashTag).
		AddUint32(payload.GetVersion()).
		AddBytes(payload.GetAllowanceId()).
		AddBytes(payload.GetOwnerPublicKey()).
		AddUint64(payload.GetOwnerProvidedTimestamp()).
		Hash(), nil
}

// ValidateRevokeTokenAllowancePayload enforces the policy invariants a revocation must satisfy
// before an SO tombstones a grant. Only the version needs checking: every other revoke field is
// bound either by the stored allowance or by the statement hash. It does not verify the owner
// signature; callers do that separately against the hash from HashRevokeTokenAllowancePayload.
func ValidateRevokeTokenAllowancePayload(payload *tokenpb.RevokeTokenAllowancePayload) error {
	if payload == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("revoke token allowance payload cannot be nil"))
	}
	if payload.GetVersion() != tokenAllowancePayloadVersion {
		return sparkerrors.InvalidArgumentInvalidVersion(fmt.Errorf("unsupported token allowance revoke version: %d", payload.GetVersion()))
	}
	return nil
}

// ValidateTokenAllowancePayload enforces the policy invariants an allowance must satisfy before
// an SO installs it. It does not verify the owner signature; callers do that separately against
// the hash from HashCreateTokenAllowancePayload.
func ValidateTokenAllowancePayload(payload *tokenpb.TokenAllowancePayload, supportedNetworks []btcnetwork.Network) error {
	if payload == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("token allowance payload cannot be nil"))
	}
	if payload.GetVersion() != tokenAllowancePayloadVersion {
		return sparkerrors.InvalidArgumentInvalidVersion(fmt.Errorf("unsupported token allowance version: %d", payload.GetVersion()))
	}

	network, err := btcnetwork.FromProtoNetwork(payload.GetNetwork())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to convert network: %w", err))
	}
	if !isNetworkSupported(network, supportedNetworks) {
		return sparkerrors.InvalidArgumentNetworkNotSupported(fmt.Errorf("network %s is not supported", network))
	}

	if err := requireByteLen("allowance_id", payload.GetAllowanceId(), allowanceIDLength); err != nil {
		return err
	}
	if err := requireByteLen("token_identifier", payload.GetTokenIdentifier(), tokenIdentifierLength); err != nil {
		return err
	}
	if err := requireByteLen("per_transaction_cap", payload.GetPerTransactionCap(), uint128Length); err != nil {
		return err
	}
	if err := requireByteLen("total_limit", payload.GetTotalLimit(), uint128Length); err != nil {
		return err
	}

	perTransactionCap := new(big.Int).SetBytes(payload.GetPerTransactionCap())
	totalLimit := new(big.Int).SetBytes(payload.GetTotalLimit())
	if perTransactionCap.Sign() == 0 {
		return sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("per_transaction_cap must be greater than 0"))
	}
	if totalLimit.Sign() == 0 {
		return sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("total_limit must be greater than 0"))
	}
	if perTransactionCap.Cmp(totalLimit) > 0 {
		return sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("per_transaction_cap must not exceed total_limit"))
	}

	expiry := payload.GetExpiryTime()
	if expiry == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("expiry_time is required"))
	}
	// Validate the whole-second value that is actually signed (the statement hash covers Unix
	// seconds) and enforced (the SO truncates the stored expiry to whole seconds).
	if !expiry.AsTime().Truncate(time.Second).After(time.Now()) {
		return sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("expiry_time must be in the future"))
	}

	ownerPublicKey, err := keys.ParsePublicKey(payload.GetOwnerPublicKey())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid owner_public_key: %w", err))
	}
	spenderPublicKey, err := keys.ParsePublicKey(payload.GetSpenderPublicKey())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid spender_public_key: %w", err))
	}
	if ownerPublicKey.Equals(spenderPublicKey) {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("spender_public_key must differ from owner_public_key"))
	}

	if len(payload.GetRecipientAllowlist()) > maxRecipientAllowlistEntries {
		return sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("recipient_allowlist has %d entries, exceeding the maximum of %d", len(payload.GetRecipientAllowlist()), maxRecipientAllowlistEntries))
	}
	for i, keyBytes := range payload.GetRecipientAllowlist() {
		recipient, err := keys.ParsePublicKey(keyBytes)
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid recipient allowlist key at index %d: %w", i, err))
		}
		if recipient.Equals(ownerPublicKey) {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("recipient allowlist must not contain the owner public key"))
		}
	}

	return nil
}

// sortedAllowlistKeys validates each allowlist entry is a 33-byte key and returns a copy sorted
// ascending bytewise so the hash is independent of the caller's ordering.
func sortedAllowlistKeys(allowlist [][]byte) ([][]byte, error) {
	sorted := make([][]byte, len(allowlist))
	for i, key := range allowlist {
		if err := requireByteLen(fmt.Sprintf("recipient_allowlist[%d]", i), key, allowancePubKeyLength); err != nil {
			return nil, err
		}
		sorted[i] = key
	}
	slices.SortFunc(sorted, bytes.Compare)
	return sorted, nil
}

func expiryUnixSeconds(payload *tokenpb.TokenAllowancePayload) uint64 {
	if payload.GetExpiryTime() == nil {
		return 0
	}
	seconds := payload.GetExpiryTime().AsTime().Unix()
	if seconds < 0 {
		return 0
	}
	return uint64(seconds)
}

func requireByteLen(name string, value []byte, requiredLen int) error {
	if len(value) != requiredLen {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("%s must be %d bytes, got %d", name, requiredLen, len(value)))
	}
	return nil
}
