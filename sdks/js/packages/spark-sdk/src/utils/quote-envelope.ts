import { getSparkTokenPrimitives } from "../token-primitives-bindings/token-primitives-bindings.js";

/** Which flow a quote is issued for. Matches the shared core's QuoteReason. */
export enum QuoteReason {
  RECEIVE = 1,
  SEND = 2,
  COOP_EXIT = 3,
  STATIC_DEPOSIT = 4,
}

/**
 * Which party a quote signature comes from. Both roles sign the same envelope
 * over the same manifest, so the role is what keeps an issuer signature from
 * being replayable as an attestor one.
 */
export enum QuoteRole {
  ISSUER = 1,
  ATTESTOR = 2,
}

const PAYMENT_HASH_LENGTH = 32;
const MANIFEST_HASH_LENGTH = 32;

/**
 * Computes the digest a quote signature is taken over:
 *
 *   tagged_hash(["spark", "quote", "v1"], network, manifest_hash, reason, role, target)
 *
 * The construction lives in the shared Rust core (spark-token-primitives), the
 * same implementation the SSP binds and the operators' Go port is pinned
 * against, so the signed rules cannot drift between consumers. Parity is
 * pinned by the cross-language golden vectors in
 * spark/testdata/quote_envelope_cases.json.
 *
 * `network` is the proto Network enum value, not the SDK's Network.
 */
export async function quoteEnvelopeDigest({
  network,
  manifestHash,
  reason,
  role,
  target,
}: {
  network: number;
  manifestHash: Uint8Array;
  reason: QuoteReason;
  role: QuoteRole;
  target: Uint8Array;
}): Promise<Uint8Array> {
  // wasm coerces a JS number to u32 at the boundary, so a fractional or
  // out-of-range discriminant would silently become a different valid one
  // rather than being rejected by the core.
  validateU32(network, "network");
  validateU32(reason, "reason");
  validateU32(role, "role");
  validateHashLength(manifestHash, MANIFEST_HASH_LENGTH, "manifest_hash");

  return normalizeWasmError(() =>
    getSparkTokenPrimitives().quoteEnvelopeDigest(
      network,
      manifestHash,
      reason,
      role,
      target,
    ),
  );
}

/**
 * Binds a receive attestation to the invoice it covers. Without this the same
 * attestation would cover any invoice of the same gross from the same attestor,
 * letting a manifest be paired with the wrong payment hash.
 */
export async function receiveAttestorTarget(
  paymentHash: Uint8Array,
): Promise<Uint8Array> {
  validateHashLength(paymentHash, PAYMENT_HASH_LENGTH, "payment_hash");

  return normalizeWasmError(() =>
    getSparkTokenPrimitives().receiveAttestorTarget(paymentHash),
  );
}

async function normalizeWasmError(
  call: () => Promise<Uint8Array>,
): Promise<Uint8Array> {
  try {
    return await call();
  } catch (err) {
    // The wasm boundary rejects with a plain string; normalize to Error so the
    // Rust message stays catchable.
    throw err instanceof Error ? err : new Error(String(err));
  }
}

function validateU32(value: number, field: string) {
  if (!Number.isInteger(value)) {
    throw new Error(`quote envelope not hashable: ${field} must be an integer`);
  }
  if (value < 0 || value > 0xffffffff) {
    throw new Error(
      `quote envelope not hashable: ${field} is outside uint32 range`,
    );
  }
}

function validateHashLength(value: Uint8Array, length: number, field: string) {
  if (value.length !== length) {
    throw new Error(
      `quote envelope not hashable: ${field} must be ${length} bytes, got ${value.length}`,
    );
  }
}
