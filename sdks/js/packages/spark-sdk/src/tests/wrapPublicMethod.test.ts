import { describe, it, expect } from "@jest/globals";
import { SparkWalletTesting } from "./utils/spark-testing-wallet.js";
import { getTestWalletConfig } from "./test-utils.js";
import { BasicTracerProvider } from "@opentelemetry/sdk-trace-base";
import { trace, type Tracer } from "@opentelemetry/api";
import { ConfigOptions, SparkWallet } from "../index.node.js";
import { SparkSigner } from "../signer/signer.js";
import { SparkSDKError } from "../errors/base.js";

class TestableWallet extends SparkWalletTesting {
  private static pendingTracerOverride: Tracer | null | undefined;
  private readonly tracerOverride: Tracer | null | undefined;

  public async testThrowError(): Promise<void> {
    throw new Error("Something went wrong");
  }

  public constructor(options?: ConfigOptions, signer?: SparkSigner) {
    super(options, signer);
    this.tracerOverride = TestableWallet.pendingTracerOverride;
    TestableWallet.pendingTracerOverride = undefined;
  }

  public static setNextTracerOverride(
    tracerValue: Tracer | null | undefined,
  ): void {
    TestableWallet.pendingTracerOverride = tracerValue;
  }

  protected override getTracer(): Tracer {
    if (TestableWallet.pendingTracerOverride !== undefined) {
      return TestableWallet.pendingTracerOverride as unknown as Tracer;
    }
    if (this.tracerOverride !== undefined) {
      return this.tracerOverride as unknown as Tracer;
    }
    return super.getTracer() as Tracer;
  }
}

const provider = new BasicTracerProvider();
trace.setGlobalTracerProvider(provider);
const tracer = trace.getTracer("test-tracer");

const TEST_IDENTITY_SEED = Uint8Array.from(
  { length: 32 },
  (_, index) => index + 1,
);

async function prepareWallet(wallet: TestableWallet) {
  await wallet.getSigner().createSparkWalletFromSeed(TEST_IDENTITY_SEED);
  return wallet;
}

async function makeTestWalletWithoutTracer() {
  const config = getTestWalletConfig();
  TestableWallet.setNextTracerOverride(null);
  const wallet = new TestableWallet(config, undefined);
  return await prepareWallet(wallet);
}

async function makeTestWalletWithTracer() {
  const config = getTestWalletConfig();
  TestableWallet.setNextTracerOverride(tracer);
  const wallet = new TestableWallet(config, undefined);
  return await prepareWallet(wallet);
}

function wrapTestMethod(wallet: TestableWallet) {
  wallet["wrapPublicMethod"]("testThrowError" as unknown as keyof SparkWallet);
}

describe("wrapPublicMethod", () => {
  it("wraps errors without tracer and adds idPubKey", async () => {
    const wallet = await makeTestWalletWithoutTracer();
    wrapTestMethod(wallet);
    const expectedId = await wallet.getIdentityPublicKey();

    try {
      await wallet.testThrowError();
      throw new Error("Expected error was not thrown");
    } catch (err) {
      expect(err).toBeInstanceOf(SparkSDKError);
      const message = (err as SparkSDKError).message;
      expect(message).toContain("Something went wrong");
      expect(message).toContain(`idPubKey: ${expectedId}`);
      expect(message).toContain("clientEnv:");
      expect(message).not.toContain("traceId:");
    }
  });

  it("wraps errors with tracer and adds traceId", async () => {
    const wallet = await makeTestWalletWithTracer();
    wrapTestMethod(wallet);

    try {
      await wallet.testThrowError();
      throw new Error("Expected error was not thrown");
    } catch (err) {
      expect(err).toBeInstanceOf(SparkSDKError);
      const message = (err as SparkSDKError).message;
      expect(message).toContain("Something went wrong");
      expect(message).toMatch(/traceId: [a-f0-9]{32}/i);
      expect(message).toContain("clientEnv:");
    }
  });

  it("does not duplicate metadata when error is rehandled", async () => {
    const wallet = await makeTestWalletWithoutTracer();
    const baseError = new SparkSDKError("duplicate test");

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
