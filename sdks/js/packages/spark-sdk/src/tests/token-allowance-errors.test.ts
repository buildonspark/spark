import { SparkError } from "../errors/base.js";
import {
  type TokenAllowanceFailure,
  toAllowanceRequestError,
  tokenAllowanceError,
  tokenAllowanceFailureOf,
} from "../errors/token-allowances.js";
import { SparkRequestError } from "../errors/types.js";

const SO_ALLOWANCE_ERRORS: Array<[string, TokenAllowanceFailure]> = [
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
  [
    "an active allowance already exists for this owner, spender, and token",
    "ALREADY_ACTIVE",
  ],
  ["the per-owner cap is", "QUOTA_EXCEEDED"],
  ["was revoked and cannot be recreated", "REVOKED_CANNOT_RECREATE"],
  ["already exists with a different statement hash", "TERMS_MISMATCH"],
  ["token allowance is not in a spendable state", "NOT_SPENDABLE"],
];

describe("allowance error normalization", () => {
  it.each(SO_ALLOWANCE_ERRORS)(
    "maps %s to its reason",
    (soErrorText, reason) => {
      const mapped = toAllowanceRequestError(
        new Error(`rpc error: prepare failed: ${soErrorText} (allowance abc)`),
        "broadcast_transaction",
      );

      expect(mapped).toBeInstanceOf(SparkRequestError);
      expect(tokenAllowanceFailureOf(mapped)).toBe(reason);
    },
  );

  it("is catchable as a SparkError", () => {
    const mapped = toAllowanceRequestError(
      new Error("token allowance has expired"),
      "broadcast_transaction",
    );

    expect(mapped).toBeInstanceOf(SparkError);
  });

  it("preserves the original error on an unrecognized failure", () => {
    const unknownError = new Error("deadline exceeded");

    expect(
      toAllowanceRequestError(unknownError, "query_token_allowances")
        .originalError,
    ).toBe(unknownError);
  });

  it("passes an already-normalized error through untouched", () => {
    const mapped = tokenAllowanceError("EXPIRED", "already mapped");

    expect(toAllowanceRequestError(mapped, "broadcast_transaction")).toBe(
      mapped,
    );
  });

  it("reports no failure reason for an unrelated SparkRequestError", () => {
    expect(tokenAllowanceFailureOf(new SparkRequestError("timeout"))).toBe(
      undefined,
    );
  });
  it("normalizes an unmatched transport failure into a SparkRequestError", () => {
    const raw = new Error("deadline exceeded");

    const mapped = toAllowanceRequestError(raw, "create_token_allowance");

    expect(mapped).toBeInstanceOf(SparkRequestError);
    expect(tokenAllowanceFailureOf(mapped)).toBe(undefined);
    expect(mapped.getContext().operation).toBe("create_token_allowance");
    expect(mapped.originalError).toBe(raw);
  });

  it("keeps the failure reason on a recognized refusal", () => {
    const mapped = toAllowanceRequestError(
      new Error("rpc error: token allowance has expired"),
      "broadcast_transaction",
    );

    expect(tokenAllowanceFailureOf(mapped)).toBe("EXPIRED");
  });

  it("ignores a context value that is not a known failure", () => {
    const bogus = new SparkRequestError("x", { allowanceFailure: "NOPE" });

    expect(tokenAllowanceFailureOf(bogus)).toBe(undefined);
  });
});
