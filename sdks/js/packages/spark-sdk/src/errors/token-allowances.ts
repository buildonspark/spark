import type { SparkTokenServiceDefinition } from "../proto/spark_token.js";
import { SparkRequestError } from "./types.js";

export type TokenAllowanceFailure =
  | "NOT_FOUND"
  | "REVOKED"
  | "EXPIRED"
  | "PER_TRANSACTION_CAP_EXCEEDED"
  | "BUDGET_EXHAUSTED"
  | "RECIPIENT_NOT_ALLOWED"
  | "MIXED_AUTHORIZATION"
  | "NOT_SPENDABLE"
  | "ALREADY_ACTIVE"
  | "QUOTA_EXCEEDED"
  | "REVOKED_CANNOT_RECREATE"
  | "TERMS_MISMATCH";

// Exact error strings emitted by the SO (spark/so/tokens/errors.go). These are
// matched as substrings of gRPC error messages because the SO wraps them with
// transaction context.
const SO_ALLOWANCE_ERROR_MATCHERS: [string, TokenAllowanceFailure][] = [
  ["token allowance not found", "NOT_FOUND"],
  ["token allowance has been revoked", "REVOKED"],
  ["token allowance has expired", "EXPIRED"],
  [
    "metered amount exceeds the allowance per-transaction cap",
    "PER_TRANSACTION_CAP_EXCEEDED",
  ],
  ["metered amount exceeds the allowance remaining budget", "BUDGET_EXHAUSTED"],
  [
    "output recipient is not in the allowance recipient allowlist",
    "RECIPIENT_NOT_ALLOWED",
  ],
  [
    "transaction mixes allowance-authorized and owner-signed inputs",
    "MIXED_AUTHORIZATION",
  ],
  ["token allowance is not in a spendable state", "NOT_SPENDABLE"],
  [
    "an active allowance already exists for this owner, spender, and token",
    "ALREADY_ACTIVE",
  ],
  ["the per-owner cap is", "QUOTA_EXCEEDED"],
  ["was revoked and cannot be recreated", "REVOKED_CANNOT_RECREATE"],
  ["already exists with a different statement hash", "TERMS_MISMATCH"],
];

export function tokenAllowanceError(
  allowanceFailure: TokenAllowanceFailure,
  message: string,
  context: Record<string, unknown> = {},
): SparkRequestError {
  return new SparkRequestError(message, { ...context, allowanceFailure });
}

export function tokenAllowanceFailureOf(
  error: unknown,
): TokenAllowanceFailure | undefined {
  if (!(error instanceof SparkRequestError)) {
    return undefined;
  }
  const failure = error.getContext().allowanceFailure;
  return isTokenAllowanceFailure(failure) ? failure : undefined;
}

function isTokenAllowanceFailure(
  value: unknown,
): value is TokenAllowanceFailure {
  return (
    typeof value === "string" &&
    SO_ALLOWANCE_ERROR_MATCHERS.some(([, failure]) => failure === value)
  );
}

/**
 * Normalizes any failure from an allowance RPC into a SparkRequestError, so
 * allowance calls surface the same error type as every other token API rather
 * than leaking raw transport errors. Known refusals also carry
 * allowanceFailure.
 */
export function toAllowanceRequestError(
  error: unknown,
  operation: keyof SparkTokenServiceDefinition["methods"],
): SparkRequestError {
  if (error instanceof SparkRequestError) {
    return error;
  }
  const message = extractErrorMessage(error);
  for (const [soErrorText, allowanceFailure] of SO_ALLOWANCE_ERROR_MATCHERS) {
    if (message.includes(soErrorText)) {
      return new SparkRequestError(soErrorText, {
        operation,
        error,
        allowanceFailure,
      });
    }
  }
  return new SparkRequestError(`Failed to ${operation}`, { operation, error });
}

function extractErrorMessage(error: unknown): string {
  if (error instanceof Error) {
    // nice-grpc ClientError puts the server message in .details; fall back to
    // .message which includes it for plain errors.
    const details = (error as { details?: unknown }).details;
    if (typeof details === "string" && details.length > 0) {
      return `${error.message} ${details}`;
    }
    return error.message;
  }
  return String(error);
}
