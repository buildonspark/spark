import { SparkSDKError } from "../errors/base.js";
import { clientEnv } from "../constants.js";

describe("SparkSDKError", () => {
  it("stringifies BigInt values in context", () => {
    const err = new SparkSDKError("Test BigInt", { big: 123n });

    expect(err.message).toBe(`Test BigInt [big: 123, clientEnv: ${clientEnv}]`);
  });

  it("stringifies primitive context values and strips punctuation", () => {
    const err = new SparkSDKError("Test primitives", {
      num: 1,
      str: "abc",
      bool: true,
    });

    expect(err.message).toBe(
      `Test primitives [num: 1, str: abc, bool: true, clientEnv: ${clientEnv}]`,
    );
  });

  it("includes original error message when provided", () => {
    const original = new Error("something broke");
    const err = new SparkSDKError("Wrapper error.", {}, original);

    expect(err.message).toBe(
      `Wrapper error: something broke [clientEnv: ${clientEnv}]`,
    );
  });

  it("stringifies Uint8Array values", () => {
    const bytes = new Uint8Array([1, 2, 3]);
    const err = new SparkSDKError("Uint8Array test", { bytes });

    expect(err.message).toBe(
      `Uint8Array test [bytes: Uint8Array(0x010203), clientEnv: ${clientEnv}]`,
    );
  });

  it("merges context via update", () => {
    const err = new SparkSDKError("Needs update.", { foo: "bar" });

    err.update({ context: { traceId: "abc123" } });

    expect(err.message).toBe(
      `Needs update [foo: bar, clientEnv: ${clientEnv}, traceId: abc123]`,
    );
  });
});
