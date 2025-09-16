import { describe, it, expect } from "@jest/globals";
import { SparkWalletTesting } from "./utils/spark-testing-wallet.js";
import { getTestWalletConfig } from "./test-utils.js";
import { BasicTracerProvider } from "@opentelemetry/sdk-trace-base";
import { trace } from "@opentelemetry/api";
import { ConfigOptions } from "../index.node.js";
import { SparkSigner } from "../signer/signer.js";

class TestableWallet extends SparkWalletTesting {
  public wrapWithOtelSpanPublic = this.wrapWithOtelSpan;

  // Public constructor to allow direct instantiation in tests without init
  public constructor(options?: ConfigOptions, signer?: SparkSigner) {
    super(options, signer);
  }
}

// Set up a real tracer provider once for all tests
const provider = new BasicTracerProvider();
trace.setGlobalTracerProvider(provider);
const tracer = trace.getTracer("test-tracer");

function makeTestWalletWithTracer() {
  const config = getTestWalletConfig();
  const wallet = new TestableWallet(config, undefined);
  wallet["tracer"] = tracer;
  return wallet;
}

describe("wrapWithOtelSpan (integration, real otel)", () => {
  it("should append traceId to error messages for Error", async () => {
    const wallet = makeTestWalletWithTracer();
    wallet["_throwError"] = async function () {
      throw new Error("Something went wrong");
    };
    const wrapped = wallet.wrapWithOtelSpanPublic(
      "dummy-span",
      wallet["_throwError"].bind(wallet),
    );
    try {
      await wrapped();
      throw new Error("Expected error was not thrown");
    } catch (err: any) {
      expect(err).toBeInstanceOf(Error);
      expect(err.message).toContain("Something went wrong");
      const match = err.message.match(/[a-f0-9]{32}/i);
      expect(match).not.toBeNull();
    }
  });

  it("should add traceId property for non-Error objects", async () => {
    const wallet = makeTestWalletWithTracer();
    wallet["_throwObj"] = async function () {
      throw { foo: "bar" };
    };
    const wrapped = wallet.wrapWithOtelSpanPublic(
      "dummy-span",
      wallet["_throwObj"].bind(wallet),
    );
    try {
      await wrapped();
      throw new Error("Expected error was not thrown");
    } catch (err: any) {
      expect(typeof err).toBe("object");
      expect(typeof err.traceId).toBe("string");
      expect(err.traceId.length).toBe(32);
      expect(err.foo).toBe("bar");
    }
  });
});
