import { describe, expect, it } from "@jest/globals";
import { bytesToHex } from "@noble/curves/utils";
import fs from "fs";
import { TransferManifest } from "../proto/spark.js";
import { setSparkTokenPrimitivesOnce } from "../token-primitives-bindings/token-primitives-bindings.js";
import { SparkTokenPrimitives } from "../token-primitives-bindings/token-primitives-bindings.node.js";
import { hashTransferManifest } from "../utils/manifest-hashing.js";

// The hash lives in the Rust core behind the token-primitives binding, which
// the SDK entry points normally register; tests import modules directly, so
// register the real (wasm-backed) node binding here.
setSparkTokenPrimitivesOnce(new SparkTokenPrimitives());

type ManifestHashTestCase = {
  name: string;
  description: string;
  expectedHash: string;
  transferManifest: unknown;
};

type ManifestInvalidTestCase = {
  name: string;
  description: string;
  transferManifest: unknown;
};

type ManifestHashDataset = {
  testCases?: ManifestHashTestCase[];
  invalidCases?: ManifestInvalidTestCase[];
};

describe("Cross-Language TransferManifest Hash", () => {
  const candidates = [
    new URL(
      "../../../../../../spark/testdata/transfer_manifest_hash_cases.json",
      import.meta.url,
    ),
  ];

  let jsonData: ManifestHashDataset | null = null;
  for (const u of candidates) {
    try {
      const raw = fs.readFileSync(u, "utf8");
      jsonData = JSON.parse(raw) as ManifestHashDataset;
      break;
    } catch (err: unknown) {
      // Absence is a legitimate skip only in mirrored checkouts without the
      // spark/testdata tree; if the tree exists but the fixture doesn't, the
      // path regressed and silently skipping would leave the parity suite
      // green while asserting nothing. Other errors — malformed JSON
      // included — always fail loudly.
      if ((err as NodeJS.ErrnoException).code !== "ENOENT") {
        throw err;
      }
      if (fs.existsSync(new URL(".", u))) {
        throw new Error(
          `transfer manifest fixture directory exists but the fixture is missing: ${u.pathname}`,
        );
      }
    }
  }

  if (!jsonData) {
    it("skips when transfer manifest hash dataset is absent", () => {
      expect(true).toBe(true);
    });
    return;
  }

  const allCases = jsonData.testCases ?? [];

  for (const tc of allCases) {
    it(`matches expected manifest hash for ${tc.name}`, async () => {
      const manifest = TransferManifest.fromJSON(tc.transferManifest);

      const hash = await hashTransferManifest(manifest);

      expect(hash).toHaveLength(32);
      expect(bytesToHex(hash).toLowerCase()).toBe(
        String(tc.expectedHash).toLowerCase(),
      );
    });
  }

  it("does not mutate the input manifest", async () => {
    const shuffledCase = allCases.find(
      (tc) => tc.name === "multi_edge_shuffled",
    );
    expect(shuffledCase).toBeDefined();

    const manifest = TransferManifest.fromJSON(shuffledCase!.transferManifest);
    const original = TransferManifest.fromJSON(shuffledCase!.transferManifest);

    await hashTransferManifest(manifest);

    expect(TransferManifest.toJSON(manifest)).toEqual(
      TransferManifest.toJSON(original),
    );
  });

  for (const tc of jsonData.invalidCases ?? []) {
    it(`rejects invalid manifest ${tc.name}`, async () => {
      const manifest = TransferManifest.fromJSON(tc.transferManifest);

      await expect(hashTransferManifest(manifest)).rejects.toThrow(
        "transfer manifest not hashable",
      );
    });
  }

  it("rejects an unrecognized enum value from lenient JSON parsing", async () => {
    const manifest = TransferManifest.fromJSON(allCases[0]!.transferManifest);
    manifest.network = -1;

    await expect(hashTransferManifest(manifest)).rejects.toThrow(
      "network must be a known value",
    );
  });

  it("rejects a timestamp beyond the year-9999 bound", async () => {
    const manifest = TransferManifest.fromJSON(allCases[0]!.transferManifest);
    manifest.transferExpiryTime = new Date(253402300800_000);

    await expect(hashTransferManifest(manifest)).rejects.toThrow(
      "seconds out of range",
    );
  });

  it("rejects an invalid date", async () => {
    const manifest = TransferManifest.fromJSON(allCases[0]!.transferManifest);
    manifest.transferExpiryTime = new Date(Number.NaN);

    await expect(hashTransferManifest(manifest)).rejects.toThrow(
      "timestamp is an invalid date",
    );
  });

  it("fixture amounts each set exactly one oneof arm", () => {
    // ts-proto's fromJSON is lenient (it checks sats before bps, so sats
    // wins regardless of JSON order) where Go's protojson rejects duplicate
    // oneof members — a both-arms vector would silently hash one arm here
    // while fataling the Go suite. Lint the raw JSON.
    const allManifests = [
      ...(jsonData.testCases ?? []).map((tc) => tc.transferManifest),
      ...(jsonData.invalidCases ?? []).map((tc) => tc.transferManifest),
    ] as Array<{
      edges?: Array<{ amount?: Record<string, unknown> }>;
      fees?: Array<{ amount?: Record<string, unknown> }>;
    }>;
    for (const manifest of allManifests) {
      for (const holder of [
        ...(manifest.edges ?? []),
        ...(manifest.fees ?? []),
      ]) {
        if (holder.amount === undefined) {
          continue;
        }
        const arms = Object.keys(holder.amount).filter((k) =>
          ["sats", "bps"].includes(k),
        );
        expect(arms).toHaveLength(1);
      }
    }
  });

  it("rejects bps beyond uint32 range", async () => {
    // Not a shared vector: Go's protojson rejects uint32 overflow at parse
    // and Go's typed uint32 makes the branch unreachable — this guard only
    // exists for TS's lenient Number() parsing.
    const manifest = TransferManifest.fromJSON(allCases[0]!.transferManifest);
    manifest.edges[0]!.amount = { amount: { $case: "bps", bps: 4294967296 } };

    await expect(hashTransferManifest(manifest)).rejects.toThrow(
      "bps exceeds uint32 range",
    );
  });

  it("rejects non-integer and NaN amounts", async () => {
    for (const bad of [1000.5, Number.NaN]) {
      const manifest = TransferManifest.fromJSON(allCases[0]!.transferManifest);
      manifest.edges[0]!.amount = { amount: { $case: "sats", sats: bad } };

      await expect(hashTransferManifest(manifest)).rejects.toThrow(
        "amount must be a safe integer",
      );
    }
  });

  it("rejects negative amounts before they wrap into valid wire values", async () => {
    // uint32 encoding would turn bps -5 into 4294967291 — a plausible value
    // the Rust core would happily sign.
    const manifest = TransferManifest.fromJSON(allCases[0]!.transferManifest);
    manifest.edges[0]!.amount = { amount: { $case: "bps", bps: -5 } };

    await expect(hashTransferManifest(manifest)).rejects.toThrow(
      "amount must be non-negative",
    );
  });
});
