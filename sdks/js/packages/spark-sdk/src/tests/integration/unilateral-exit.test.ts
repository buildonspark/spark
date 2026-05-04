import { describe, expect, it } from "@jest/globals";
import { bytesToHex } from "@noble/hashes/utils";
import { Transaction } from "@scure/btc-signer";

import { SparkError } from "../../errors/index.js";
import { getTxId } from "../../utils/bitcoin.js";
import { Network } from "../../utils/network.js";
import { constructUnilateralExitFeeBumpPackages } from "../../utils/unilateral-exit.js";
import { TreeNode } from "../../proto/spark.js";
import { signPsbtWithExternalKey } from "../utils/signing.js";
import { SparkWalletTestingIntegration } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";
import {
  broadcastUnilateralExit,
  didTxSucceed,
  makeExternalFundingUtxo,
} from "../utils/unilateral-exit-helpers.js";
import { waitForClaim } from "../utils/utils.js";

const claimSingleDeposit = async (
  faucet: BitcoinFaucet,
  amount: bigint,
): Promise<{ wallet: SparkWalletTestingIntegration; leaf: TreeNode }> => {
  const { wallet } = await SparkWalletTestingIntegration.initialize({
    options: { network: "LOCAL" },
  });

  const depositResp = await wallet.getSingleUseDepositAddress();
  if (!depositResp) {
    throw new SparkError("Deposit address not found", {
      method: "getDepositAddress",
    });
  }

  const signedTx = await faucet.sendToAddress(depositResp, amount);
  await faucet.mineBlocksAndWaitForMiningToComplete(6);
  await wallet.claimDeposit(signedTx.id);
  await waitForClaim({ wallet });

  const leaves = await wallet.getLeaves();
  expect(leaves.length).toBe(1);
  return { wallet, leaf: leaves[0]! };
};

describe("unilateral exit", () => {
  it("should unilateral exit", async () => {
    const faucet = BitcoinFaucet.getInstance();
    const { wallet, leaf } = await claimSingleDeposit(faucet, 100_000n);

    try {
      await broadcastUnilateralExit(faucet, wallet, [leaf], 50_000n);
    } finally {
      await wallet.getConnectionManager().closeConnections();
    }
  }, 90000);

  it("watchtower should broadcast direct from cpfp txn", async () => {
    const faucet = BitcoinFaucet.getInstance();
    const { wallet, leaf } = await claimSingleDeposit(faucet, 100_000n);

    const funding = await makeExternalFundingUtxo(faucet, 50_000n);

    const sparkClient = await wallet
      .getConnectionManager()
      .createSparkClient(wallet.getConfigService().getCoordinatorAddress());

    const constructedTx = await constructUnilateralExitFeeBumpPackages(
      [bytesToHex(TreeNode.encode(leaf).finish())],
      [funding.utxo],
      { satPerVbyte: 5 },
      Network.LOCAL,
      sparkClient,
    );

    const txPackage = constructedTx[0]?.txPackages[0];
    expect(txPackage).toBeDefined();

    const directFromCpfpRefundTx = Transaction.fromRaw(
      leaf.directFromCpfpRefundTx,
    );
    const directFromCpfpRefundTxId = getTxId(directFromCpfpRefundTx);

    const feeBumpPsbtSigned = await signPsbtWithExternalKey(
      txPackage!.feeBumpPsbt!,
      bytesToHex(funding.privateKey),
    );
    const res = await faucet.submitPackage([txPackage!.tx, feeBumpPsbtSigned]);
    expect(didTxSucceed(res)).toBe(true);

    // Confirm package (establishes confirmation height)
    await faucet.mineBlocksAndWaitForMiningToComplete(1);

    // Mine 2050 blocks to expire time lock for direct from cpfp refund txn.
    // (Current Height will be ConfHeight + 2050)
    await faucet.mineBlocksAndWaitForMiningToComplete(2050);

    await faucet.waitForMempoolEntry(directFromCpfpRefundTxId, 60_000);

    // Mine 5 more blocks to confirm the direct-from-cpfp refund txn.
    await faucet.mineBlocksAndWaitForMiningToComplete(5);
    const txInfo = await faucet.getRawTransaction(directFromCpfpRefundTxId);
    expect(txInfo.confirmations).toBeGreaterThan(0);
  }, 120_000);
});
