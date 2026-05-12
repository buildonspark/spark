import { afterEach, describe, expect, it, jest } from "@jest/globals";
import { SparkWalletTesting } from "./utils/spark-testing-wallet.js";
import { getTestWalletConfig } from "./test-utils.js";
import { SparkError } from "../errors/base.js";
import { SparkWallet } from "../spark-wallet/spark-wallet.js";

class TestableWallet extends SparkWalletTesting {
  public async testThrowError(): Promise<void> {
    await Promise.resolve();
    throw new Error("Something went wrong");
  }
}

const TEST_IDENTITY_SEED = Uint8Array.from(
  { length: 32 },
  (_, index) => index + 1,
);
const walletsToCleanup = new Set<TestableWallet>();

async function prepareWallet(wallet: TestableWallet) {
  await wallet.getSigner().createSparkWalletFromSeed(TEST_IDENTITY_SEED);
  return wallet;
}

async function makeTestWallet() {
  const config = getTestWalletConfig();
  const wallet = new TestableWallet(config, undefined);
  const prepared = await prepareWallet(wallet);
  walletsToCleanup.add(prepared);
  return prepared;
}

function wrapTestMethod(wallet: TestableWallet) {
  wallet["wrapPublicMethod"]("testThrowError" as unknown as keyof SparkWallet);
}

afterEach(async () => {
  for (const wallet of walletsToCleanup) {
    await wallet.cleanup();
  }
  walletsToCleanup.clear();
  await new Promise((resolve) => setTimeout(resolve, 0));
});

describe("wrapPublicMethod", () => {
  it("wraps errors and adds idPubKey without a client traceId", async () => {
    const wallet = await makeTestWallet();
    wrapTestMethod(wallet);
    const expectedId = await wallet.getIdentityPublicKey();

    try {
      await wallet.testThrowError();
      throw new Error("Expected error was not thrown");
    } catch (err) {
      expect(err).toBeInstanceOf(SparkError);
      const message = (err as SparkError).message;
      expect(message).toContain("Something went wrong");
      expect(message).toContain(`idPubKey: ${expectedId}`);
      expect(message).toContain("clientEnv:");
      expect(message).not.toContain("traceId:");
    }
  });

  it("does not duplicate metadata when error is rehandled", async () => {
    const wallet = await makeTestWallet();
    const baseError = new SparkError("duplicate test");

    const first = await SparkWallet["handlePublicMethodError"](baseError, {
      wallet,
    });
    const second = await SparkWallet["handlePublicMethodError"](first, {
      wallet,
    });

    expect(first).toBe(second);
    expect(second.message).toBe(first.message);
  });

  it("reconfigures the wallet logger when method logging is enabled later", async () => {
    const wallet = await makeTestWallet();
    const logger = wallet["logger"];

    expect(logger.options.enabled).toBe(false);

    wallet.setMethodLoggingEnabled(true);

    expect(wallet.isMethodLoggingEnabled()).toBe(true);
    expect(wallet["logger"]).toBe(logger);
    expect(logger.options.enabled).toBe(true);
  });

  it("keeps cleanupConnections as an alias for cleanup", async () => {
    const wallet = await makeTestWallet();
    const cleanup = jest.spyOn(wallet, "cleanup").mockResolvedValue(undefined);

    await wallet.cleanupConnections();

    expect(cleanup).toHaveBeenCalledTimes(1);
    cleanup.mockRestore();
  });
});
