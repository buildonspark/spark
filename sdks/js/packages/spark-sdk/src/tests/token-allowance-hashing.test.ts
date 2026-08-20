import { bytesToHex, hexToBytes } from "@noble/hashes/utils";
import { Network } from "../proto/spark.js";
import type {
  RevokeTokenAllowancePayload,
  TokenAllowancePayload,
} from "../proto/spark_token.js";
import {
  hashCreateTokenAllowancePayload,
  hashRevokeTokenAllowancePayload,
} from "../utils/token-allowance-hashing.js";

// Constants mirroring spark/so/utils/token_allowance_test.go so the frozen
// cross-language vector matches exactly.
const ALLOWANCE_OWNER_KEY_HEX =
  "02ca75659458529755b77663f18282f4aa130313e098fac40deffb1208207a2ffe";
const ALLOWANCE_SPENDER_KEY_HEX =
  "033e40d72117ee89f7bda15d2b3d779843e6721e8e4c5078c192b50fb3782de2f5";
const ALLOWANCE_FEE_KEY_HEX =
  "0264a6f0a4f02477123875eceb43592369848081d329f3db0eba7445a4abed23b8";
const ALLOWANCE_RECIPIENT_1_HEX =
  "0375a9121cd7c3684ca1941978cc0dc42ce316fddf70261643f17ba3eeca6d10f2";
const ALLOWANCE_RECIPIENT_2_HEX =
  "028c094a432d46a0ac95349d792c2e3730bd60c29188db716f56a99e39b95338b4";
const ALLOWANCE_ID_HEX = "0123456789abcdef0123456789abcdef";
const ALLOWANCE_TOKEN_ID_HEX =
  "3e534a8d9798fe5e20516f9b1aa05f5d78d718ece893e8af89d678c3d88f2451";
const ALLOWANCE_PER_TX_CAP_HEX = "00000000000000000000000000002710"; // 10000
const ALLOWANCE_TOTAL_LIMIT_HEX = "000000000000000000000000000186a0"; // 100000
const ALLOWANCE_PROVIDED_TS_MILLIS = 1747337980820;
const ALLOWANCE_EXPIRY_UNIX_SECONDS = 2000000000;

// Frozen cross-language vectors from Go: KNOWN_CREATE_VECTOR_HEX pins the bounded
// layout (TestHashCreateTokenAllowancePayload_KnownVector) and
// KNOWN_CREATE_UNLIMITED_VECTOR_HEX pins the canonical unlimited encoding
// (TestHashCreateTokenAllowancePayload_KnownVectorUnlimited). If these change, the
// wire format changed and every client that signs allowances must move in
// lockstep; do not edit without a deliberate format change on both sides.
const KNOWN_CREATE_VECTOR_HEX =
  "df52577d7fd9feda71cdd93ba54f96f19cb4bc009ec56148e95de083f9381f58";
const KNOWN_CREATE_UNLIMITED_VECTOR_HEX =
  "373edc3c0929cf645992a07994b3cbafa6e8b5e97f847d1eca1a2491b13eec9a";
// Pins the revoke layout against Go TestHashRevokeTokenAllowancePayload_KnownVector.
const KNOWN_REVOKE_VECTOR_HEX =
  "e35cf0188bae34706871d27f8c87797aa00df737d63ff65191ef0e62d2afc256";

function deterministicCreatePayload(): TokenAllowancePayload {
  return {
    version: 1,
    allowanceId: hexToBytes(ALLOWANCE_ID_HEX),
    ownerPublicKey: hexToBytes(ALLOWANCE_OWNER_KEY_HEX),
    spenderPublicKey: hexToBytes(ALLOWANCE_SPENDER_KEY_HEX),
    tokenIdentifier: hexToBytes(ALLOWANCE_TOKEN_ID_HEX),
    perTransactionCap: hexToBytes(ALLOWANCE_PER_TX_CAP_HEX),
    totalLimit: hexToBytes(ALLOWANCE_TOTAL_LIMIT_HEX),
    perTransactionUnlimited: false,
    totalUnlimited: false,
    recipientAllowlist: [
      hexToBytes(ALLOWANCE_RECIPIENT_1_HEX),
      hexToBytes(ALLOWANCE_RECIPIENT_2_HEX),
    ],
    expiryTime: new Date(ALLOWANCE_EXPIRY_UNIX_SECONDS * 1000),
    network: Network.REGTEST,
    ownerProvidedTimestamp: ALLOWANCE_PROVIDED_TS_MILLIS,
  };
}

/** Both ceilings waived: flags true, caps all-zero (the canonical encoding). */
function deterministicCreatePayloadUnlimited(): TokenAllowancePayload {
  return {
    ...deterministicCreatePayload(),
    perTransactionUnlimited: true,
    totalUnlimited: true,
    perTransactionCap: new Uint8Array(16),
    totalLimit: new Uint8Array(16),
  };
}

function deterministicRevokePayload(): RevokeTokenAllowancePayload {
  return {
    version: 1,
    allowanceId: hexToBytes(ALLOWANCE_ID_HEX),
    ownerPublicKey: hexToBytes(ALLOWANCE_OWNER_KEY_HEX),
    ownerProvidedTimestamp: ALLOWANCE_PROVIDED_TS_MILLIS,
  };
}

describe("hashCreateTokenAllowancePayload", () => {
  it("matches the frozen cross-language vector", () => {
    const hash = hashCreateTokenAllowancePayload(deterministicCreatePayload());
    expect(bytesToHex(hash)).toBe(KNOWN_CREATE_VECTOR_HEX);
  });

  it("matches the frozen unlimited vector", () => {
    const hash = hashCreateTokenAllowancePayload(
      deterministicCreatePayloadUnlimited(),
    );
    expect(bytesToHex(hash)).toBe(KNOWN_CREATE_UNLIMITED_VECTOR_HEX);
  });

  it("binds each unlimited flag in the hash", () => {
    const perTx = deterministicCreatePayload();
    perTx.perTransactionUnlimited = true;
    const total = deterministicCreatePayload();
    total.totalUnlimited = true;

    const base = bytesToHex(
      hashCreateTokenAllowancePayload(deterministicCreatePayload()),
    );
    const perTxHash = bytesToHex(hashCreateTokenAllowancePayload(perTx));
    const totalHash = bytesToHex(hashCreateTokenAllowancePayload(total));
    expect(perTxHash).not.toBe(base);
    expect(totalHash).not.toBe(base);
    expect(perTxHash).not.toBe(totalHash);
  });

  it("is deterministic", () => {
    const first = hashCreateTokenAllowancePayload(deterministicCreatePayload());
    const second = hashCreateTokenAllowancePayload(
      deterministicCreatePayload(),
    );
    expect(bytesToHex(first)).toBe(bytesToHex(second));
    expect(first).toHaveLength(32);
  });

  it("canonicalizes allowlist ordering", () => {
    const descending = deterministicCreatePayload();
    descending.recipientAllowlist = [
      hexToBytes(ALLOWANCE_RECIPIENT_2_HEX),
      hexToBytes(ALLOWANCE_RECIPIENT_1_HEX),
    ];
    expect(bytesToHex(hashCreateTokenAllowancePayload(descending))).toBe(
      KNOWN_CREATE_VECTOR_HEX,
    );
  });

  const mutations: [string, (p: TokenAllowancePayload) => void][] = [
    ["version", (p) => (p.version = 2)],
    ["network", (p) => (p.network = Network.MAINNET)],
    [
      "allowanceId",
      (p) => (p.allowanceId = hexToBytes("ffffffffffffffffffffffffffffffff")),
    ],
    [
      "ownerPublicKey",
      (p) => (p.ownerPublicKey = hexToBytes(ALLOWANCE_FEE_KEY_HEX)),
    ],
    [
      "spenderPublicKey",
      (p) => (p.spenderPublicKey = hexToBytes(ALLOWANCE_FEE_KEY_HEX)),
    ],
    [
      "tokenIdentifier",
      (p) =>
        (p.tokenIdentifier = hexToBytes(
          "00534a8d9798fe5e20516f9b1aa05f5d78d718ece893e8af89d678c3d88f2451",
        )),
    ],
    [
      "perTransactionCap",
      (p) =>
        (p.perTransactionCap = hexToBytes("00000000000000000000000000002711")),
    ],
    [
      "totalLimit",
      (p) => (p.totalLimit = hexToBytes("000000000000000000000000000186a1")),
    ],
    [
      "recipientAllowlist",
      (p) => (p.recipientAllowlist = [hexToBytes(ALLOWANCE_RECIPIENT_1_HEX)]),
    ],
    [
      "expiryTime",
      (p) =>
        (p.expiryTime = new Date((ALLOWANCE_EXPIRY_UNIX_SECONDS + 1) * 1000)),
    ],
    [
      "ownerProvidedTimestamp",
      (p) => (p.ownerProvidedTimestamp = ALLOWANCE_PROVIDED_TS_MILLIS + 1),
    ],
  ];

  it.each(mutations)("mutating %s changes the hash", (_name, mutate) => {
    const mutated = deterministicCreatePayload();
    mutate(mutated);
    expect(bytesToHex(hashCreateTokenAllowancePayload(mutated))).not.toBe(
      KNOWN_CREATE_VECTOR_HEX,
    );
  });

  it("rejects malformed field lengths", () => {
    const shortAllowanceId = deterministicCreatePayload();
    shortAllowanceId.allowanceId = new Uint8Array(15);
    expect(() => hashCreateTokenAllowancePayload(shortAllowanceId)).toThrow(
      "allowanceId must be 16 bytes",
    );

    const shortOwner = deterministicCreatePayload();
    shortOwner.ownerPublicKey = new Uint8Array(32);
    expect(() => hashCreateTokenAllowancePayload(shortOwner)).toThrow(
      "ownerPublicKey must be 33 bytes",
    );

    const badAllowlistEntry = deterministicCreatePayload();
    badAllowlistEntry.recipientAllowlist = [new Uint8Array(32)];
    expect(() => hashCreateTokenAllowancePayload(badAllowlistEntry)).toThrow(
      "recipientAllowlist[0] must be 33 bytes",
    );
  });

  it("rejects an unspecified network", () => {
    const payload = deterministicCreatePayload();
    payload.network = Network.UNSPECIFIED;
    expect(() => hashCreateTokenAllowancePayload(payload)).toThrow(
      "failed to convert network",
    );
  });
});

describe("hashRevokeTokenAllowancePayload", () => {
  it("is deterministic and does not collide with the create hash", () => {
    const first = hashRevokeTokenAllowancePayload(deterministicRevokePayload());
    const second = hashRevokeTokenAllowancePayload(
      deterministicRevokePayload(),
    );
    expect(bytesToHex(first)).toBe(bytesToHex(second));
    expect(first).toHaveLength(32);
    expect(bytesToHex(first)).not.toBe(KNOWN_CREATE_VECTOR_HEX);
  });

  it("matches the frozen Go revoke vector", () => {
    const hash = hashRevokeTokenAllowancePayload(deterministicRevokePayload());
    expect(bytesToHex(hash)).toBe(KNOWN_REVOKE_VECTOR_HEX);
  });

  it("rejects malformed field lengths", () => {
    const badId = deterministicRevokePayload();
    badId.allowanceId = new Uint8Array(17);
    expect(() => hashRevokeTokenAllowancePayload(badId)).toThrow(
      "allowanceId must be 16 bytes",
    );
  });
});
