import { expect } from "@jest/globals";
import { bytesToHex } from "@noble/hashes/utils";

import { TreeNode } from "../../proto/spark.js";
import { getTxFromRawTxHex, getTxId } from "../../utils/bitcoin.js";
import { Network } from "../../utils/network.js";
import {
  constructUnilateralExitFeeBumpPackages,
  hash160,
} from "../../utils/unilateral-exit.js";
import type { FeeBumpTxChain, Utxo } from "../../utils/unilateral-exit.js";
import { signPsbtWithExternalKey } from "./signing.js";
import { SparkWalletTestingIntegration } from "./spark-testing-wallet.js";
import { BitcoinFaucet } from "./test-faucet.js";

export interface ExternalFundingWallet {
  utxo: Utxo;
  privateKey: Uint8Array;
  publicKey: Uint8Array;
}

export const didTxSucceed = (response: unknown): boolean =>
  (response as { package_msg?: string })?.package_msg === "success";

/**
 * Faucet a fresh P2WPKH wallet, fund it with `amount` sats, and return the
 * resulting Utxo plus its private key for signing fee-bump PSBTs later.
 */
export async function makeExternalFundingUtxo(
  faucet: BitcoinFaucet,
  amount: bigint,
): Promise<ExternalFundingWallet> {
  const {
    address,
    key: privateKey,
    pubKey: publicKey,
  } = await faucet.getNewExternalWPKHWallet();

  const fundingTx = await faucet.sendToAddress(address, amount);
  await faucet.mineBlocksAndWaitForMiningToComplete(6);

  const p2wpkhScript = new Uint8Array([0x00, 0x14, ...hash160(publicKey)]);
  const vout = BitcoinFaucet.findOutputIndex(fundingTx, p2wpkhScript);

  return {
    utxo: {
      txid: fundingTx.id,
      vout,
      value: amount,
      script: bytesToHex(p2wpkhScript),
      publicKey: bytesToHex(publicKey),
    },
    privateKey,
    publicKey,
  };
}

/**
 * Drive the full unilateral-exit broadcast pipeline for `leaves` against a
 * running local Spark stack:
 *   - faucet a fresh external funding UTXO,
 *   - construct a sparkClient against the wallet's coordinator,
 *   - call constructUnilateralExitFeeBumpPackages,
 *   - sign every fee-bump PSBT with the funding wallet's key,
 *   - submitpackage each (parent, fee_bump) pair, mining 2000 blocks between
 *     to mature the relative timelocks,
 *   - assert each broadcast succeeded and the final refund tx confirms.
 *
 * Returns the constructed packages and the funding wallet so callers can
 * run additional assertions if needed.
 */
export async function broadcastUnilateralExit(
  faucet: BitcoinFaucet,
  wallet: SparkWalletTestingIntegration,
  leaves: TreeNode[],
  fundingAmount: bigint,
): Promise<{
  constructedTx: FeeBumpTxChain[];
  funding: ExternalFundingWallet;
}> {
  expect(leaves.length).toBeGreaterThan(0);

  const funding = await makeExternalFundingUtxo(faucet, fundingAmount);

  const sparkClient = await wallet
    .getConnectionManager()
    .createSparkClient(wallet.getConfigService().getCoordinatorAddress());

  const encodedLeaves = leaves.map((leaf) =>
    bytesToHex(TreeNode.encode(leaf).finish()),
  );

  const constructedTx = await constructUnilateralExitFeeBumpPackages(
    encodedLeaves,
    [funding.utxo],
    { satPerVbyte: 5 },
    Network.LOCAL,
    sparkClient,
  );

  expect(constructedTx).toHaveLength(leaves.length);

  for (const leafChain of constructedTx) {
    expect(leafChain.txPackages.length).toBeGreaterThan(0);

    for (const pkg of leafChain.txPackages) {
      const feeBumpPsbtSigned = await signPsbtWithExternalKey(
        pkg.feeBumpPsbt!,
        bytesToHex(funding.privateKey),
      );
      const res = await faucet.submitPackage([pkg.tx, feeBumpPsbtSigned]);
      expect(didTxSucceed(res)).toBe(true);
      await faucet.mineBlocksAndWaitForMiningToComplete(2000);
    }

    const finalTxHex =
      leafChain.txPackages[leafChain.txPackages.length - 1]?.tx;
    expect(finalTxHex).toBeDefined();
    const finalTxId = getTxId(getTxFromRawTxHex(finalTxHex!));
    const finalTxInfo = await faucet.getRawTransaction(finalTxId);
    expect(finalTxInfo.confirmations).toBeGreaterThan(0);
  }

  return { constructedTx, funding };
}
