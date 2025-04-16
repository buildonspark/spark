import { describe, expect, it } from "@jest/globals";
import { hexToBytes } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { equalBytes, sha256 } from "@scure/btc-signer/utils";
import { WalletConfigService } from "../../services/config.js";
import { ConnectionManager } from "../../services/connection.js";
import { CoopExitService } from "../../services/coop-exit.js";
import { LeafKeyTweak, TransferService } from "../../services/transfer.js";
import { ConfigOptions } from "../../services/wallet-config.js";
import {
  getP2TRAddressFromPublicKey,
  getP2TRScriptFromPublicKey,
  getTxId,
  getTxIdNoReverse,
} from "../../utils/bitcoin.js";
import { getNetwork, Network } from "../../utils/network.js";
import { createNewTree } from "../test-util.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";
describe("coop exit", () => {
  it("test coop exit", async () => {
    const faucet = new BitcoinFaucet();

    const faucetCoin = await faucet.fund();

    const amountSats = 100_000n;

    const options: ConfigOptions = {
      network: "LOCAL",
    };

    // Setup user with leaves
    const { wallet: userWallet } = await SparkWalletTesting.initialize({
      options,
    });

    const configService = new WalletConfigService(
      options,
      userWallet.getSigner(),
    );
    const connectionManager = new ConnectionManager(configService);
    const coopExitService = new CoopExitService(
      configService,
      connectionManager,
    );

    const leafPubKey = await userWallet
      .getSigner()
      .generatePublicKey(sha256("leafPubKey"));
    const rootNode = await createNewTree(
      userWallet,
      leafPubKey,
      faucet,
      amountSats,
    );

    // Setup ssp
    const { wallet: sspWallet } = await SparkWalletTesting.initialize({
      options,
    });
    const sspPubkey = await sspWallet.getIdentityPublicKey();

    const sspConfigService = new WalletConfigService(
      options,
      sspWallet.getSigner(),
    );
    const sspConnectionManager = new ConnectionManager(sspConfigService);
    const sspTransferService = new TransferService(
      sspConfigService,
      sspConnectionManager,
    );

    const sspIntermediateAddressScript = getP2TRScriptFromPublicKey(
      hexToBytes(sspPubkey),
      Network.LOCAL,
    );

    // Setup withdraw
    const withdrawPubKey = await userWallet.getSigner().generatePublicKey();
    const withdrawAddressScript = getP2TRScriptFromPublicKey(
      withdrawPubKey,
      Network.LOCAL,
    );

    const leafCount = 1;
    const dustAmountSats = 354;
    const intermediateAmountSats = (leafCount + 1) * dustAmountSats;

    const exitTx = new Transaction();
    exitTx.addInput(faucetCoin!.outpoint);
    exitTx.addOutput({
      script: withdrawAddressScript,
      amount: amountSats,
    });
    exitTx.addOutput({
      script: sspIntermediateAddressScript,
      amount: BigInt(intermediateAmountSats),
    });

    const exitTxId = getTxId(exitTx);
    const intermediateOutPoint: TransactionInput = {
      txid: hexToBytes(exitTxId),
      index: 1,
    };

    let connectorP2trAddrs: string[] = [];
    for (let i = 0; i < leafCount + 1; i++) {
      const connectorPubKey = await userWallet.getSigner().generatePublicKey();
      const connectorP2trAddr = getP2TRAddressFromPublicKey(
        connectorPubKey,
        Network.LOCAL,
      );
      connectorP2trAddrs.push(connectorP2trAddr);
    }
    const feeBumpAddr = connectorP2trAddrs[connectorP2trAddrs.length - 1];
    connectorP2trAddrs = connectorP2trAddrs.slice(0, -1);

    const connectorTx = new Transaction();
    connectorTx.addInput(intermediateOutPoint);
    for (const addr of [...connectorP2trAddrs, feeBumpAddr]) {
      connectorTx.addOutput({
        script: OutScript.encode(
          Address(getNetwork(Network.LOCAL)).decode(addr!),
        ),
        amount: BigInt(intermediateAmountSats / connectorP2trAddrs.length),
      });
    }

    const connectorOutputs: TransactionInput[] = [];
    for (let i = 0; i < connectorTx.outputsLength - 1; i++) {
      connectorOutputs.push({
        txid: hexToBytes(getTxId(connectorTx)),
        index: i,
      });
    }

    const newLeafPubKey = await userWallet.getSigner().generatePublicKey();

    const transferNode = {
      leaf: rootNode,
      signingPubKey: leafPubKey,
      newSigningPubKey: newLeafPubKey,
    };

    const senderTransfer = await coopExitService.getConnectorRefundSignatures({
      leaves: [transferNode],
      exitTxId: hexToBytes(getTxIdNoReverse(exitTx)),
      connectorOutputs,
      receiverPubKey: hexToBytes(sspPubkey),
    });

    const pendingTransfer = await sspWallet.queryPendingTransfers();

    expect(pendingTransfer.transfers.length).toBe(1);

    const receiverTransfer = pendingTransfer.transfers[0];
    expect(receiverTransfer!.id).toBe(senderTransfer.transfer.id);

    const leafPubKeyMap = await sspWallet.verifyPendingTransfer(
      receiverTransfer!,
    );

    expect(leafPubKeyMap.size).toBe(1);
    expect(leafPubKeyMap.get(rootNode.id)).toBeDefined();
    expect(equalBytes(leafPubKeyMap.get(rootNode.id)!, newLeafPubKey)).toBe(
      true,
    );

    // Try to claim leaf before exit tx confirms -> should fail
    const finalLeafPubKey = await sspWallet
      .getSigner()
      .generatePublicKey(sha256("finalLeafPubKey"));

    const leavesToClaim: LeafKeyTweak[] = [
      {
        leaf: receiverTransfer!.leaves[0]!.leaf!,
        signingPubKey: newLeafPubKey,
        newSigningPubKey: finalLeafPubKey,
      },
    ];

    let hasError = false;
    try {
      await sspTransferService.claimTransfer(receiverTransfer!, leavesToClaim);
    } catch (e) {
      hasError = true;
    }
    expect(hasError).toBe(true);

    // Sign an exit tx and broadcast
    const signedExitTx = await faucet.signFaucetCoin(
      exitTx,
      faucetCoin!.txout,
      faucetCoin!.key,
    );

    await faucet.broadcastTx(signedExitTx.hex);

    // Make sure the exit tx gets enough confirmations
    const randomKey = secp256k1.utils.randomPrivateKey();
    const randomPubKey = secp256k1.getPublicKey(randomKey);
    const randomAddress = getP2TRAddressFromPublicKey(
      randomPubKey,
      Network.LOCAL,
    );
    // Confirm extra buffer to scan more blocks than needed
    // So that we don't race the chain watcher in this test
    await faucet.generateToAddress(30, randomAddress);

    // Claim leaf
    await sspTransferService.claimTransfer(receiverTransfer!, leavesToClaim);
  }, 30000);
});
