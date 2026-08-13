import { describe, expect, it } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import {
  FeeRole,
  FeeSource,
  Network,
  type FeeComponent,
  type ManifestEdge,
  type TransferManifest,
} from "../proto/spark.js";
import {
  manifestGrossSats,
  ReceiveQuoteAmountBasis,
  validateQuotedManifestAmounts,
} from "../utils/receive-quote.js";

// Driven through the exported check rather than createLightningInvoice: this is
// the gate the identity-key attestation is taken behind, and the quotes it has
// to refuse are ones no reachable SSP produces on demand.

const key = (seed: number) =>
  secp256k1.getPublicKey(new Uint8Array(32).fill(seed), true);

const SSP = key(1);
const RECEIVER = key(2);
const AFFILIATE = key(3);
const PARTNER = key(4);

const sats = (value: number) => ({
  amount: { $case: "sats" as const, sats: value },
});

const edge = (receiver: Uint8Array, amount: number): ManifestEdge => ({
  senderIdentityPublicKey: SSP,
  receiverIdentityPublicKey: receiver,
  amount: sats(amount),
});

const fee = (
  receiver: Uint8Array,
  amount: number,
  role: FeeRole,
): FeeComponent => ({
  source: FeeSource.FEE_SOURCE_PARTNER_MARKUP,
  role,
  amount: sats(amount),
  receiverIdentityPublicKey: receiver,
});

const manifest = (
  edges: ManifestEdge[],
  fees: FeeComponent[] = [],
): TransferManifest => ({
  version: 1,
  transferId: "0197f9a0-0000-7000-8000-000000000001",
  network: Network.REGTEST,
  transferExpiryTime: undefined,
  edges,
  fees,
  quoteExpiryTime: new Date("2026-08-11T00:05:00Z"),
});

const feeless = manifest([edge(RECEIVER, 100_000)]);

// What the SSP quotes for an attributed 200bps markup: the receiver still nets
// what they asked for, each fee payee gets an edge, and the payer funds 102_000.
// Built here rather than fetched — a manifest is just bytes, so none of the
// fee-producing server path has to exist for this to be the real shape.
const feeBearingNet = manifest(
  [
    edge(RECEIVER, 100_000),
    edge(AFFILIATE, 1_000),
    edge(PARTNER, 700),
    edge(SSP, 300),
  ],
  [
    fee(AFFILIATE, 1_000, FeeRole.FEE_ROLE_AFFILIATE),
    fee(PARTNER, 700, FeeRole.FEE_ROLE_PARTNER),
    fee(SSP, 300, FeeRole.FEE_ROLE_LS),
  ],
);

// The same markup pinned the other way: the invoice is exactly 100_000 and the
// receiver absorbs the 2_000.
const feeBearingGross = manifest(
  [
    edge(RECEIVER, 98_000),
    edge(AFFILIATE, 1_000),
    edge(PARTNER, 700),
    edge(SSP, 300),
  ],
  [
    fee(AFFILIATE, 1_000, FeeRole.FEE_ROLE_AFFILIATE),
    fee(PARTNER, 700, FeeRole.FEE_ROLE_PARTNER),
    fee(SSP, 300, FeeRole.FEE_ROLE_LS),
  ],
);

const validate = (
  quoted: TransferManifest,
  amountSats: number,
  basis: ReceiveQuoteAmountBasis,
  receiver: Uint8Array = RECEIVER,
) =>
  validateQuotedManifestAmounts({
    manifest: quoted,
    amountSats,
    basis,
    receiverIdentityPublicKey: receiver,
  });

describe("quoted manifest amount validation", () => {
  it("accepts a feeless quote on either basis", () => {
    expect(() =>
      validate(feeless, 100_000, ReceiveQuoteAmountBasis.NET),
    ).not.toThrow();
    expect(() =>
      validate(feeless, 100_000, ReceiveQuoteAmountBasis.GROSS),
    ).not.toThrow();
  });

  it("accepts a fee-bearing net quote whose gross exceeds the request", () => {
    expect(manifestGrossSats(feeBearingNet)).toBe(102_000);
    expect(() =>
      validate(feeBearingNet, 100_000, ReceiveQuoteAmountBasis.NET),
    ).not.toThrow();
  });

  it("accepts a fee-bearing gross quote where the receiver nets less", () => {
    expect(manifestGrossSats(feeBearingGross)).toBe(100_000);
    expect(() =>
      validate(feeBearingGross, 100_000, ReceiveQuoteAmountBasis.GROSS),
    ).not.toThrow();
  });

  it("refuses a fee-bearing quote checked on the other basis", () => {
    // The case a bare `gross == amountSats` gets backwards: it would take the
    // net quote as gross, invoice 102_000 against a 100_000 request, and attest.
    expect(() =>
      validate(feeBearingNet, 100_000, ReceiveQuoteAmountBasis.GROSS),
    ).toThrow(/does not match the requested amount/);
    expect(() =>
      validate(feeBearingGross, 100_000, ReceiveQuoteAmountBasis.NET),
    ).toThrow(/does not match the requested amount/);
  });

  it("refuses a quote that pays somebody else", () => {
    // Conservation still holds, so any whole-manifest identity accepts this.
    const elsewhere = manifest([edge(AFFILIATE, 100_000)]);
    expect(manifestGrossSats(elsewhere)).toBe(100_000);
    expect(() =>
      validate(elsewhere, 100_000, ReceiveQuoteAmountBasis.NET),
    ).toThrow(/pays the receiver on 0 edges/);
  });

  it("nets out a fee credited back to the receiver's own key", () => {
    // Edges merge by receiver key, so a receiver who is also the affiliate has
    // the fee folded into one edge; the edge alone overstates what they keep.
    const selfAffiliate = manifest(
      [edge(RECEIVER, 101_000), edge(SSP, 1_000)],
      [
        fee(RECEIVER, 1_000, FeeRole.FEE_ROLE_AFFILIATE),
        fee(SSP, 1_000, FeeRole.FEE_ROLE_LS),
      ],
    );
    expect(() =>
      validate(selfAffiliate, 100_000, ReceiveQuoteAmountBasis.NET),
    ).not.toThrow();
    expect(() =>
      validate(selfAffiliate, 101_000, ReceiveQuoteAmountBasis.NET),
    ).toThrow(/does not match the requested amount/);
  });

  it("refuses an amount the quote does not cover", () => {
    expect(() =>
      validate(feeless, 99_999, ReceiveQuoteAmountBasis.NET),
    ).toThrow(/does not match the requested amount/);
  });

  it("refuses a bps-denominated quote rather than reading it as zero", () => {
    // Zero-amount/bps quoting is SP-3394. Until it lands the SDK refuses:
    // `.sats` on a bps oneof is undefined, which would otherwise sum to 0 and
    // sail through as a feeless manifest.
    const bps = manifest([
      {
        senderIdentityPublicKey: SSP,
        receiverIdentityPublicKey: RECEIVER,
        amount: { amount: { $case: "bps" as const, bps: 200 } },
      },
    ]);
    expect(() => validate(bps, 100_000, ReceiveQuoteAmountBasis.NET)).toThrow(
      /basis points/,
    );
  });

  it("sums every fee credited to the same key", () => {
    // One payee, two fee components. A first-match read of fees[] overstates
    // the receiver's net and understates what a fee payee is owed.
    const twoFeesEach = manifest(
      [edge(RECEIVER, 101_500), edge(SSP, 1_500)],
      [
        fee(RECEIVER, 1_000, FeeRole.FEE_ROLE_AFFILIATE),
        fee(RECEIVER, 500, FeeRole.FEE_ROLE_PARTNER),
        fee(SSP, 1_000, FeeRole.FEE_ROLE_LS),
        fee(SSP, 500, FeeRole.FEE_ROLE_PARTNER),
      ],
    );
    expect(() =>
      validate(twoFeesEach, 100_000, ReceiveQuoteAmountBasis.NET),
    ).not.toThrow();
    expect(() =>
      validate(twoFeesEach, 100_500, ReceiveQuoteAmountBasis.NET),
    ).toThrow(/does not match the requested amount/);
  });

  it("refuses an edge to a key that is owed no fee", () => {
    const stranger = manifest([
      edge(RECEIVER, 100_000),
      edge(AFFILIATE, 900_000),
    ]);
    expect(() =>
      validate(stranger, 100_000, ReceiveQuoteAmountBasis.NET),
    ).toThrow(/pays 900000 sats to a key owed 0 in fees/);
  });

  it("refuses an edge that overpays the fee it is backed by", () => {
    const overpaid = manifest(
      [edge(RECEIVER, 100_000), edge(AFFILIATE, 5_000)],
      [fee(AFFILIATE, 1_000, FeeRole.FEE_ROLE_AFFILIATE)],
    );
    expect(() =>
      validate(overpaid, 100_000, ReceiveQuoteAmountBasis.NET),
    ).toThrow(/pays 5000 sats to a key owed 1000 in fees/);
  });

  it("refuses more than one edge to the receiver", () => {
    const split = manifest([edge(RECEIVER, 60_000), edge(RECEIVER, 40_000)]);
    expect(() => validate(split, 100_000, ReceiveQuoteAmountBasis.NET)).toThrow(
      /pays the receiver on 2 edges/,
    );
  });

  it("refuses an edge with no amount rather than reading it as zero", () => {
    const unset = manifest([
      {
        senderIdentityPublicKey: SSP,
        receiverIdentityPublicKey: RECEIVER,
        amount: undefined,
      },
    ]);
    expect(() => validate(unset, 100_000, ReceiveQuoteAmountBasis.NET)).toThrow(
      /has no amount/,
    );
  });

  it("refuses a fee that no edge funds", () => {
    // Edges alone look honest; the declared payout has nothing behind it.
    const unfunded = manifest(
      [edge(RECEIVER, 100_000)],
      [fee(AFFILIATE, 1_000, FeeRole.FEE_ROLE_AFFILIATE)],
    );
    expect(() =>
      validate(unfunded, 100_000, ReceiveQuoteAmountBasis.NET),
    ).toThrow(/does not conserve value/);
  });

  it("refuses a manifest that debits the receiving wallet", () => {
    // Conservation and the receiver's own edge both hold; only the sender is
    // wrong, and a countersignature would attest that the receiver paid.
    const debits = manifest(
      [
        { ...edge(RECEIVER, 100_000) },
        {
          senderIdentityPublicKey: RECEIVER,
          receiverIdentityPublicKey: SSP,
          amount: { amount: { $case: "sats" as const, sats: 2_000 } },
        },
      ],
      [fee(SSP, 2_000, FeeRole.FEE_ROLE_LS)],
    );
    expect(() =>
      validate(debits, 100_000, ReceiveQuoteAmountBasis.NET),
    ).toThrow(/debits the receiving wallet/);
  });

  it("refuses an unrecognized basis rather than treating it as gross", () => {
    // A basis that survived a round trip as a bare string would otherwise fall
    // into the gross branch and be signed on the wrong economics.
    expect(() =>
      validate(feeBearingNet, 100_000, "NETT" as ReceiveQuoteAmountBasis),
    ).toThrow(/Unrecognized amount basis/);
  });

  it("refuses a total that leaves the safe integer range", () => {
    const huge = Math.floor(Number.MAX_SAFE_INTEGER / 2) + 1;
    const overflowing = manifest([
      edge(RECEIVER, huge),
      edge(AFFILIATE, huge),
      edge(PARTNER, huge),
    ]);
    expect(() =>
      validate(overflowing, huge, ReceiveQuoteAmountBasis.NET),
    ).toThrow(/safe integer range/);
  });

  it("refuses a quote that leaves the receiver nothing", () => {
    const consumed = manifest(
      [edge(RECEIVER, 1_000)],
      [fee(RECEIVER, 1_000, FeeRole.FEE_ROLE_LS)],
    );
    expect(() => validate(consumed, 0, ReceiveQuoteAmountBasis.NET)).toThrow(
      /leaves the receiver nothing/,
    );
  });
});
