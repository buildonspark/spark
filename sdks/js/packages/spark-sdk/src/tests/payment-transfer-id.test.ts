import { describe, expect, it } from "@jest/globals";
import { uuidv7obj } from "uuidv7";
import type {
  PayLightningInvoiceParams,
  TransferWithInvoiceParams,
} from "../spark-wallet/types.js";
import { type IdempotencyOptions } from "../utils/idempotency.js";
import { generateTransferId, UUID } from "../utils/transfer-id.js";

describe("Lightning payment transfer IDs", () => {
  it("generates a UUIDv7", () => {
    const key = generateTransferId();

    expect(key).toBeInstanceOf(UUID);
    expect(key.getVersion()).toBe(7);
  });

  it("uses UUID for Lightning payment de-duplication", () => {
    const transferId = uuidv7obj();
    const params: PayLightningInvoiceParams = {
      invoice: "invoice",
      maxFeeSats: 1,
      transferId,
    };

    expect(params.transferId).toBe(transferId);
  });

  it("leaves generic idempotency options string-based", () => {
    const options: IdempotencyOptions = { idempotencyKey: "request-key" };

    expect(options.idempotencyKey).toBe("request-key");
  });

  it("does not expose transfer IDs through wallet transfer parameters", () => {
    type HasTransferId = "transferId" extends keyof TransferWithInvoiceParams
      ? true
      : false;
    const hasTransferId: HasTransferId = false;

    expect(hasTransferId).toBe(false);
  });
});
