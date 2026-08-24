import { beforeEach, describe, expect, it, jest } from "@jest/globals";
import type { SparkWallet } from "@buildonspark/spark-sdk";
import {
  handleCreateInvoiceFromQuote,
  handleCreateQuotedInvoice,
  handleLightningReceiveQuote,
} from "../../tools/receive-quote.js";

type QuoteParams = {
  amountSats: number;
  amountBasis?: string;
  partnerJwt?: string;
};
type InvoiceParams = { amountSats: number; memo?: string; quote?: unknown };

const bodyOf = (result: { content: Array<{ text: string }> }) =>
  JSON.parse(result.content[0]?.text ?? "{}") as Record<string, unknown>;

// A feeless manifest: one edge paying the receiver 1000 sats. Encoded by hand
// so the tool's decode of the echoed bytes is exercised for real.
const RECEIVER = "02".padEnd(66, "a");
const SERIALIZED_MANIFEST = buildManifestHex(1000);

const SSP_KEY = "03".padEnd(66, "b");

function buildFeeBearingHex(): string {
  const { TransferManifest } = jest.requireActual<
    typeof import("@buildonspark/spark-sdk/proto/spark")
  >("@buildonspark/spark-sdk/proto/spark");
  const receiver = Uint8Array.from(Buffer.from(RECEIVER, "hex"));
  const ssp = Uint8Array.from(Buffer.from(SSP_KEY, "hex"));
  const bytes = TransferManifest.encode({
    version: 1,
    transferId: "0197f9a0-0000-7000-8000-000000000002",
    network: 2,
    transferExpiryTime: undefined,
    edges: [
      {
        senderIdentityPublicKey: ssp,
        receiverIdentityPublicKey: receiver,
        amount: { amount: { $case: "sats", sats: 100_000 } },
      },
      {
        senderIdentityPublicKey: ssp,
        receiverIdentityPublicKey: ssp,
        amount: { amount: { $case: "sats", sats: 2_000 } },
      },
    ],
    fees: [
      {
        source: 1,
        role: 3,
        amount: { amount: { $case: "sats", sats: 2_000 } },
        receiverIdentityPublicKey: ssp,
      },
    ],
    quoteExpiryTime: new Date("2026-08-11T00:05:00Z"),
  }).finish();
  return Buffer.from(bytes).toString("hex");
}

function buildManifestHex(sats: number): string {
  // version=1, transfer_id, network=REGTEST, one edge, quote_expiry_time.
  const { TransferManifest } = jest.requireActual<
    typeof import("@buildonspark/spark-sdk/proto/spark")
  >("@buildonspark/spark-sdk/proto/spark");
  const key = Uint8Array.from(Buffer.from(RECEIVER, "hex"));
  const bytes = TransferManifest.encode({
    version: 1,
    transferId: "0197f9a0-0000-7000-8000-000000000001",
    network: 2,
    transferExpiryTime: undefined,
    edges: [
      {
        senderIdentityPublicKey: key,
        receiverIdentityPublicKey: key,
        amount: { amount: { $case: "sats", sats } },
      },
    ],
    fees: [],
    quoteExpiryTime: new Date("2026-08-11T00:05:00Z"),
  }).finish();
  return Buffer.from(bytes).toString("hex");
}

const feeBearingQuote = () => {
  const { TransferManifest } = jest.requireActual<
    typeof import("@buildonspark/spark-sdk/proto/spark")
  >("@buildonspark/spark-sdk/proto/spark");
  const hex = buildFeeBearingHex();
  return {
    serializedManifest: hex,
    issuerSignature: "aabb",
    attributionStatus: "ATTRIBUTED",
    manifest: TransferManifest.decode(Uint8Array.from(Buffer.from(hex, "hex"))),
    amountSats: 100_000,
    amountBasis: "NET",
  };
};

const getLightningReceiveQuoteMock =
  jest.fn<(params: QuoteParams) => Promise<Record<string, unknown>>>();
const createLightningInvoiceMock =
  jest.fn<(params: InvoiceParams) => Promise<Record<string, unknown>>>();

const mockWallet = {
  getLightningReceiveQuote: getLightningReceiveQuoteMock,
  createLightningInvoice: createLightningInvoiceMock,
};

const mockResolve = jest
  .fn<(mnemonic?: string) => Promise<SparkWallet>>()
  .mockResolvedValue(mockWallet as unknown as SparkWallet);

const quoteResult = (sats: number) => {
  const { TransferManifest } = jest.requireActual<
    typeof import("@buildonspark/spark-sdk/proto/spark")
  >("@buildonspark/spark-sdk/proto/spark");
  const hex = buildManifestHex(sats);
  return {
    serializedManifest: hex,
    issuerSignature: "aabb",
    attributionStatus: "NOT_PROVIDED",
    manifest: TransferManifest.decode(Uint8Array.from(Buffer.from(hex, "hex"))),
    amountSats: sats,
    amountBasis: "NET",
  };
};

beforeEach(() => {
  jest.clearAllMocks();
  mockResolve.mockResolvedValue(mockWallet as unknown as SparkWallet);
  createLightningInvoiceMock.mockResolvedValue({
    id: "receive-1",
    invoice: { encodedInvoice: "lnbc10u1ptest" },
  });
});

describe("handleLightningReceiveQuote", () => {
  it("surfaces the manifest, its fees and the attribution status", async () => {
    getLightningReceiveQuoteMock.mockResolvedValue(quoteResult(1000));

    const result = await handleLightningReceiveQuote({
      amountSats: 1000,
      resolve: mockResolve,
    });

    expect(result.isError).toBeFalsy();
    const body = bodyOf(result);
    expect(body["serializedManifest"]).toBe(SERIALIZED_MANIFEST);
    expect(body["attributionStatus"]).toBe("NOT_PROVIDED");
    expect(body["invoicedSats"]).toBe(1000);
    expect(body["feeSats"]).toBe(0);
    expect(body["manifestTransferId"]).toBe(
      "0197f9a0-0000-7000-8000-000000000001",
    );
  });

  it("passes the basis and partner token through to the SDK", async () => {
    getLightningReceiveQuoteMock.mockResolvedValue(quoteResult(1000));

    await handleLightningReceiveQuote({
      amountSats: 1000,
      amountBasis: "GROSS",
      partnerJwt: "jwt-token",
      resolve: mockResolve,
    });

    expect(getLightningReceiveQuoteMock).toHaveBeenCalledWith({
      amountSats: 1000,
      amountBasis: "GROSS",
      partnerJwt: "jwt-token",
    });
  });

  it("passes a third-party payee through to the SDK", async () => {
    getLightningReceiveQuoteMock.mockResolvedValue(quoteResult(1000));
    const payee = "03".repeat(33);

    await handleLightningReceiveQuote({
      amountSats: 1000,
      receiverIdentityPubkey: payee,
      resolve: mockResolve,
      output: "normal",
    });

    expect(getLightningReceiveQuoteMock.mock.calls[0]?.[0]).toMatchObject({
      receiverIdentityPubkey: payee,
    });
  });

  it("reports a quote failure as an error", async () => {
    getLightningReceiveQuoteMock.mockRejectedValue(new Error("knob is off"));

    const result = await handleLightningReceiveQuote({
      amountSats: 1000,
      resolve: mockResolve,
    });

    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("knob is off");
  });
});

describe("handleCreateInvoiceFromQuote", () => {
  it("invoices the echoed bytes rather than a rebuilt manifest", async () => {
    const result = await handleCreateInvoiceFromQuote({
      serializedManifest: SERIALIZED_MANIFEST,
      issuerSignature: "aabb",
      amountSats: 1000,
      memo: "memo",
      mnemonic: "seed words",
      resolve: mockResolve,
    });

    expect(result.isError).toBeFalsy();
    expect(mockResolve).toHaveBeenCalledWith("seed words");
    const passed = createLightningInvoiceMock.mock.calls[0]?.[0];
    expect(passed?.amountSats).toBe(1000);
    expect(passed?.memo).toBe("memo");
    expect(
      (passed?.quote as { serializedManifest: string }).serializedManifest,
    ).toBe(SERIALIZED_MANIFEST);
    expect((passed?.quote as { issuerSignature: string }).issuerSignature).toBe(
      "aabb",
    );
  });

  it("invoices a delegated quote for its named payee", async () => {
    const payee = "02".repeat(33);

    await handleCreateInvoiceFromQuote({
      serializedManifest: SERIALIZED_MANIFEST,
      issuerSignature: "aabb",
      amountSats: 1000,
      receiverIdentityPubkey: payee,
      resolve: mockResolve,
      output: "normal",
    });

    expect(createLightningInvoiceMock.mock.calls[0]?.[0]).toMatchObject({
      receiverIdentityPubkey: payee,
    });
  });

  it("surfaces a refusal to reuse the same quote", async () => {
    createLightningInvoiceMock.mockRejectedValue(
      new Error("this quote has already been committed; request a new one"),
    );

    const result = await handleCreateInvoiceFromQuote({
      serializedManifest: SERIALIZED_MANIFEST,
      issuerSignature: "aabb",
      amountSats: 1000,
      resolve: mockResolve,
    });

    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("already been committed");
  });

  it("reports a malformed issuer signature as an error", async () => {
    const result = await handleCreateInvoiceFromQuote({
      serializedManifest: SERIALIZED_MANIFEST,
      issuerSignature: "zzzz",
      amountSats: 1000,
      resolve: mockResolve,
    });

    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain(
      "issuerSignature is not valid hex",
    );
    expect(createLightningInvoiceMock).not.toHaveBeenCalled();
  });

  it("reports malformed manifest hex as an error", async () => {
    const result = await handleCreateInvoiceFromQuote({
      // Even length, so the character-class guard is what has to reject it —
      // Buffer.from would silently truncate at the first non-hex byte.
      serializedManifest: `${SERIALIZED_MANIFEST.slice(0, -2)}zz`,
      issuerSignature: "aabb",
      amountSats: 1000,
      resolve: mockResolve,
    });

    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("not valid hex");
    expect(createLightningInvoiceMock).not.toHaveBeenCalled();
  });
});

describe("handleCreateQuotedInvoice", () => {
  it("quotes and invoices in one call", async () => {
    getLightningReceiveQuoteMock.mockResolvedValue(quoteResult(1000));

    const result = await handleCreateQuotedInvoice({
      amountSats: 1000,
      memo: "memo",
      partnerJwt: "jwt-token",
      resolve: mockResolve,
    });

    // memo and partnerJwt are adjacent same-typed optionals, so setting both is
    // what makes a transposition detectable.
    expect(getLightningReceiveQuoteMock).toHaveBeenCalledWith({
      amountSats: 1000,
      amountBasis: "NET",
      partnerJwt: "jwt-token",
    });
    expect(createLightningInvoiceMock.mock.calls[0]?.[0]?.memo).toBe("memo");
    expect(result.isError).toBeFalsy();
    const body = bodyOf(result);
    expect(body["invoice"]).toBe("lnbc10u1ptest");
    expect(body["invoicedSats"]).toBe(1000);
    // The only way an agent learns a partner token was rejected.
    expect(body["attributionStatus"]).toBe("NOT_PROVIDED");
    expect(getLightningReceiveQuoteMock).toHaveBeenCalledTimes(1);
    expect(createLightningInvoiceMock).toHaveBeenCalledTimes(1);
  });

  it("names the same payee on both the quote and the invoice", async () => {
    getLightningReceiveQuoteMock.mockResolvedValue(quoteResult(1000));
    const payee = "02".repeat(33);

    await handleCreateQuotedInvoice({
      amountSats: 1000,
      receiverIdentityPubkey: payee,
      resolve: mockResolve,
      output: "normal",
    });

    // Two independent forwards; a mismatch signs an attestation the SSP refuses.
    expect(getLightningReceiveQuoteMock.mock.calls[0]?.[0]).toMatchObject({
      receiverIdentityPubkey: payee,
    });
    expect(createLightningInvoiceMock.mock.calls[0]?.[0]).toMatchObject({
      receiverIdentityPubkey: payee,
    });
  });

  it("does not invoice when the quote fails", async () => {
    getLightningReceiveQuoteMock.mockRejectedValue(new Error("quote refused"));

    const result = await handleCreateQuotedInvoice({
      amountSats: 1000,
      resolve: mockResolve,
    });

    expect(result.isError).toBe(true);
    expect(createLightningInvoiceMock).not.toHaveBeenCalled();
  });
});

describe("fee-bearing quotes", () => {
  it("surfaces the fee total and the edge and fee breakdown", async () => {
    getLightningReceiveQuoteMock.mockResolvedValue(feeBearingQuote());

    const body = bodyOf(
      await handleLightningReceiveQuote({
        amountSats: 100_000,
        partnerJwt: "jwt",
        resolve: mockResolve,
      }),
    );

    expect(body["invoicedSats"]).toBe(102_000);
    expect(body["feeSats"]).toBe(2_000);
    expect(body["attributionStatus"]).toBe("ATTRIBUTED");
    expect(body["edges"]).toEqual([
      { receiver: RECEIVER, sats: 100_000 },
      { receiver: SSP_KEY, sats: 2_000 },
    ]);
    expect(body["fees"]).toEqual([{ receiver: SSP_KEY, role: 3, sats: 2_000 }]);
  });

  it("invoices for the quoted net, not the gross", async () => {
    getLightningReceiveQuoteMock.mockResolvedValue(feeBearingQuote());

    const body = bodyOf(
      await handleCreateQuotedInvoice({
        amountSats: 100_000,
        memo: "memo",
        resolve: mockResolve,
      }),
    );

    // The SDK refuses to sign a quote whose amount is not the amount quoted,
    // so handing it the gross here would be rejected against any real SSP.
    expect(createLightningInvoiceMock.mock.calls[0]?.[0]?.amountSats).toBe(
      100_000,
    );
    expect(createLightningInvoiceMock.mock.calls[0]?.[0]?.memo).toBe("memo");
    expect(body["invoicedSats"]).toBe(102_000);
    expect(body["feeSats"]).toBe(2_000);
  });

  it("defaults the basis to NET and honours an explicit GROSS on the invoice call", async () => {
    getLightningReceiveQuoteMock.mockResolvedValue(feeBearingQuote());
    await handleLightningReceiveQuote({
      amountSats: 100_000,
      resolve: mockResolve,
    });
    expect(getLightningReceiveQuoteMock).toHaveBeenCalledWith({
      amountSats: 100_000,
      amountBasis: "NET",
      partnerJwt: undefined,
    });

    await handleCreateInvoiceFromQuote({
      serializedManifest: SERIALIZED_MANIFEST,
      issuerSignature: "aabb",
      amountSats: 1000,
      amountBasis: "GROSS",
      resolve: mockResolve,
    });
    const sentQuote = createLightningInvoiceMock.mock.calls[0]?.[0]?.quote as {
      amountBasis: string;
    };
    expect(sentQuote.amountBasis).toBe("GROSS");
  });

  it("passes the mnemonic through and honours raw output", async () => {
    getLightningReceiveQuoteMock.mockResolvedValue(feeBearingQuote());

    const body = bodyOf(
      await handleLightningReceiveQuote({
        amountSats: 100_000,
        mnemonic: "seed words",
        resolve: mockResolve,
        output: "raw",
      }),
    );

    expect(mockResolve).toHaveBeenCalledWith("seed words");
    // raw is the SDK quote itself, not the summarised view
    expect(body["manifest"]).toBeDefined();
    expect(body["invoicedSats"]).toBeUndefined();
  });
});
