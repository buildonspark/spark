import { describe, expect, it } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import { bytesToHex, hexToBytes } from "@noble/curves/utils";
import fs from "fs";
import { TransferManifest } from "../proto/spark.js";
import { setSparkTokenPrimitivesOnce } from "../token-primitives-bindings/token-primitives-bindings.js";
import { SparkTokenPrimitives } from "../token-primitives-bindings/token-primitives-bindings.node.js";
import {
  signTransferManifest,
  verifyTransferManifestSignature,
} from "../utils/manifest-signing.js";

// Tests import modules directly, so register the real wasm-backed node binding
// (the SDK entry points normally do this).
setSparkTokenPrimitivesOnce(new SparkTokenPrimitives());

// A fixed TEST-ONLY identity key (never a real key). The manifest shapes are
// reused from the shared hash vectors; we assert signatures VERIFY, not that they
// equal fixed bytes — ECDSA is nonce-dependent, so verification is the contract.
const TEST_PRIVATE_KEY = hexToBytes(
  "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
);
const TEST_PUBLIC_KEY = hexToBytes(
  "0284bf7562262bbd6940085748f3be6afa52ae317155181ece31b66351ccffa4b0",
);

type HashTestCase = { name: string; transferManifest: unknown };
type HashDataset = { testCases?: HashTestCase[] };

describe("TransferManifest sender signature", () => {
  const url = new URL(
    "../../../../../../spark/testdata/transfer_manifest_hash_cases.json",
    import.meta.url,
  );

  let jsonData: HashDataset | null = null;
  try {
    jsonData = JSON.parse(fs.readFileSync(url, "utf8")) as HashDataset;
  } catch (err: unknown) {
    // Absence of the spark/testdata tree is a legitimate skip (mirrored
    // checkouts); a missing fixture in an existing tree is a regressed path.
    if ((err as NodeJS.ErrnoException).code !== "ENOENT") {
      throw err;
    }
    if (fs.existsSync(new URL(".", url))) {
      throw new Error(
        `manifest hash fixture directory exists but the fixture is missing: ${url.pathname}`,
      );
    }
  }

  if (!jsonData) {
    it("skips when the manifest hash dataset is absent", () => {
      expect(true).toBe(true);
    });
    return;
  }

  const cases = jsonData.testCases ?? [];
  const firstCase = cases[0];
  if (!firstCase) {
    throw new Error("manifest hash fixture exists but has no test cases");
  }

  const signWithFixedKey = (manifest: TransferManifest) =>
    signTransferManifest(manifest, {
      signMessageWithIdentityKey: (message: Uint8Array) =>
        Promise.resolve(
          secp256k1.sign(message, TEST_PRIVATE_KEY).toDERRawBytes(),
        ),
    });

  for (const tc of cases) {
    it(`signs and verifies ${tc.name}`, async () => {
      const manifest = TransferManifest.fromJSON(tc.transferManifest);
      const signature = await signWithFixedKey(manifest);
      expect(
        await verifyTransferManifestSignature(
          manifest,
          signature,
          TEST_PUBLIC_KEY,
        ),
      ).toBe(true);
    });
  }

  it("rejects a tampered manifest", async () => {
    const manifest = TransferManifest.fromJSON(firstCase.transferManifest);
    const signature = await signWithFixedKey(manifest);
    const tampered = TransferManifest.fromJSON(firstCase.transferManifest);
    tampered.transferId = "0197f9a0-9999-7000-8000-00000000dead";
    expect(
      await verifyTransferManifestSignature(
        tampered,
        signature,
        TEST_PUBLIC_KEY,
      ),
    ).toBe(false);
  });

  it("round-trips with a fresh key and rejects garbage signature bytes", async () => {
    const manifest = TransferManifest.fromJSON(firstCase.transferManifest);
    const freshPriv = secp256k1.utils.randomPrivateKey();
    const freshPub = secp256k1.getPublicKey(freshPriv, true);

    const signature = await signTransferManifest(manifest, {
      signMessageWithIdentityKey: (message: Uint8Array) =>
        Promise.resolve(secp256k1.sign(message, freshPriv).toDERRawBytes()),
    });
    expect(
      await verifyTransferManifestSignature(manifest, signature, freshPub),
    ).toBe(true);
    // wrong key
    expect(
      await verifyTransferManifestSignature(
        manifest,
        signature,
        TEST_PUBLIC_KEY,
      ),
    ).toBe(false);
    // unparseable signature bytes must verify false, never throw
    expect(
      await verifyTransferManifestSignature(
        manifest,
        new Uint8Array([1, 2, 3]),
        freshPub,
      ),
    ).toBe(false);
    // ...but a malformed identity KEY is a caller bug and must throw, not
    // masquerade as an invalid signature.
    await expect(
      verifyTransferManifestSignature(manifest, signature, new Uint8Array(33)),
    ).rejects.toThrow();
  });

  it("rejects high-S (malleated) DER signatures like the SO's canonical check", async () => {
    const manifest = TransferManifest.fromJSON(firstCase.transferManifest);
    const sig = secp256k1.Signature.fromDER(await signWithFixedKey(manifest));
    const highS = new secp256k1.Signature(
      sig.r,
      secp256k1.CURVE.n - sig.s,
      sig.recovery,
    );
    expect(highS.hasHighS()).toBe(true);
    expect(
      await verifyTransferManifestSignature(
        manifest,
        highS.toDERRawBytes(),
        TEST_PUBLIC_KEY,
      ),
    ).toBe(false);
  });

  it("rejects compact-encoded signatures (the SO's verifier is strict DER)", async () => {
    const manifest = TransferManifest.fromJSON(firstCase.transferManifest);
    const compact = secp256k1.Signature.fromDER(
      await signWithFixedKey(manifest),
    ).toCompactRawBytes();
    expect(
      await verifyTransferManifestSignature(manifest, compact, TEST_PUBLIC_KEY),
    ).toBe(false);
  });

  it("pins the production signer's digest-in/DER-out convention", async () => {
    // The other tests inject an inline signer; this one drives the REAL
    // DefaultSparkSigner so drift in signMessageWithIdentityKey (compact
    // default, payload prehash, backend swap) fails here instead of shipping.
    const { DefaultSparkSigner } = await import("../signer/signer.js");
    const signer = new DefaultSparkSigner();
    const identityPubkeyHex = await signer.createSparkWalletFromSeed(
      "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
    );
    const manifest = TransferManifest.fromJSON(firstCase.transferManifest);

    const signature = await signTransferManifest(manifest, signer);

    // Canonical DER (byte-stable through a parse/re-serialize round trip) and
    // low-S — the shape the SO's strict verifier requires.
    const parsed = secp256k1.Signature.fromDER(signature);
    expect(bytesToHex(parsed.toDERRawBytes())).toBe(bytesToHex(signature));
    expect(parsed.hasHighS()).toBe(false);
    expect(
      await verifyTransferManifestSignature(
        manifest,
        signature,
        hexToBytes(identityPubkeyHex),
      ),
    ).toBe(true);
  });
});
