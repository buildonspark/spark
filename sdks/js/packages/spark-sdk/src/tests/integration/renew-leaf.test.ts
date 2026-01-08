import { SparkError } from "../../errors/index.js";
import { ConfigOptions } from "../../services/wallet-config.js";
import { getCurrentTimelock, getTxFromRawTxBytes } from "../../utils/index.js";
import { SparkWalletTestingIntegrationWithStream } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";
import { waitForClaim } from "../utils/utils.js";

// Config options for wallet to not automatically swap leaves
const options: ConfigOptions = {
  network: "LOCAL",
  optimizationOptions: {
    auto: false,
    multiplicity: 0,
  },
};

const TRANSFER_AMOUNT = 100_000;

const checkLeafTimelockEquals = async (
  wallet: SparkWalletTestingIntegrationWithStream,
  expectedTimelock: number,
) => {
  const leaves = await wallet.getLeaves();
  expect(leaves.length).toBe(1);
  const leaf = leaves[0]!;
  const refundTx = leaf.refundTx;
  const tx = getTxFromRawTxBytes(refundTx);
  const seq = tx.getInput(0).sequence ?? 0;
  const timelock = getCurrentTimelock(seq);
  expect(timelock).toBe(expectedTimelock);
};

const selfTransfer = async (
  wallet: SparkWalletTestingIntegrationWithStream,
  amountSats: number,
) => {
  const address = await wallet.getSparkAddress();
  await wallet.transfer({
    amountSats,
    receiverSparkAddress: address,
  });
  await wallet.syncWalletForTesting();
};

describe("Test renew leaf", () => {
  it("Should decrement and renew leaf timelock", async () => {
    const faucet = BitcoinFaucet.getInstance();

    const { wallet: userWallet } =
      await SparkWalletTestingIntegrationWithStream.initialize({
        options,
      });

    const depositResp = await userWallet.getSingleUseDepositAddress();

    if (!depositResp) {
      throw new SparkError("Deposit address not found");
    }

    const signedTx = await faucet.sendToAddress(depositResp, 100_000n);
    await faucet.mineBlocksAndWaitForMiningToComplete(6);
    await userWallet.claimDeposit(signedTx.id);
    await waitForClaim({ wallet: userWallet });

    const leaves = await userWallet.getLeaves();
    expect(leaves.length).toBe(1);

    // Confirm timelock decrements down to 200
    for (let i = 0; i < 18; i++) {
      await selfTransfer(userWallet, TRANSFER_AMOUNT);
    }
    await checkLeafTimelockEquals(userWallet, 200);

    // Confirm timelock renews to 2000
    await selfTransfer(userWallet, TRANSFER_AMOUNT);
    await checkLeafTimelockEquals(userWallet, 2000);

    // Confirm timelock successfully decrements down to 1900
    await selfTransfer(userWallet, TRANSFER_AMOUNT);
    await checkLeafTimelockEquals(userWallet, 1900);
  }, 60000);
});
