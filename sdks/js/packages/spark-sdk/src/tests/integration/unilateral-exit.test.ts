import { describe, expect, it } from "@jest/globals";
import { bytesToHex } from "@noble/hashes/utils";
import { Transaction } from "@scure/btc-signer";

import { SparkError } from "../../errors/index.js";
import { TreeNode } from "../../proto/spark.js";
import { getTxId } from "../../utils/bitcoin.js";
import { Network } from "../../utils/network.js";
import {
  constructUnilateralExitFeeBumpPackages,
  hash160,
} from "../../utils/unilateral-exit.js";
import { signPsbtWithExternalKey } from "../utils/signing.js";
import { SparkWalletTestingIntegration } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";
import { waitForClaim } from "../utils/utils.js";

const LOCAL_MEMPOOL_URL = "http://mempool.minikube.local/api";

const didTxSucceed = (response: any) => {
  return response.package_msg === "success";
};

describe("unilateral exit", () => {
  it("should unilateral exit", async () => {
    const faucet = BitcoinFaucet.getInstance();

    const { wallet: userWallet } =
      await SparkWalletTestingIntegration.initialize({
        options: {
          network: "LOCAL",
        },
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

    const leaf = leaves[0]!;

    const encodedLeaf = TreeNode.encode(leaf).finish();
    const hexString = bytesToHex(encodedLeaf);

    const {
      address: fundingWalletAddress,
      key: fundingWalletKey,
      pubKey: fundingWalletPubKey,
    } = await faucet.getNewExternalWPKHWallet();

    const fundingTx = await faucet.sendToAddress(fundingWalletAddress, 50_000n);

    await faucet.mineBlocksAndWaitForMiningToComplete(6);

    const pubKeyHash = hash160(fundingWalletPubKey);
    const p2wpkhScript = new Uint8Array([0x00, 0x14, ...pubKeyHash]);

    const utxos = [
      {
        txid: fundingTx.id,
        vout: 0,
        value: 50_000n,
        script: bytesToHex(p2wpkhScript),
        publicKey: bytesToHex(fundingWalletPubKey),
      },
    ];

    // Create a spark client to be used for signing fee bump transactions.
    const configService = userWallet.getConfigService();
    const connectionManager = userWallet.getConnectionManager();
    const sparkClient = await connectionManager.createSparkClient(
      configService.getCoordinatorAddress(),
    );

    const constructedTx = await constructUnilateralExitFeeBumpPackages(
      [hexString],
      utxos,
      { satPerVbyte: 5 },
      LOCAL_MEMPOOL_URL,
      sparkClient,
      Network.LOCAL,
    );

    const txPackages = constructedTx[0]?.txPackages;

    // Broadcast unilateral exit transactions in order
    for (let i = 0; i < txPackages!.length; i++) {
      const txPackage = txPackages![i];
      const feeBumpPsbtSigned = await signPsbtWithExternalKey(
        txPackage!.feeBumpPsbt!,
        bytesToHex(fundingWalletKey),
      );
      const res = await faucet.submitPackage([
        txPackage!.tx,
        feeBumpPsbtSigned,
      ]);

      expect(didTxSucceed(res)).toBe(true);
      // Mine 2000 blocks to expire time lock.
      await faucet.mineBlocksAndWaitForMiningToComplete(2000);
    }
    await connectionManager.closeConnections();
  }, 90000);

  it("watchtower should broadcast direct from cpfp txn", async () => {
    const faucet = BitcoinFaucet.getInstance();
    const { wallet: userWallet } =
      await SparkWalletTestingIntegration.initialize({
        options: {
          network: "LOCAL",
        },
      });
    const depositResp = await userWallet.getSingleUseDepositAddress();
    if (!depositResp) {
      throw new SparkError("Deposit address not found", {
        method: "getDepositAddress",
      });
    }
    const signedTx = await faucet.sendToAddress(depositResp, 100_000n);
    await faucet.mineBlocksAndWaitForMiningToComplete(6);
    await userWallet.claimDeposit(signedTx.id);
    await waitForClaim({ wallet: userWallet });
    const leaves = await userWallet.getLeaves();
    expect(leaves.length).toBe(1);
    const leaf = leaves[0]!;
    const encodedLeaf = TreeNode.encode(leaf).finish();
    const hexString = bytesToHex(encodedLeaf);
    const {
      address: fundingWalletAddress,
      key: fundingWalletKey,
      pubKey: fundingWalletPubKey,
    } = await faucet.getNewExternalWPKHWallet();
    const fundingTx = await faucet.sendToAddress(fundingWalletAddress, 50_000n);

    await faucet.mineBlocksAndWaitForMiningToComplete(6);
    const pubKeyHash = hash160(fundingWalletPubKey);
    const p2wpkhScript = new Uint8Array([0x00, 0x14, ...pubKeyHash]);
    const utxos = [
      {
        txid: fundingTx.id,
        vout: 0,
        value: 50_000n,
        script: bytesToHex(p2wpkhScript),
        publicKey: bytesToHex(fundingWalletPubKey),
      },
    ];

    // Create a spark client to be used for signing fee bump transactions.
    const configService = userWallet.getConfigService();
    const connectionManager = userWallet.getConnectionManager();
    const sparkClient = await connectionManager.createSparkClient(
      configService.getCoordinatorAddress(),
    );
    const constructedTx = await constructUnilateralExitFeeBumpPackages(
      [hexString],
      utxos,
      { satPerVbyte: 5 },
      LOCAL_MEMPOOL_URL,
      sparkClient,
      Network.LOCAL,
    );

    const txPackages = constructedTx[0]?.txPackages.slice(0, 1);
    const directFromCpfpRefundTx = Transaction.fromRaw(
      leaf.directFromCpfpRefundTx,
    );
    const directFromCpfpRefundTxId = getTxId(directFromCpfpRefundTx);

    // Get node tx package and construct fee bump package.
    const txPackage = txPackages![0];
    const feeBumpPsbtSigned = await signPsbtWithExternalKey(
      txPackage!.feeBumpPsbt!,
      bytesToHex(fundingWalletKey),
    );

    // Submit node tx and fee bump package.
    const res = await faucet.submitPackage([txPackage!.tx, feeBumpPsbtSigned]);
    expect(didTxSucceed(res)).toBe(true);

    // Confirm package (establishes confirmation height)
    await faucet.mineBlocksAndWaitForMiningToComplete(1);

    // Mine 2050 blocks to expire time lock for direct from cpfp refund txn.
    // (Current Height will be ConfHeight + 2050)
    await faucet.mineBlocksAndWaitForMiningToComplete(2050);

    await faucet.waitForMempoolEntry(directFromCpfpRefundTxId);

    // Mining another 5 blocks to confirm that direct from cpfp refund txn is confirmed.
    await faucet.mineBlocksAndWaitForMiningToComplete(5);
    const txInfo = await faucet.getRawTransaction(directFromCpfpRefundTxId);
    expect(txInfo.confirmations).toBeGreaterThan(0);
  }, 90000);
});
