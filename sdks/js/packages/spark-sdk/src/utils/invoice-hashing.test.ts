import { bytesToHex, hexToBytes } from "@noble/hashes/utils";
import { HashSparkInvoice } from "./invoice-hashing.js";
import { type SparkInvoiceFields } from "../proto/spark.js";
import { UUID } from "uuidv7";
import { type NetworkType } from "./network.js";

it("produces a stable hash from known inputs", () => {
  const identityPKHex =
    "026c943bfef71040371ca1c1d1ee1d5b203573dc97fdf6497a0b74e5aec0220e21";
  const tokenIdHex =
    "49046dd67bbe5fc8e3abb45bc4f809b9cb5cb5871a19292fa5c7120389641363";
  const senderPKHex =
    "02b0e3203121de9df0bd7c2b3846100e25c63310392e05961d8042fa81906d6f2b";
  const idStr = "0198b4ec-3d20-7e4b-b288-1107ecf64d49";
  const expiryStr = "2025-08-16T22:12:17.791Z";

  const amountBytes = bigintToMinimalBE(1000n);
  const network: NetworkType = "REGTEST";

  const sparkInvoiceFields: SparkInvoiceFields = {
    version: 1,
    id: UUID.parse(idStr).bytes,
    paymentType: {
      $case: "tokensPayment",
      tokensPayment: {
        tokenIdentifier: hexToBytes(tokenIdHex),
        amount: amountBytes,
      },
    },
    memo: "memo",
    senderPublicKey: hexToBytes(senderPKHex),
    expiryTime: new Date(expiryStr),
  };

  const hash = HashSparkInvoice(
    sparkInvoiceFields,
    hexToBytes(identityPKHex),
    network,
  );

  expect(bytesToHex(hash)).toBe(
    "21f91b971cccc74f76fcac5384ba99d8629baff87d602f9614f6c032a2e6fb2d",
  );
});

function bigintToMinimalBE(n: bigint): Uint8Array {
  if (n < 0n) throw new Error("amount must be >= 0");
  if (n === 0n) return new Uint8Array();
  let hex = n.toString(16);
  if (hex.length % 2) hex = "0" + hex;
  return hexToBytes(hex);
}

// Helpers to build valid SparkInvoiceFields quickly
function makeTokensFields(
  overrides: Partial<{
    id: Uint8Array;
    version: number;
    tokenIdentifier: Uint8Array | undefined;
    amount: Uint8Array | undefined;
    memo: string | undefined;
    senderPublicKey: Uint8Array | undefined;
    expiryTime: Date | undefined;
  }> = {},
): SparkInvoiceFields {
  const id =
    overrides.id ?? UUID.parse("11111111-2222-4333-8444-555555555555").bytes;
  const version = overrides.version ?? 1;
  return {
    version,
    id,
    paymentType: {
      $case: "tokensPayment",
      tokensPayment: {
        tokenIdentifier:
          overrides.tokenIdentifier ??
          hexToBytes(
            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
          ),
        amount: overrides.amount,
      },
    },
    memo: overrides.memo,
    senderPublicKey: overrides.senderPublicKey,
    expiryTime: overrides.expiryTime,
  };
}

function makeSatsFields(
  overrides: Partial<{
    id: Uint8Array;
    version: number;
    amount: number | undefined;
    memo: string | undefined;
    senderPublicKey: Uint8Array | undefined;
    expiryTime: Date | undefined;
  }> = {},
): SparkInvoiceFields {
  const id =
    overrides.id ?? UUID.parse("66666666-7777-4888-9999-000000000000").bytes;
  const version = overrides.version ?? 1;
  return {
    version,
    id,
    paymentType: {
      $case: "satsPayment",
      satsPayment: {
        amount: overrides.amount,
      },
    },
    memo: overrides.memo,
    senderPublicKey: overrides.senderPublicKey,
    expiryTime: overrides.expiryTime,
  };
}

const RECV_PK_HEX =
  "026c943bfef71040371ca1c1d1ee1d5b203573dc97fdf6497a0b74e5aec0220e21";
const RECV_PK = hexToBytes(RECV_PK_HEX);

it("tokens vs sats produce different hashes", () => {
  const tf = makeTokensFields({ amount: bigintToMinimalBE(123n) });
  const sf = makeSatsFields({ amount: 123 });
  const h1 = HashSparkInvoice(tf, RECV_PK, "REGTEST");
  const h2 = HashSparkInvoice(sf, RECV_PK, "REGTEST");
  expect(bytesToHex(h1)).not.toBe(bytesToHex(h2));
});

it("token amount minimal vs 16-byte padded produces different hashes", () => {
  const minimal = makeTokensFields({ amount: bigintToMinimalBE(1000n) }); // 0x03e8
  const padded = makeTokensFields({
    amount: hexToBytes("000000000000000000000000000003e8"),
  }); // 16-byte
  const h1 = HashSparkInvoice(minimal, RECV_PK, "REGTEST");
  const h2 = HashSparkInvoice(padded, RECV_PK, "REGTEST");
  expect(bytesToHex(h1)).not.toBe(bytesToHex(h2));
});

it("nil vs empty equivalences (amount, memo, senderPublicKey)", () => {
  const base = makeTokensFields({
    amount: undefined,
    memo: undefined,
    senderPublicKey: undefined,
  });
  const empty = makeTokensFields({
    amount: new Uint8Array(),
    memo: "",
    senderPublicKey: new Uint8Array(0),
  });
  const h1 = HashSparkInvoice(base, RECV_PK, "REGTEST");
  const h2 = HashSparkInvoice(empty, RECV_PK, "REGTEST");
  expect(bytesToHex(h1)).toBe(bytesToHex(h2));
});

it("LOCAL network hashes same as REGTEST for the network field contribution", () => {
  const f = makeTokensFields({ amount: bigintToMinimalBE(1n) });
  const hReg = HashSparkInvoice(f, RECV_PK, "REGTEST");
  const hLocal = HashSparkInvoice(f, RECV_PK, "LOCAL");
  expect(bytesToHex(hLocal)).toBe(bytesToHex(hReg));
});

it("expiry uses seconds only and treats undefined == epoch zero", () => {
  const a = makeTokensFields({ expiryTime: undefined });
  const b = makeTokensFields({ expiryTime: new Date(0) });
  const h1 = HashSparkInvoice(a, RECV_PK, "REGTEST");
  const h2 = HashSparkInvoice(b, RECV_PK, "REGTEST");
  expect(bytesToHex(h1)).toBe(bytesToHex(h2));

  const c = makeTokensFields({ expiryTime: new Date(1000) }); // 1 second
  const h3 = HashSparkInvoice(c, RECV_PK, "REGTEST");
  expect(bytesToHex(h3)).not.toBe(bytesToHex(h2));
});

it("deterministic and stable across identical objects and clones", () => {
  const f = makeTokensFields({ amount: bigintToMinimalBE(42n), memo: "m" });
  const h1 = HashSparkInvoice(f, RECV_PK, "REGTEST");
  const fClone = JSON.parse(JSON.stringify(f)) as unknown as SparkInvoiceFields;
  // Re-hydrate types lost by JSON (id/tokenIdentifier/amount) where needed
  fClone.id = new Uint8Array(f.id);
  if (fClone.paymentType?.$case === "tokensPayment") {
    if (f.paymentType?.$case === "tokensPayment") {
      fClone.paymentType.tokensPayment.tokenIdentifier = new Uint8Array(
        f.paymentType.tokensPayment.tokenIdentifier!,
      );
      fClone.paymentType.tokensPayment.amount = new Uint8Array(
        f.paymentType.tokensPayment.amount!,
      );
    }
  }
  const h2 = HashSparkInvoice(f, RECV_PK, "REGTEST");
  const h3 = HashSparkInvoice(fClone, RECV_PK, "REGTEST");
  expect(bytesToHex(h1)).toBe(bytesToHex(h2));
  expect(bytesToHex(h1)).toBe(bytesToHex(h3));
});

it("hash changes if receiver public key changes by 1 bit", () => {
  const f = makeTokensFields({ amount: bigintToMinimalBE(7n) });
  const pk2 = new Uint8Array(RECV_PK);
  pk2[1] = (pk2[1] ?? 0) ^ 0x01;
  const h1 = HashSparkInvoice(f, RECV_PK, "REGTEST");
  const h2 = HashSparkInvoice(f, pk2, "REGTEST");
  expect(bytesToHex(h1)).not.toBe(bytesToHex(h2));
});

it("throws on invalid lengths and missing fields", () => {
  const badId = makeTokensFields({});
  badId.id = new Uint8Array([1, 2, 3]);
  expect(() => HashSparkInvoice(badId, RECV_PK, "REGTEST")).toThrow(
    /id must be exactly 16/,
  );

  const badTid = makeTokensFields({ tokenIdentifier: new Uint8Array(31) });
  expect(() => HashSparkInvoice(badTid, RECV_PK, "REGTEST")).toThrow(
    /token identifier/,
  );

  const badAmt = makeTokensFields({ amount: new Uint8Array(17) });
  expect(() => HashSparkInvoice(badAmt, RECV_PK, "REGTEST")).toThrow(
    /exceeds 16 bytes/,
  );

  expect(() =>
    HashSparkInvoice(makeTokensFields({}), new Uint8Array(32), "REGTEST"),
  ).toThrow(/exactly 33/);

  const missingPt: SparkInvoiceFields = {
    version: 1,
    id: UUID.parse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa").bytes,
    paymentType: undefined,
  };
  expect(() => HashSparkInvoice(missingPt, RECV_PK, "REGTEST")).toThrow(
    /unsupported or missing/i,
  );
});
