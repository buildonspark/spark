import { describe, it, expect } from "@jest/globals";
import { SparkWalletTesting } from "./utils/spark-testing-wallet.js";
import { getTestWalletConfig } from "./test-utils.js";
import { SparkWallet } from "../index.node.js";
import { SparkError } from "../errors/base.js";

class TestableWallet extends SparkWalletTesting {
  public async testThrowError(): Promise<void> {
    throw new Error("Something went wrong");
  }
}

const TEST_IDENTITY_SEED = Uint8Array.from(
  { length: 32 },
  (_, index) => index + 1,
);

async function prepareWallet(wallet: TestableWallet) {
  await wallet.getSigner().createSparkWalletFromSeed(TEST_IDENTITY_SEED);
  return wallet;
}

async function makeTestWallet() {
  const config = getTestWalletConfig();
  const wallet = new TestableWallet(config, undefined);
  return await prepareWallet(wallet);
}

function wrapTestMethod(wallet: TestableWallet) {
  wallet["wrapPublicMethod"]("testThrowError" as unknown as keyof SparkWallet);
}

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
});
