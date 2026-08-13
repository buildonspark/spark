import { describe, expect, it, jest } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import { bytesToHex } from "@noble/curves/utils";
import {
  FeeRole,
  FeeSource,
  Network,
  TransferManifest,
  type FeeComponent,
  type ManifestEdge,
} from "../proto/spark.js";
import { SparkWallet } from "../spark-wallet/spark-wallet.js";
import type { LightningReceiveQuote } from "../spark-wallet/types.js";
import { setSparkTokenPrimitivesOnce } from "../token-primitives-bindings/token-primitives-bindings.js";
import { SparkTokenPrimitives } from "../token-primitives-bindings/token-primitives-bindings.node.js";
import { verifyTransferManifestSignature } from "../utils/manifest-signing.js";
import { Network as WalletNetwork } from "../utils/network.js";
import { ReceiveQuoteAmountBasis } from "../utils/receive-quote.js";

setSparkTokenPrimitivesOnce(new SparkTokenPrimitives());

// Reaches private methods deliberately: the substitution under test is
// invisible from the public surface without a live SSP, and getting it wrong is
// refused only after the manifest is signed.

const RECEIVER_PRIVATE_KEY = new Uint8Array(32).fill(7);
const RECEIVER = secp256k1.getPublicKey(RECEIVER_PRIVATE_KEY, true);
const SSP = secp256k1.getPublicKey(new Uint8Array(32).fill(1), true);
const STRANGER = secp256k1.getPublicKey(new Uint8Array(32).fill(9), true);

const sats = (value: number) => ({
  amount: { $case: "sats" as const, sats: value },
});

const edge = (receiver: Uint8Array, amount: number): ManifestEdge => ({
  senderIdentityPublicKey: SSP,
  receiverIdentityPublicKey: receiver,
  amount: sats(amount),
});

const lsFee = (amount: number): FeeComponent => ({
  source: FeeSource.FEE_SOURCE_PARTNER_MARKUP,
  role: FeeRole.FEE_ROLE_LS,
  amount: sats(amount),
  receiverIdentityPublicKey: SSP,
});

const manifestOf = (
  edges: ManifestEdge[],
  fees: FeeComponent[] = [],
): TransferManifest => ({
  version: 1,
  transferId: "0197f9a0-0000-7000-8000-000000000001",
  network: Network.REGTEST,
  transferExpiryTime: undefined,
  edges,
  fees,
  quoteExpiryTime: new Date(Date.now() + 5 * 60 * 1000),
});

// A 2000-sat markup on a 100_000-sat net: the payer funds 102_000.
const FEE_BEARING = manifestOf(
  [edge(RECEIVER, 100_000), edge(SSP, 2_000)],
  [lsFee(2_000)],
);

const FEELESS = manifestOf([edge(RECEIVER, 100_000)]);

const quoteFor = (
  manifest: TransferManifest,
  overrides: Partial<LightningReceiveQuote> = {},
): LightningReceiveQuote => ({
  serializedManifest: bytesToHex(TransferManifest.encode(manifest).finish()),
  issuerSignature: "aabb",
  attributionStatus: "ATTRIBUTED",
  manifest,
  amountSats: 100_000,
  amountBasis: ReceiveQuoteAmountBasis.NET,
  ...overrides,
});

function walletWithSigner() {
  const signMessageWithIdentityKey = jest.fn(
    (message: Uint8Array): Promise<Uint8Array> =>
      Promise.resolve(
        secp256k1.sign(message, RECEIVER_PRIVATE_KEY).toDERRawBytes(),
      ),
  );
  const wallet = Object.create(SparkWallet.prototype) as unknown as {
    config: {
      getNetwork: () => WalletNetwork;
      signer: {
        getIdentityPublicKey: () => Promise<Uint8Array>;
        signMessageWithIdentityKey: typeof signMessageWithIdentityKey;
      };
    };
    getSspClient: () => unknown;
    validateAndCreateLightningInvoice: (
      params: Record<string, unknown>,
    ) => Promise<unknown>;
    getLightningReceiveQuote: (params: {
      amountSats: number;
      amountBasis?: ReceiveQuoteAmountBasis;
      partnerJwt?: string;
    }) => Promise<LightningReceiveQuote>;
    signReceiveQuote: (params: {
      quote: LightningReceiveQuote;
      amountSats: number;
      receiverIdentityPubkey?: string;
      includeSparkAddress: boolean;
      includeSparkInvoice: boolean;
    }) => Promise<{
      committedQuote: { serializedManifest: string; manifestSignature: string };
      invoicedSats: number;
    }>;
  };
  const quoteCalls: Record<string, unknown>[] = [];
  const receiveCalls: Record<string, unknown>[] = [];
  const sspClient = {
    requestLightningReceive: (params: Record<string, unknown>) => {
      receiveCalls.push(params);
      return Promise.resolve(null);
    },
    lightningReceiveQuote: (params: Record<string, unknown>) => {
      quoteCalls.push(params);
      return Promise.resolve({
        issuedQuote: {
          serializedManifest: bytesToHex(
            TransferManifest.encode(FEE_BEARING).finish(),
          ),
          issuerSignature: "aabb",
        },
        attributionStatus: "ATTRIBUTED",
      });
    },
  };
  wallet.config = {
    getNetwork: () => WalletNetwork.REGTEST,
    signer: {
      getIdentityPublicKey: () => Promise.resolve(RECEIVER),
      signMessageWithIdentityKey,
    },
  };
  wallet.getSspClient = () => sspClient;
  return { wallet, signMessageWithIdentityKey, quoteCalls, receiveCalls };
}

const sign = (
  wallet: ReturnType<typeof walletWithSigner>["wallet"],
  quote: LightningReceiveQuote,
  overrides: Partial<{
    amountSats: number;
    receiverIdentityPubkey: string;
    includeSparkAddress: boolean;
    includeSparkInvoice: boolean;
  }> = {},
) =>
  wallet.signReceiveQuote({
    quote,
    amountSats: 100_000,
    includeSparkAddress: false,
    includeSparkInvoice: false,
    ...overrides,
  });

describe("signing a receive quote", () => {
  it("invoices the manifest gross, not the requested net", async () => {
    const { wallet } = walletWithSigner();

    const { invoicedSats } = await sign(wallet, quoteFor(FEE_BEARING));

    expect(invoicedSats).toBe(102_000);
  });

  it("echoes the quoted bytes and signs their hash with the identity key", async () => {
    const { wallet } = walletWithSigner();
    const quote = quoteFor(FEE_BEARING);

    const { committedQuote } = await sign(wallet, quote);

    expect(committedQuote.serializedManifest).toBe(quote.serializedManifest);
    expect(
      await verifyTransferManifestSignature(
        FEE_BEARING,
        Uint8Array.from(Buffer.from(committedQuote.manifestSignature, "hex")),
        RECEIVER,
      ),
    ).toBe(true);
  });

  it("validates the serialized bytes rather than the decoded field beside them", async () => {
    const { wallet, signMessageWithIdentityKey } = walletWithSigner();
    // The decoded field describes an honest quote; the bytes that would be
    // signed pay a stranger 900_000 on top.
    const quote = quoteFor(
      manifestOf([edge(RECEIVER, 100_000), edge(STRANGER, 900_000)]),
      { manifest: manifestOf([edge(RECEIVER, 100_000)]) },
    );

    await expect(sign(wallet, quote)).rejects.toThrow(/owed 0 in fees/);
    expect(signMessageWithIdentityKey).not.toHaveBeenCalled();
  });

  it.each([
    [
      "an amount the quote was not issued for",
      { amountSats: 99_999 },
      /different amount/,
    ],
    [
      "a receiver other than this wallet",
      { receiverIdentityPubkey: bytesToHex(STRANGER) },
      /other than this wallet/,
    ],
    [
      "a fee-bearing quote with a Spark fallback",
      { includeSparkAddress: true },
      /Spark fallback/,
    ],
  ])("refuses %s before signing", async (_name, overrides, expected) => {
    const { wallet, signMessageWithIdentityKey } = walletWithSigner();

    await expect(
      sign(wallet, quoteFor(FEE_BEARING), overrides),
    ).rejects.toThrow(expected);
    expect(signMessageWithIdentityKey).not.toHaveBeenCalled();
  });

  it("still allows a Spark fallback on a feeless quote", async () => {
    const { wallet } = walletWithSigner();

    const { invoicedSats } = await sign(wallet, quoteFor(FEELESS), {
      includeSparkAddress: true,
    });

    expect(invoicedSats).toBe(100_000);
  });

  it("names includeSparkInvoice when that is the conflicting flag", async () => {
    const { wallet } = walletWithSigner();

    // The message is identical for either flag, so only the field metadata
    // distinguishes them.
    await expect(
      sign(wallet, quoteFor(FEE_BEARING), { includeSparkInvoice: true }),
    ).rejects.toThrow(/field: includeSparkInvoice/);
    await expect(
      sign(wallet, quoteFor(FEE_BEARING), { includeSparkAddress: true }),
    ).rejects.toThrow(/field: includeSparkAddress/);
  });

  it("refuses a quote carrying no expiry at all", async () => {
    const { wallet, signMessageWithIdentityKey } = walletWithSigner();
    const undated = manifestOf([edge(RECEIVER, 100_000)]);
    undated.quoteExpiryTime = undefined;

    await expect(sign(wallet, quoteFor(undated))).rejects.toThrow(/expired/);
    expect(signMessageWithIdentityKey).not.toHaveBeenCalled();
  });

  it("signs on a network the quote request collapses onto regtest", async () => {
    // The SSP is asked for REGTEST on every non-mainnet network, so validating
    // against the wallet's own network would refuse its own quote here.
    const { wallet } = walletWithSigner();
    (
      wallet as unknown as { config: { getNetwork: () => WalletNetwork } }
    ).config.getNetwork = () => WalletNetwork.SIGNET;

    const { invoicedSats } = await sign(wallet, quoteFor(FEE_BEARING));

    expect(invoicedSats).toBe(102_000);
  });

  it("refuses a quote minted for another network before signing", async () => {
    const { wallet, signMessageWithIdentityKey } = walletWithSigner();
    const mainnet = manifestOf([edge(RECEIVER, 100_000)]);
    mainnet.network = Network.MAINNET;

    await expect(sign(wallet, quoteFor(mainnet))).rejects.toThrow(
      /different network/,
    );
    expect(signMessageWithIdentityKey).not.toHaveBeenCalled();
  });

  it("accepts the receiver key in either hex case", async () => {
    const { wallet } = walletWithSigner();

    const { invoicedSats } = await sign(wallet, quoteFor(FEE_BEARING), {
      receiverIdentityPubkey: bytesToHex(RECEIVER).toUpperCase(),
    });

    expect(invoicedSats).toBe(102_000);
  });

  it("refuses a quote whose expiry is not a representable date", async () => {
    const { wallet, signMessageWithIdentityKey } = walletWithSigner();
    // Only reachable from the wire: an out-of-range seconds decodes to an
    // Invalid Date, which is truthy and compares false against every bound.
    // It cannot be built by encoding a Date, since that conversion throws.
    const undated = manifestOf([edge(RECEIVER, 100_000)]);
    undated.quoteExpiryTime = undefined;
    const withHugeExpiry = new Uint8Array([
      ...TransferManifest.encode(undated).finish(),
      0x3a, // field 7 (quote_expiry_time), length-delimited
      0x07,
      0x08, // field 1 (seconds), varint
      ...[0x80, 0x90, 0xbf, 0xa0, 0xa5, 0x9d, 0x12],
    ]);
    expect(
      Number.isNaN(
        TransferManifest.decode(withHugeExpiry).quoteExpiryTime?.getTime() ?? 0,
      ),
    ).toBe(true);

    await expect(
      sign(wallet, {
        ...quoteFor(undated),
        serializedManifest: bytesToHex(withHugeExpiry),
      }),
    ).rejects.toThrow(/expired/);
    expect(signMessageWithIdentityKey).not.toHaveBeenCalled();
  });

  it("signs the bytes it validated even if the quote is mutated mid-flight", async () => {
    const { wallet } = walletWithSigner();
    const quote = quoteFor(FEE_BEARING);
    const original = quote.serializedManifest;
    // The quote is caller-owned; swapping its fields after the checks must not
    // change which bytes are signed or which signature they are paired with.
    (
      wallet as unknown as {
        config: { signer: { getIdentityPublicKey: () => Promise<Uint8Array> } };
      }
    ).config.signer.getIdentityPublicKey = () => {
      quote.serializedManifest = bytesToHex(
        TransferManifest.encode(FEELESS).finish(),
      );
      quote.issuerSignature = "ffff";
      return Promise.resolve(RECEIVER);
    };

    const { committedQuote, invoicedSats } = await sign(wallet, quote);

    expect(committedQuote.serializedManifest).toBe(original);
    expect(invoicedSats).toBe(102_000);
  });

  it("refuses an expired quote before signing", async () => {
    const { wallet, signMessageWithIdentityKey } = walletWithSigner();
    const expired = manifestOf([edge(RECEIVER, 100_000)]);
    expired.quoteExpiryTime = new Date(Date.now() - 1000);

    await expect(sign(wallet, quoteFor(expired))).rejects.toThrow(/expired/);
    expect(signMessageWithIdentityKey).not.toHaveBeenCalled();
  });
});

describe("requesting a receive quote", () => {
  it("stamps the requested amount and basis onto the quote", async () => {
    const { wallet } = walletWithSigner();

    const quote = await wallet.getLightningReceiveQuote({
      amountSats: 100_000,
    });

    // Stamped from the request, not echoed from the response — the signing-side
    // amount guard compares against these, so echoing would make it vacuous.
    expect(quote.amountSats).toBe(100_000);
    expect(quote.amountBasis).toBe(ReceiveQuoteAmountBasis.NET);
    expect(quote.manifest.transferId).toBe(FEE_BEARING.transferId);
    expect(quote.attributionStatus).toBe("ATTRIBUTED");
  });

  it("passes an explicit basis and partner token through to the SSP", async () => {
    const { wallet, quoteCalls } = walletWithSigner();

    await wallet.getLightningReceiveQuote({
      amountSats: 100_000,
      amountBasis: ReceiveQuoteAmountBasis.GROSS,
      partnerJwt: "token",
    });

    expect(quoteCalls[0]).toMatchObject({
      amountSats: 100_000,
      amountBasis: ReceiveQuoteAmountBasis.GROSS,
      partnerJwt: "token",
    });
  });

  it.each([0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1])(
    "refuses to request a quote for %p",
    async (amountSats) => {
      const { wallet, quoteCalls } = walletWithSigner();

      await expect(
        wallet.getLightningReceiveQuote({ amountSats }),
      ).rejects.toThrow(/Invalid amount/);
      expect(quoteCalls).toHaveLength(0);
    },
  );
});

describe("requesting the invoice for a committed quote", () => {
  it("asks the SSP for the manifest gross, not the caller's net", async () => {
    const { wallet, receiveCalls } = walletWithSigner();

    // The stub returns no invoice, so this rejects after the request — the
    // request itself is what is under test.
    await expect(
      wallet.validateAndCreateLightningInvoice({
        amountSats: 100_000,
        paymentHashHex: "00".repeat(32),
        expirySeconds: 3600,
        includeSparkAddress: false,
        includeSparkInvoice: false,
        quote: quoteFor(FEE_BEARING),
      }),
    ).rejects.toThrow();

    expect(receiveCalls).toHaveLength(1);
    expect(receiveCalls[0]).toMatchObject({ amountSats: 102_000 });
    expect(receiveCalls[0]?.["committedQuote"]).toMatchObject({
      serializedManifest: bytesToHex(
        TransferManifest.encode(FEE_BEARING).finish(),
      ),
    });
  });

  it("refuses a fee-bearing quote before minting a Spark invoice", async () => {
    const { wallet } = walletWithSigner();
    let mintedSparkInvoice = false;
    (
      wallet as unknown as { createSatsInvoice: () => Promise<string> }
    ).createSatsInvoice = () => {
      mintedSparkInvoice = true;
      return Promise.resolve("spark1test");
    };

    await expect(
      wallet.validateAndCreateLightningInvoice({
        amountSats: 100_000,
        paymentHashHex: "00".repeat(32),
        expirySeconds: 3600,
        includeSparkAddress: false,
        includeSparkInvoice: true,
        quote: quoteFor(FEE_BEARING),
      }),
    ).rejects.toThrow(/Spark fallback/);

    expect(mintedSparkInvoice).toBe(false);
  });

  it("asks for the caller's amount and sends no quote when unquoted", async () => {
    const { wallet, receiveCalls } = walletWithSigner();

    await expect(
      wallet.validateAndCreateLightningInvoice({
        amountSats: 100_000,
        paymentHashHex: "00".repeat(32),
        expirySeconds: 3600,
        includeSparkAddress: false,
        includeSparkInvoice: false,
      }),
    ).rejects.toThrow();

    expect(receiveCalls[0]).toMatchObject({ amountSats: 100_000 });
    expect(receiveCalls[0]?.["committedQuote"]).toBeUndefined();
  });
});
