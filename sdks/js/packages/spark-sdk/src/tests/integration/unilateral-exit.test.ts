import { describe, expect, it } from "@jest/globals";
import { bytesToHex } from "@noble/hashes/utils";

import { RPCError } from "../../errors/types.js";
import { Network } from "../../utils/network.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";
import { waitForClaim } from "../utils/utils.js";
import {
  constructUnilateralExitFeeBumpPackages,
  hash160,
} from "../../utils/unilateral-exit.js";
import { signPsbtWithExternalKey } from "../utils/signing.js";
import { TreeNode } from "../../proto/spark.js";
import { WalletConfigService } from "../../services/config.js";
import { ConnectionManagerNodeJS } from "../../services/connection/connection.node.js";

describe("unilateral exit", () => {
  it("should unilateral exit", async () => {
    const faucet = BitcoinFaucet.getInstance();

    const { wallet: userWallet } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });

    const depositResp = await userWallet.getSingleUseDepositAddress();

    if (!depositResp) {
      throw new RPCError("Deposit address not found", {
        method: "getDepositAddress",
      });
    }

    const signedTx = await faucet.sendToAddress(depositResp, 100_000n);

    await faucet.mineBlocks(6);

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
    } = await faucet.getNewExternalWallet();

    const fundingTx = await faucet.sendToAddress(fundingWalletAddress, 50_000n);

    await faucet.mineBlocks(6);

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
    const configService = new WalletConfigService(
      { network: "LOCAL" },
      userWallet.getSigner(),
    );
    const connectionManager = new ConnectionManagerNodeJS(configService);
    const sparkClient = await connectionManager.createSparkClient(
      configService.getCoordinatorAddress(),
    );

    const constructedTx = await constructUnilateralExitFeeBumpPackages(
      [hexString],
      utxos,
      { satPerVbyte: 5 },
      "http://mempool.minikube.local/api",
      sparkClient,
      Network.LOCAL,
    );

    const txPackages = constructedTx[0]?.txPackages;

    // Broadcast unilateral exit transactions in order
    txPackages?.forEach(async (txPackage) => {
      const startBlock = await faucet.getBlockCount();
      const feeBumpPsbtSigned = await signPsbtWithExternalKey(
        txPackage.feeBumpPsbt!,
        bytesToHex(fundingWalletKey),
      );
      await faucet.submitPackage([txPackage.tx, feeBumpPsbtSigned]);

      // Mine 1910 blocks to expire time lock.
      await faucet.mineBlocks(1910);

      // Since we do not depend on the chain watcher, we just need to wait for the blocks to be mined.
      await faucet.waitForBlocksMined({
        startBlock,
        expectedIncrease: 1910,
      });
    });

    await connectionManager.closeConnections();
  }, 90000);
});
