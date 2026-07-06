import { TransferManifest, type ManifestAmount } from "../proto/spark.js";
import { getSparkTokenPrimitives } from "../token-primitives-bindings/token-primitives-bindings.js";

/**
 * Computes the signed digest for a TransferManifest:
 *
 *   tagged_hash(["spark", "transfer", "manifest"], protohash(canonicalize(manifest)))
 *
 * The construction — fail-closed validation, canonicalization (edge/fee
 * ordering, millisecond-floored timestamps), protohash, and the tagged-hash
 * wrapper — lives in the shared Rust core (spark-token-primitives), the same
 * implementation the SSP binds, so the signed rules cannot drift between
 * consumers. Parity with Go's common.HashTransferManifest is pinned by the
 * cross-language golden vectors in
 * spark/testdata/transfer_manifest_hash_cases.json.
 *
 * The validation kept on this side covers only JS-representability hazards:
 * a value the proto encoder would silently coerce (unsafe-integer or negative
 * amounts, bps beyond uint32, NaN timestamps) reaches the wire as a DIFFERENT
 * valid-looking value, which the Rust core cannot detect after the fact.
 */
export async function hashTransferManifest(
  manifest: TransferManifest,
): Promise<Uint8Array> {
  if (!manifest) {
    throw new Error("transfer manifest not hashable: manifest is missing");
  }
  validateEncodable(manifest);

  const manifestBytes = TransferManifest.encode(manifest).finish();
  try {
    return await getSparkTokenPrimitives().hashTransferManifest(manifestBytes);
  } catch (err) {
    // The wasm boundary rejects with a plain string; normalize to Error so
    // the Rust message ("transfer manifest not hashable: ...") stays catchable.
    throw err instanceof Error ? err : new Error(String(err));
  }
}

function validateEncodable(manifest: TransferManifest) {
  validateEncodableTimestamp(
    manifest.transferExpiryTime,
    "transfer_expiry_time",
  );
  validateEncodableTimestamp(manifest.quoteExpiryTime, "quote_expiry_time");
  manifest.edges.forEach((edge, i) => {
    validateEncodableAmount(edge.amount, `edges[${i}]`);
  });
  manifest.fees.forEach((fee, i) => {
    validateEncodableAmount(fee.amount, `fees[${i}]`);
  });
}

// An invalid Date's NaN epoch would encode as a zeroed timestamp instead of
// failing — reject it before it can silently become 1970-01-01.
function validateEncodableTimestamp(ts: Date | undefined, field: string) {
  if (ts !== undefined && Number.isNaN(ts.getTime())) {
    throw new Error(
      `transfer manifest not hashable: ${field}: timestamp is an invalid date`,
    );
  }
}

function validateEncodableAmount(
  amount: ManifestAmount | undefined,
  context: string,
) {
  switch (amount?.amount?.$case) {
    case "sats":
      validateEncodableAmountValue(amount.amount.sats, context);
      break;
    case "bps":
      validateEncodableAmountValue(amount.amount.bps, context);
      if (amount.amount.bps > 0xffffffff) {
        throw new Error(
          `transfer manifest not hashable: ${context}: bps exceeds uint32 range`,
        );
      }
      break;
    default:
      // Unset amounts encode faithfully; the Rust core rejects them.
      break;
  }
}

function validateEncodableAmountValue(value: number, context: string) {
  if (!Number.isSafeInteger(value)) {
    throw new Error(
      `transfer manifest not hashable: ${context}: amount must be a safe integer`,
    );
  }
  if (value < 0) {
    throw new Error(
      `transfer manifest not hashable: ${context}: amount must be non-negative`,
    );
  }
}
