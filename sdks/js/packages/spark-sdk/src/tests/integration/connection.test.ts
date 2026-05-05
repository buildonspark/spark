import { describe, expect, it, jest } from "@jest/globals";
import { ConnectionManagerNodeJS } from "../../services/connection/connection.node.js";
import {
  SparkWalletTesting,
  SparkWalletTestingIntegration,
} from "../utils/spark-testing-wallet.js";

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

describe("ConnectionManager auth retry refcount", () => {
  it("re-auth after token expiry recovers cleanly", async () => {
    const { wallet } = await SparkWalletTestingIntegration.initialize({
      options: { network: "LOCAL" },
    });

    // Prove the channel works
    const balance1 = await wallet.getBalance();
    expect(balance1).toBeDefined();

    // Clear the auth token cache to force a full re-auth on the next call.
    // This is the happy path — the connection is healthy, re-auth succeeds
    // on the first try, close() is called once, channel survives
    (ConnectionManagerNodeJS as any).authTokenCache.clear();

    const balance2 = await wallet.getBalance();
    expect(balance2).toBeDefined();

    await wallet.cleanupConnections();
  }, 30000);

  /**
   * Regression test for the auth retry refcount bug.
   *
   * Spies on createSparkAuthnGrpcConnection to wrap the returned authn
   * client's get_challenge — making it throw connection errors on the
   * first 3 attempts before calling through to the real implementation.
   *
   * On the buggy code: close() is called on each failed retry, draining
   * the shared channel refcount. After auth eventually succeeds, the
   * channel is already destroyed and getBalance fails permanently.
   *
   * On the fixed code: close() is called once in the finally block,
   * the refcount stays correct, and getBalance succeeds.
   */
  it("channel survives auth retries with injected connection errors", async () => {
    const { wallet } = await SparkWalletTestingIntegration.initialize({
      options: { network: "LOCAL" },
    });
    const connMgr = wallet.getConnectionManager();

    // Prove the channel works
    const balance1 = await wallet.getBalance();
    expect(balance1).toBeDefined();

    // Spy on createSparkAuthnGrpcConnection to inject failures into
    // get_challenge while keeping the real auth flow otherwise intact.
    let getChallengeAttempts = 0;
    const authnSpy = jest.spyOn(
      connMgr as any,
      "createSparkAuthnGrpcConnection",
    );
    authnSpy.mockImplementation(async (...args: unknown[]) => {
      const address = args[0] as string;
      // Call the real implementation to get a real authn client
      authnSpy.mockRestore();
      const realClient = await (connMgr as any).createSparkAuthnGrpcConnection(
        address,
      );

      // Wrap get_challenge to fail the first 3 times
      const originalGetChallenge = realClient.get_challenge.bind(realClient);
      realClient.get_challenge = async (req: any) => {
        getChallengeAttempts++;
        if (getChallengeAttempts <= 3) {
          throw new Error("UNAVAILABLE: read ETIMEDOUT");
        }
        return originalGetChallenge(req);
      };
      return realClient;
    });

    // Clear the auth token cache to force re-auth
    (ConnectionManagerNodeJS as any).authTokenCache.clear();

    // This triggers re-auth. get_challenge fails 3 times, then succeeds.
    // On buggy code: each failure calls close(), refcount drains, channel dies.
    // On fixed code: finally block calls close() once, channel survives.
    const balance2 = await wallet.getBalance();
    expect(balance2).toBeDefined();
    expect(getChallengeAttempts).toBe(4); // 3 failures + 1 success

    await wallet.cleanupConnections();
  }, 30000);
});
