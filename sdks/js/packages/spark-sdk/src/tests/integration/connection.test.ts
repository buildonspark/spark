import { describe, expect, it, jest } from "@jest/globals";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { ConnectionManagerNodeJS } from "../../services/connection/connection.node.js";

describe("ConnectionManager", () => {
  it("reuses channels across many wallets for the same operator address", async () => {
    const createSpy = jest.spyOn(
      ConnectionManagerNodeJS.prototype as any,
      "createChannelWithTLS",
    );

    const NUM_WALLETS = 6;

    const wallets = await Promise.all(
      Array.from({ length: NUM_WALLETS }, async () => {
        const { wallet } = await SparkWalletTesting.initialize({
          options: { network: "LOCAL" },
        });
        return wallet;
      }),
    );

    await Promise.all(wallets.map((w) => w.getSparkAddress()));

    expect(createSpy.mock.calls.length).toBeGreaterThan(0);
    const callsByAddress = new Map<string, number>();
    for (const [addr] of createSpy.mock.calls) {
      const key = String(addr);
      callsByAddress.set(key, (callsByAddress.get(key) ?? 0) + 1);
    }
    for (const [addr, count] of callsByAddress) {
      expect(count).toBeLessThanOrEqual(1);
    }

    await Promise.all(wallets.map((w) => w.cleanupConnections()));

    createSpy.mockRestore();
  }, 60000);
});
