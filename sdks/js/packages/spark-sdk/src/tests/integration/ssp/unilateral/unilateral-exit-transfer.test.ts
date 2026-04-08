import { describe, expect, it } from "@jest/globals";
import { bytesToHex } from "@noble/hashes/utils";

import { SparkError } from "../../../../errors/index.js";
import { TreeNode } from "../../../../proto/spark.js";
import { SparkWalletEvent } from "../../../../spark-wallet/types.js";
import { getTxFromRawTxHex, getTxId } from "../../../../utils/bitcoin.js";
import {
  constructUnilateralExitFeeBumpPackages,
  hash160,
} from "../../../../utils/unilateral-exit.js";
import { signPsbtWithExternalKey } from "../../../utils/signing.js";
import {
  SparkWalletTestingIntegration,
  SparkWalletTestingIntegrationWithStream,
} from "../../../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../../../utils/test-faucet.js";
import { retryUntilSuccess } from "../../../utils/utils.js";

const LOCAL_MEMPOOL_URL = "http://mempool.minikube.local/api";
const DEPOSIT_AMOUNT = 10_000n;
const TRANSFER_AMOUNT = 1_000;
const EXTERNAL_FUNDING_AMOUNT = 100_000n;

const didTxSucceed = (response: any) => {
  return response.package_msg === "success";
};

const closeWallets = async (...wallets: SparkWalletTestingIntegration[]) => {
  await Promise.allSettled(
    wallets.map((wallet) => wallet.getConnectionManager().closeConnections()),
  );
};

const waitForWalletBalance = async (
  wallet: SparkWalletTestingIntegration,
  expectedBalance: bigint,
) => {
  await retryUntilSuccess(
    async () => {
      const { balance } = await wallet.getBalance();
      expect(balance).toBe(expectedBalance);
      return balance;
    },
    { maxAttempts: 40, delayMs: 500 },
  );
};

const waitForWalletLeaves = async (
  wallet: SparkWalletTestingIntegration,
  expectedBalance: bigint,
) => {
  return await retryUntilSuccess(
    async () => {
      const { balance } = await wallet.getBalance();
      expect(balance).toBe(expectedBalance);

      const leaves = await wallet.getLeaves();
      expect(leaves.length).toBeGreaterThan(0);

      const leavesBalance = leaves.reduce(
        (sum, leaf) => sum + BigInt(leaf.value),
        0n,
      );
      expect(leavesBalance).toBe(expectedBalance);

      return leaves;
    },
    { maxAttempts: 40, delayMs: 500 },
  );
};

const initializeWalletWithConnectedStream = async () => {
  let resolveStreamConnected!: () => void;
  const streamConnectedPromise = new Promise<void>((resolve) => {
    resolveStreamConnected = resolve;
  });

  const { wallet } = await SparkWalletTestingIntegrationWithStream.initialize({
    options: {
      network: "LOCAL",
      events: {
        [SparkWalletEvent.StreamConnected]: () => {
          resolveStreamConnected();
        },
      },
    },
  });

  await streamConnectedPromise;
  return wallet;
};

const createClaimedWallet = async (faucet: BitcoinFaucet, amount: bigint) => {
  const wallet = await initializeWalletWithConnectedStream();
  const depositAddress = await wallet.getSingleUseDepositAddress();

  if (!depositAddress) {
    throw new SparkError("Deposit address not found");
  }

  const signedTx = await faucet.sendToAddress(depositAddress, amount);
  await faucet.mineBlocksAndWaitForMiningToComplete(6);
  await wallet.claimDeposit(signedTx.id);
  await waitForWalletLeaves(wallet, amount);

  return wallet;
};

const unilateralExitWallet = async (
  faucet: BitcoinFaucet,
  wallet: SparkWalletTestingIntegration,
  expectedBalance: bigint,
) => {
  const leaves = await waitForWalletLeaves(wallet, expectedBalance);
  expect(leaves.length).toBeGreaterThan(0);

  const leaf = [...leaves].sort(
    (left, right) =>
      Number(BigInt(right.value) - BigInt(left.value)) ||
      left.id.localeCompare(right.id),
  )[0]!;
  const encodedLeaf = bytesToHex(TreeNode.encode(leaf).finish());

  const {
    address: fundingWalletAddress,
    key: fundingWalletKey,
    pubKey: fundingWalletPubKey,
  } = await faucet.getNewExternalWPKHWallet();

  const fundingTx = await faucet.sendToAddress(
    fundingWalletAddress,
    EXTERNAL_FUNDING_AMOUNT,
  );
  await faucet.mineBlocksAndWaitForMiningToComplete(6);

  const pubKeyHash = hash160(fundingWalletPubKey);
  const p2wpkhScript = new Uint8Array([0x00, 0x14, ...pubKeyHash]);
  const fundingVout = BitcoinFaucet.findOutputIndex(fundingTx, p2wpkhScript);
  const utxos = [
    {
      txid: fundingTx.id,
      vout: fundingVout,
      value: EXTERNAL_FUNDING_AMOUNT,
      script: bytesToHex(p2wpkhScript),
      publicKey: bytesToHex(fundingWalletPubKey),
    },
  ];

  const configService = wallet.getConfigService();
  const connectionManager = wallet.getConnectionManager();
  const sparkClient = await connectionManager.createSparkClient(
    configService.getCoordinatorAddress(),
  );

  const constructedTx = await constructUnilateralExitFeeBumpPackages(
    [encodedLeaf],
    utxos,
    { satPerVbyte: 5 },
    LOCAL_MEMPOOL_URL,
    sparkClient,
    configService.getNetworkProto(),
  );

  expect(constructedTx).toHaveLength(1);
  const leafChain = constructedTx[0]!;
  expect(leafChain.txPackages.length).toBeGreaterThan(0);

  for (const txPackage of leafChain.txPackages) {
    const feeBumpPsbtSigned = await signPsbtWithExternalKey(
      txPackage!.feeBumpPsbt!,
      bytesToHex(fundingWalletKey),
    );
    const res = await faucet.submitPackage([txPackage!.tx, feeBumpPsbtSigned]);

    expect(didTxSucceed(res)).toBe(true);
    // Mine blocks to expire the relative timelock before broadcasting the next level
    await faucet.mineBlocksAndWaitForMiningToComplete(2000);
  }

  const finalRefundTx =
    leafChain.txPackages[leafChain.txPackages.length - 1]?.tx;
  expect(finalRefundTx).toBeDefined();

  const finalRefundTxId = getTxId(getTxFromRawTxHex(finalRefundTx!));
  const finalRefundTxInfo = await faucet.getRawTransaction(finalRefundTxId);
  expect(finalRefundTxInfo.confirmations).toBeGreaterThan(0);
};

describe("SSP unilateral exit1", () => {
  it("should unilateral exit a received spark transfer leaf", async () => {
    const faucet = BitcoinFaucet.getInstance();
    const senderWallet = await createClaimedWallet(faucet, DEPOSIT_AMOUNT);
    const receiverWallet = await initializeWalletWithConnectedStream();

    try {
      await senderWallet.transfer({
        amountSats: TRANSFER_AMOUNT,
        receiverSparkAddress: await receiverWallet.getSparkAddress(),
      });

      await waitForWalletBalance(receiverWallet, BigInt(TRANSFER_AMOUNT));
      await unilateralExitWallet(
        faucet,
        receiverWallet,
        BigInt(TRANSFER_AMOUNT),
      );
    } finally {
      await closeWallets(senderWallet, receiverWallet);
    }
  }, 300_000);
});
