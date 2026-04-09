import { describe, it, expect, beforeEach } from "@jest/globals";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { SparkWallet } from "../../index.node.js";

const TEST_SEED_A =
  "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about";
const TEST_SEED_B = "zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo wrong";

describe("SparkWallet.getOrCreateWallet", () => {
  beforeEach(async () => {
    await SparkWallet.resetInstances();
  });

  it("returns the same instance for the same seed", async () => {
    const { wallet: w1 } = await SparkWalletTesting.getOrCreateWallet({
      mnemonicOrSeed: TEST_SEED_A,
      options: { network: "LOCAL" },
    });

    const { wallet: w2 } = await SparkWalletTesting.getOrCreateWallet({
      mnemonicOrSeed: TEST_SEED_A,
      options: { network: "LOCAL" },
    });

    expect(w1).toBe(w2);

    await w1.cleanupConnections();
  });

  it("returns different instances for different seeds", async () => {
    const { wallet: w1 } = await SparkWalletTesting.getOrCreateWallet({
      mnemonicOrSeed: TEST_SEED_A,
      options: { network: "LOCAL" },
    });

    const { wallet: w2 } = await SparkWalletTesting.getOrCreateWallet({
      mnemonicOrSeed: TEST_SEED_B,
      options: { network: "LOCAL" },
    });

    expect(w1).not.toBe(w2);

    await w1.cleanupConnections();
    await w2.cleanupConnections();
  });

  it("creates a new instance after cleanupConnections", async () => {
    const { wallet: w1 } = await SparkWalletTesting.getOrCreateWallet({
      mnemonicOrSeed: TEST_SEED_A,
      options: { network: "LOCAL" },
    });

    await w1.cleanupConnections();

    const { wallet: w2 } = await SparkWalletTesting.getOrCreateWallet({
      mnemonicOrSeed: TEST_SEED_A,
      options: { network: "LOCAL" },
    });

    expect(w1).not.toBe(w2);

    await w2.cleanupConnections();
  });

  it("forceReinit creates a new instance and cleans up the old one", async () => {
    const { wallet: w1 } = await SparkWalletTesting.getOrCreateWallet({
      mnemonicOrSeed: TEST_SEED_A,
      options: { network: "LOCAL" },
    });

    const { wallet: w2 } = await SparkWalletTesting.getOrCreateWallet({
      mnemonicOrSeed: TEST_SEED_A,
      options: { network: "LOCAL" },
      forceReinit: true,
    });

    expect(w1).not.toBe(w2);

    // w2 should be the registered instance now
    const { wallet: w3 } = await SparkWalletTesting.getOrCreateWallet({
      mnemonicOrSeed: TEST_SEED_A,
      options: { network: "LOCAL" },
    });

    expect(w3).toBe(w2);

    await w2.cleanupConnections();
  });

  it("concurrent calls return the same instance", async () => {
    const opts = {
      mnemonicOrSeed: TEST_SEED_A,
      options: { network: "LOCAL" as const },
    };

    const [result1, result2, result3] = await Promise.all([
      SparkWalletTesting.getOrCreateWallet(opts),
      SparkWalletTesting.getOrCreateWallet(opts),
      SparkWalletTesting.getOrCreateWallet(opts),
    ]);

    expect(result1.wallet).toBe(result2.wallet);
    expect(result2.wallet).toBe(result3.wallet);

    await result1.wallet.cleanupConnections();
  });

  it("initialize always creates a new instance", async () => {
    const { wallet: w1 } = await SparkWalletTesting.initialize({
      mnemonicOrSeed: TEST_SEED_A,
      options: { network: "LOCAL" },
    });

    const { wallet: w2 } = await SparkWalletTesting.initialize({
      mnemonicOrSeed: TEST_SEED_A,
      options: { network: "LOCAL" },
    });

    expect(w1).not.toBe(w2);

    await w1.cleanupConnections();
    await w2.cleanupConnections();
  });

  it("resetInstances clears the registry", async () => {
    const { wallet: w1 } = await SparkWalletTesting.getOrCreateWallet({
      mnemonicOrSeed: TEST_SEED_A,
      options: { network: "LOCAL" },
    });

    await SparkWallet.resetInstances();

    const { wallet: w2 } = await SparkWalletTesting.getOrCreateWallet({
      mnemonicOrSeed: TEST_SEED_A,
      options: { network: "LOCAL" },
    });

    expect(w1).not.toBe(w2);

    await w2.cleanupConnections();
  });
});
