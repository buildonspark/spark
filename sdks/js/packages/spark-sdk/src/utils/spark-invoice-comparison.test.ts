import { hexToBytes, numberToVarBytesBE } from "@noble/curves/utils";
import { uuidv7obj } from "uuidv7";
import { encodeSparkAddress } from "./address.js";
import { type NetworkType } from "./network.js";
import { compareSparkInvoices } from "./spark-invoice-comparison.js";

const RECEIVER_A =
  "02ccb26ba79c63aaf60c9192fd874be3087ae8d8703275df0e558704a6d3a4f132";
const RECEIVER_B =
  "026c943bfef71040371ca1c1d1ee1d5b203573dc97fdf6497a0b74e5aec0220e21";
const SENDER =
  "02b0e3203121de9df0bd7c2b3846100e25c63310392e05961d8042fa81906d6f2b";

const BASE_ID = uuidv7obj().bytes;
const BASE_EXPIRY = new Date(Date.now() + 24 * 60 * 60 * 1000);
const BASE_TOKEN_ID = new Uint8Array(32).fill(0);
BASE_TOKEN_ID[31] = 1;

type SatsOpts = {
  id?: Uint8Array;
  receiver?: string;
  network?: NetworkType;
  amount?: number;
  memo?: string;
  sender?: string;
  expiry?: Date;
};

function satsInvoice(opts: SatsOpts = {}): string {
  return encodeSparkAddress({
    identityPublicKey: opts.receiver ?? RECEIVER_A,
    network: opts.network ?? "REGTEST",
    sparkInvoiceFields: {
      version: 1,
      id: opts.id ?? BASE_ID,
      paymentType: {
        $case: "satsPayment" as const,
        satsPayment: { amount: "amount" in opts ? opts.amount : 1000 },
      },
      memo: opts.memo ?? "base-memo",
      senderPublicKey: hexToBytes(opts.sender ?? SENDER),
      expiryTime: opts.expiry ?? BASE_EXPIRY,
    },
  });
}

type TokenOpts = {
  tokenIdentifier?: Uint8Array;
  amount?: Uint8Array;
};

function tokenInvoice(opts: TokenOpts = {}): string {
  return encodeSparkAddress({
    identityPublicKey: RECEIVER_A,
    network: "REGTEST",
    sparkInvoiceFields: {
      version: 1,
      id: BASE_ID,
      paymentType: {
        $case: "tokensPayment" as const,
        tokensPayment: {
          tokenIdentifier: opts.tokenIdentifier ?? BASE_TOKEN_ID,
          amount: opts.amount ?? numberToVarBytesBE(1000n),
        },
      },
      memo: "base-memo",
      senderPublicKey: hexToBytes(SENDER),
      expiryTime: BASE_EXPIRY,
    },
  });
}

describe("compareSparkInvoices", () => {
  it("reports all facets equal for identical invoices", () => {
    const result = compareSparkInvoices(satsInvoice(), satsInvoice());
    expect(result).toEqual({
      network: true,
      version: true,
      id: true,
      receiverIdentityPublicKey: true,
      paymentType: true,
      amount: true,
      tokenIdentifier: true,
      memo: true,
      senderPublicKey: true,
      expiryTime: true,
      signature: true,
      all: true,
    });
  });

  it.each<{
    name: string;
    field: keyof SatsOpts;
    value: SatsOpts[keyof SatsOpts];
    flag: string;
  }>([
    { name: "amount", field: "amount", value: 2000, flag: "amount" },
    {
      name: "receiver",
      field: "receiver",
      value: RECEIVER_B,
      flag: "receiverIdentityPublicKey",
    },
    { name: "memo", field: "memo", value: "other-memo", flag: "memo" },
    {
      name: "sender",
      field: "sender",
      value: RECEIVER_B,
      flag: "senderPublicKey",
    },
    { name: "id", field: "id", value: uuidv7obj().bytes, flag: "id" },
    {
      name: "expiry",
      field: "expiry",
      value: new Date(Date.now() + 48 * 60 * 60 * 1000),
      flag: "expiryTime",
    },
    { name: "network", field: "network", value: "MAINNET", flag: "network" },
  ])("flags only $flag when $name differs", ({ field, value, flag }) => {
    const result = compareSparkInvoices(
      satsInvoice(),
      satsInvoice({ [field]: value }),
    );
    expect(result[flag as keyof typeof result]).toBe(false);
    expect(result.all).toBe(false);
    const stillTrue = Object.entries(result).filter(
      ([k, v]) => k !== flag && k !== "all" && v === false,
    );
    expect(stillTrue).toEqual([]);
  });

  it("treats open amount (unset) as different from a fixed amount", () => {
    const result = compareSparkInvoices(
      satsInvoice({ amount: undefined }),
      satsInvoice(),
    );
    expect(result.amount).toBe(false);
    expect(result.all).toBe(false);
  });

  it("flags payment type, amount, and token identifier when comparing sats vs tokens", () => {
    const result = compareSparkInvoices(satsInvoice(), tokenInvoice());
    expect(result.paymentType).toBe(false);
    expect(result.amount).toBe(false);
    expect(result.tokenIdentifier).toBe(false);
    expect(result.receiverIdentityPublicKey).toBe(true);
    expect(result.id).toBe(true);
    expect(result.all).toBe(false);
  });

  it("flags only amount when two token invoices differ in amount", () => {
    const result = compareSparkInvoices(
      tokenInvoice(),
      tokenInvoice({ amount: numberToVarBytesBE(2000n) }),
    );
    expect(result.amount).toBe(false);
    expect(result.tokenIdentifier).toBe(true);
    expect(result.paymentType).toBe(true);
    expect(result.all).toBe(false);
  });

  it("flags only token identifier when two token invoices differ in identifier", () => {
    const otherTokenId = new Uint8Array(32).fill(0);
    otherTokenId[31] = 2;
    const result = compareSparkInvoices(
      tokenInvoice(),
      tokenInvoice({ tokenIdentifier: otherTokenId }),
    );
    expect(result.tokenIdentifier).toBe(false);
    expect(result.amount).toBe(true);
    expect(result.paymentType).toBe(true);
    expect(result.all).toBe(false);
  });

  it("throws when an input cannot be decoded", () => {
    expect(() =>
      compareSparkInvoices("spark:not-a-valid-invoice", satsInvoice()),
    ).toThrow();
  });

  it("throws when a spark address carries no invoice fields", () => {
    const plainAddress = encodeSparkAddress({
      identityPublicKey: RECEIVER_A,
      network: "REGTEST",
    });
    expect(() => compareSparkInvoices(plainAddress, satsInvoice())).toThrow(
      /not an invoice/,
    );
  });
});
