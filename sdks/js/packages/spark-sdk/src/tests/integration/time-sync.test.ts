import { describe, expect, it } from "@jest/globals";
import { SparkWalletTestingIntegration } from "../utils/spark-testing-wallet.js";

describe("Server time synchronization", () => {
  it("should be synced after making a gRPC call", async () => {
    const { wallet } = await SparkWalletTestingIntegration.initialize({
      options: { network: "LOCAL" },
    });

    const connectionManager = wallet.getConnectionManager();

    await wallet.getSparkAddress();

    expect(connectionManager.isTimeSynced()).toBe(true);

    await wallet.cleanupConnections();
  }, 30000);
});
