import { describe, expect, it, jest } from "@jest/globals";
import LeafManager from "../../services/leaf-manager.js";
import { SparkWalletTestingIntegration } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";

describe("LeafManager", () => {
  jest.setTimeout(30_000);

  let userWallet: SparkWalletTestingIntegration;

  afterAll(async () => {
    await userWallet?.cleanupConnections();
  });

  it("should get all leaves", async () => {
    const { wallet } = await SparkWalletTestingIntegration.initialize({
      options: { network: "LOCAL" },
    });
    userWallet = wallet;

    const faucet = BitcoinFaucet.getInstance();

    const depositAddress = await userWallet.getSingleUseDepositAddress();
    const depositTx = await faucet.sendToAddress(depositAddress, 1_000n);
    await faucet.mineBlocksAndWaitForMiningToComplete(3);
    const depositLeaf = await userWallet.claimDeposit(depositTx.id);

    expect(depositLeaf).toBeDefined();
    expect(depositLeaf.length).toBe(1);

    const leafManager = new LeafManager(
      userWallet.getConfigService(),
      userWallet.getSwapService(),
      userWallet.getTransferService(),
      userWallet.getConnectionManager(),
    );

    const leaves = await leafManager.getLeaves();
    expect(leaves.length).toBe(1);
    expect(leaves[0]!.id).toEqual(depositLeaf[0]?.id);
  });
});
