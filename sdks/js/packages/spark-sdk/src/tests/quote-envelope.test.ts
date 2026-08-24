import fs from "fs";
import { describe, expect, it } from "@jest/globals";
import { hexToBytes } from "@noble/hashes/utils";
import { setSparkTokenPrimitivesOnce } from "../token-primitives-bindings/token-primitives-bindings.js";
import { SparkTokenPrimitives } from "../token-primitives-bindings/token-primitives-bindings.node.js";
import {
  QuoteReason,
  QuoteRole,
  quoteEnvelopeDigest,
  receiveAttestorTarget,
} from "../utils/quote-envelope.js";

// The digest lives in the Rust core behind the token-primitives binding, which
// the SDK entry points normally register; tests import modules directly, so
// register the real (wasm-backed) node binding here.
setSparkTokenPrimitivesOnce(new SparkTokenPrimitives());

type DigestTestCase = {
  name: string;
  network: number;
  manifestHash: string;
  reason: number;
  role: number;
  target: string;
  expectedDigest: string;
};

type DistinctDigestPair = { name: string; a: string; b: string };

type InvalidTestCase = {
  name: string;
  call: string;
  expectedError: string;
  network?: number;
  manifestHash?: string;
  reason?: number;
  role?: number;
  target?: string;
  paymentHash?: string;
};

type TargetTestCase = {
  name: string;
  call: string;
  ports?: string[];
  paymentHash?: string;
  expectedDigest: string;
};

type QuoteEnvelopeDataset = {
  reasons?: Record<string, number>;
  roles?: Record<string, number>;
  testCases?: DigestTestCase[];
  distinctDigestPairs?: DistinctDigestPair[];
  invalidCases?: InvalidTestCase[];
  targetCases?: TargetTestCase[];
};

describe("Cross-Language Quote Envelope", () => {
  const fixture = new URL(
    "../../../../../../spark/testdata/quote_envelope_cases.json",
    import.meta.url,
  );

  let jsonData: QuoteEnvelopeDataset | null = null;
  try {
    jsonData = JSON.parse(
      fs.readFileSync(fixture, "utf8"),
    ) as QuoteEnvelopeDataset;
  } catch (err: unknown) {
    // Absence is a legitimate skip only in mirrored checkouts without the
    // spark/testdata tree; if the tree exists but the fixture doesn't, the
    // path regressed and silently skipping would leave the parity suite green
    // while asserting nothing.
    if ((err as NodeJS.ErrnoException).code !== "ENOENT") {
      throw err;
    }
    if (fs.existsSync(new URL(".", fixture))) {
      throw new Error(
        `quote envelope fixture directory exists but the fixture is missing: ${fixture.pathname}`,
      );
    }
  }

  const digestCases = jsonData?.testCases ?? [];
  const distinctPairs = jsonData?.distinctDigestPairs ?? [];
  const invalidCases = jsonData?.invalidCases ?? [];
  // The other target derivations exist only in Rust; TS binds the one the
  // operators verify.
  const targetCases = (jsonData?.targetCases ?? []).filter(
    (c) => c.call === "receiveAttestorTarget",
  );

  const escapeRegExp = (value: string) =>
    value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

  const digest = (c: DigestTestCase) =>
    quoteEnvelopeDigest({
      network: c.network,
      manifestHash: hexToBytes(c.manifestHash),
      reason: c.reason,
      role: c.role,
      target: hexToBytes(c.target),
    });

  // Nothing else reads these enums: every case takes reason and role from the
  // fixture, so a collision between members passes the entire suite.
  it("numbers its enums the way the shared core does", () => {
    expect(jsonData?.reasons).toBeDefined();
    expect(jsonData?.roles).toBeDefined();

    const reasons = jsonData?.reasons ?? {};
    const roles = jsonData?.roles ?? {};

    for (const [name, value] of Object.entries(reasons)) {
      expect([name, QuoteReason[name as keyof typeof QuoteReason]]).toEqual([
        name,
        value,
      ]);
    }
    for (const [name, value] of Object.entries(roles)) {
      expect([name, QuoteRole[name as keyof typeof QuoteRole]]).toEqual([
        name,
        value,
      ]);
    }

    const memberNames = (e: object) =>
      Object.keys(e).filter((k) => Number.isNaN(Number(k)));
    expect(memberNames(QuoteReason).sort()).toEqual(
      Object.keys(reasons).sort(),
    );
    expect(memberNames(QuoteRole).sort()).toEqual(Object.keys(roles).sort());
  });

  // A filtered parametrize that silently empties would report green while
  // asserting nothing, so pin that each set was actually populated.
  it("loaded the shared vectors", () => {
    expect(jsonData).not.toBeNull();
    expect(digestCases.length).toBeGreaterThan(0);
    expect(distinctPairs.length).toBeGreaterThan(0);
    expect(invalidCases.length).toBeGreaterThan(0);
    expect(targetCases.length).toBeGreaterThan(0);
  });

  it.each(digestCases.map((c) => [c.name, c] as const))(
    "digest matches the shared vector: %s",
    async (_name, testCase) => {
      const actual = await digest(testCase);
      expect(Buffer.from(actual).toString("hex")).toBe(testCase.expectedDigest);
    },
  );

  it.each(distinctPairs.map((p) => [p.name, p] as const))(
    "digests stay distinct: %s",
    async (_name, pair) => {
      const a = digestCases.find((c) => c.name === pair.a);
      const b = digestCases.find((c) => c.name === pair.b);
      if (!a || !b) {
        throw new Error(
          `distinct-digest pair ${pair.name} names a case absent from the fixture`,
        );
      }
      expect(Buffer.from(await digest(a)).toString("hex")).not.toBe(
        Buffer.from(await digest(b)).toString("hex"),
      );
    },
  );

  it.each(targetCases.map((c) => [c.name, c] as const))(
    "receive attestor target matches the shared vector: %s",
    async (_name, testCase) => {
      const actual = await receiveAttestorTarget(
        hexToBytes(testCase.paymentHash ?? ""),
      );
      expect(Buffer.from(actual).toString("hex")).toBe(testCase.expectedDigest);
    },
  );

  it.each(invalidCases.map((c) => [c.name, c] as const))(
    "refuses the invalid input: %s",
    async (_name, testCase) => {
      // The fixture names the receive target derivation by its construction
      // rather than by the exported binding.
      const call =
        testCase.call === "receiveAttestationTarget"
          ? receiveAttestorTarget(hexToBytes(testCase.paymentHash ?? ""))
          : quoteEnvelopeDigest({
              network: testCase.network ?? 0,
              manifestHash: hexToBytes(testCase.manifestHash ?? ""),
              reason: testCase.reason ?? 0,
              role: testCase.role ?? 0,
              target: hexToBytes(testCase.target ?? ""),
            });

      // Two cases alias a valid network only once truncated to u32, and the
      // wrapper refuses them before the core can report the aliased name —
      // catching that truncation is what those cases are for.
      const refusedEarlier =
        "quote envelope not hashable: network is outside uint32 range";
      await expect(call).rejects.toThrow(
        new RegExp(
          `${escapeRegExp(testCase.expectedError)}|${escapeRegExp(refusedEarlier)}`,
        ),
      );
    },
  );

  // wasm coerces a JS number to u32 at the boundary, so a fractional
  // discriminant would reach Rust as a different valid one rather than being
  // refused. These have no shared vector because only TS can express them.
  it.each([
    ["network", { network: 1.5 }],
    ["reason", { reason: 1.5 }],
    ["role", { role: 2.5 }],
  ])("refuses a fractional %s before crossing into wasm", async (_f, over) => {
    const base = digestCases[0];
    if (!base) {
      throw new Error("fixture supplied no digest case to vary");
    }
    await expect(
      quoteEnvelopeDigest({
        network: base.network,
        manifestHash: hexToBytes(base.manifestHash),
        reason: base.reason,
        role: base.role,
        target: hexToBytes(base.target),
        ...over,
      }),
    ).rejects.toThrow(/must be an integer/);
  });
});
