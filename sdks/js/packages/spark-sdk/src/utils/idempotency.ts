import { Metadata } from "nice-grpc-common";
import { SparkCallOptions } from "../types/grpc.js";

const IDEMPOTENCY_KEY_HEADER = "x-idempotency-key";

export type IdempotencyOptions = {
  /**
   * Optional client-provided idempotency key for deduplication.
   * If not provided, no idempotency key will be sent (null/undefined).
   *
   * Multiple requests with the same key will return the same result
   * instead of creating duplicates or returning errors.
   */
  idempotencyKey?: string;
};

/**
 * Add idempotency key to call options metadata.
 *
 * @param idempotencyKey - Idempotency key
 * @param options - Existing call options (optional)
 * @returns Call options with idempotency key in metadata
 */
export function optionsWithIdempotencyKey(
  idempotencyKey: string,
  options?: SparkCallOptions,
): SparkCallOptions {
  const metadata = new Metadata(options?.metadata);
  metadata.set(IDEMPOTENCY_KEY_HEADER, idempotencyKey);

  return { ...options, metadata };
}
