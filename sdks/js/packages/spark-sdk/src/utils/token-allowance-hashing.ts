import { SparkValidationError } from "../errors/types.js";
import { newHasher } from "./hashstructure.js";
import { Network, networkToJSON } from "../proto/spark.js";
import type {
  RevokeTokenAllowancePayload,
  TokenAllowancePayload,
} from "../proto/spark_token.js";

// Hierarchical domain tags mixed into the respective statement hashes so a
// signature over one can never be replayed as the other. Must match the tags
// in spark/so/utils/token_allowance.go. The trailing component versions the
// statement TYPE, not the payload layout: payload.version is itself the first
// value hashed, so two payload versions can never collide.
const CREATE_TOKEN_ALLOWANCE_TAG = [
  "spark",
  "token",
  "create_token_allowance",
  "v1",
];
const REVOKE_TOKEN_ALLOWANCE_TAG = [
  "spark",
  "token",
  "revoke_token_allowance",
  "v1",
];

const ALLOWANCE_ID_LENGTH = 16;
const ALLOWANCE_PUBKEY_LENGTH = 33;
const TOKEN_IDENTIFIER_LENGTH = 32;
const UINT128_LENGTH = 16;

/**
 * Returns the canonical statement hash the owner signs to authorize a token
 * allowance. Byte-for-byte mirror of the Go implementation in
 * spark/so/utils/token_allowance.go (HashCreateTokenAllowancePayload): a
 * tagged hash (hashstructure) over, in order, version, the
 * lowercase network name, allowance_id, owner/spender public keys,
 * token_identifier, per_transaction_cap, the per_transaction_unlimited flag,
 * total_limit, the total_unlimited flag, the allowlist entry count followed
 * by each 33-byte key sorted ascending bytewise, the expiry as Unix seconds
 * (0 when unset), and owner_provided_timestamp in milliseconds.
 *
 * The frozen cross-language vector lives in
 * TestHashCreateTokenAllowancePayload_KnownVector (Go) and
 * token-allowance-hashing.test.ts (TS); do not change this layout without a
 * deliberate, version-bumped format change on both sides.
 */
export function hashCreateTokenAllowancePayload(
  payload: TokenAllowancePayload,
): Uint8Array {
  if (!payload) {
    throw new SparkValidationError("token allowance payload cannot be nil", {
      field: "payload",
    });
  }

  const networkName = lowercaseNetworkName(payload.network);

  requireByteLength("allowanceId", payload.allowanceId, ALLOWANCE_ID_LENGTH);
  requireByteLength(
    "ownerPublicKey",
    payload.ownerPublicKey,
    ALLOWANCE_PUBKEY_LENGTH,
  );
  requireByteLength(
    "spenderPublicKey",
    payload.spenderPublicKey,
    ALLOWANCE_PUBKEY_LENGTH,
  );
  requireByteLength(
    "tokenIdentifier",
    payload.tokenIdentifier,
    TOKEN_IDENTIFIER_LENGTH,
  );
  requireByteLength(
    "perTransactionCap",
    payload.perTransactionCap,
    UINT128_LENGTH,
  );
  requireByteLength("totalLimit", payload.totalLimit, UINT128_LENGTH);

  const sortedAllowlist = sortedAllowlistKeys(payload.recipientAllowlist);

  const hasher = newHasher(CREATE_TOKEN_ALLOWANCE_TAG)
    .addUint32(payload.version)
    .addString(networkName)
    .addBytes(payload.allowanceId)
    .addBytes(payload.ownerPublicKey)
    .addBytes(payload.spenderPublicKey)
    .addBytes(payload.tokenIdentifier)
    .addBytes(payload.perTransactionCap)
    .addUint8(payload.perTransactionUnlimited ? 1 : 0)
    .addBytes(payload.totalLimit)
    .addUint8(payload.totalUnlimited ? 1 : 0)
    .addUint32(sortedAllowlist.length);
  for (const key of sortedAllowlist) {
    hasher.addBytes(key);
  }
  return hasher
    .addUint64(expiryUnixSeconds(payload.expiryTime))
    .addUint64(BigInt(payload.ownerProvidedTimestamp))
    .hash();
}

/**
 * Returns the canonical statement hash the owner signs to revoke an
 * allowance. Mirrors Go HashRevokeTokenAllowancePayload: a tagged hash
 * (hashstructure) over version, allowance_id, owner_public_key, and
 * owner_provided_timestamp in milliseconds.
 */
export function hashRevokeTokenAllowancePayload(
  payload: RevokeTokenAllowancePayload,
): Uint8Array {
  if (!payload) {
    throw new SparkValidationError(
      "revoke token allowance payload cannot be nil",
      {
        field: "payload",
      },
    );
  }

  requireByteLength("allowanceId", payload.allowanceId, ALLOWANCE_ID_LENGTH);
  requireByteLength(
    "ownerPublicKey",
    payload.ownerPublicKey,
    ALLOWANCE_PUBKEY_LENGTH,
  );

  return newHasher(REVOKE_TOKEN_ALLOWANCE_TAG)
    .addUint32(payload.version)
    .addBytes(payload.allowanceId)
    .addBytes(payload.ownerPublicKey)
    .addUint64(BigInt(payload.ownerProvidedTimestamp))
    .hash();
}

/**
 * Maps a proto Network to the lowercase name hashed into the create
 * statement. Mirrors Go strings.ToLower(btcnetwork.Network.String()) for the
 * networks accepted by btcnetwork.FromProtoNetwork; anything else is
 * rejected, matching the Go error path.
 */
function lowercaseNetworkName(network: Network): string {
  // No operator hashes these names, and this feeds the owner-signed statement.
  if (
    network === Network.UNSPECIFIED ||
    network === Network.UNRECOGNIZED ||
    !(network in Network)
  ) {
    throw new SparkValidationError("failed to convert network", {
      field: "network",
      value: network,
    });
  }
  return networkToJSON(network).toLowerCase();
}

/**
 * Validates each allowlist entry is a 33-byte key and returns a copy sorted
 * ascending bytewise so the hash is independent of the caller's ordering.
 */
function sortedAllowlistKeys(allowlist: Uint8Array[]): Uint8Array[] {
  const sorted = allowlist.map((key, i) => {
    requireByteLength(`recipientAllowlist[${i}]`, key, ALLOWANCE_PUBKEY_LENGTH);
    return key;
  });
  sorted.sort(compareBytes);
  return sorted;
}

function compareBytes(a: Uint8Array, b: Uint8Array): number {
  const minLength = Math.min(a.length, b.length);
  for (let i = 0; i < minLength; i++) {
    if (a[i] !== b[i]) {
      return a[i]! - b[i]!;
    }
  }
  return a.length - b.length;
}

function expiryUnixSeconds(expiryTime: Date | undefined): bigint {
  if (!expiryTime) {
    return 0n;
  }
  const seconds = Math.floor(expiryTime.getTime() / 1000);
  if (seconds < 0) {
    return 0n;
  }
  return BigInt(seconds);
}

function requireByteLength(
  name: string,
  value: Uint8Array | undefined,
  expected: number,
): asserts value is Uint8Array {
  if (!value || value.length !== expected) {
    throw new SparkValidationError(
      `${name} must be ${expected} bytes, got ${value ? value.length : 0}`,
      {
        field: name,
        expectedLength: expected,
        actualLength: value ? value.length : 0,
      },
    );
  }
}
