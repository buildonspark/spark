import { SparkError } from "../errors/base.js";
import { getClientEnv } from "../constants.js";

describe("SparkError", () => {
  it("stringifies BigInt values in context", () => {
    const err = new SparkError("Test BigInt", { big: 123n });

    expect(err.message).toBe(
      `Test BigInt [big: 123, clientEnv: ${getClientEnv()}]`,
    );
  });

  it("stringifies primitive context values and strips punctuation", () => {
    const err = new SparkError("Test primitives", {
      num: 1,
      str: "abc",
      bool: true,
    });

    expect(err.message).toBe(
      `Test primitives [num: 1, str: abc, bool: true, clientEnv: ${getClientEnv()}]`,
    );
  });

  it("includes original error message when provided", () => {
    const original = new Error("something broke");
    const err = new SparkError("Wrapper error.", { error: original });

    expect(err.message).toBe(
      `Wrapper error: something broke [clientEnv: ${getClientEnv()}]`,
    );
  });

  it("stringifies Uint8Array values", () => {
    const bytes = new Uint8Array([1, 2, 3]);
    const err = new SparkError("Uint8Array test", { bytes });

    expect(err.message).toBe(
      `Uint8Array test [bytes: Uint8Array(0x010203), clientEnv: ${getClientEnv()}]`,
    );
  });

  it("merges context via update", () => {
    const err = new SparkError("Needs update.", { foo: "bar" });

    err.update({ context: { traceId: "abc123" } });

    expect(err.message).toBe(
      `Needs update [foo: bar, clientEnv: ${getClientEnv()}, traceId: abc123]`,
    );
  });
});
