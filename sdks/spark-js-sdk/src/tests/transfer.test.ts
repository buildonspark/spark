import { describe, expect, it, xit } from "@jest/globals";
import {
  bytesToHex,
  equalBytes,
  hexToBytes,
} from "@noble/curves/abstract/utils";
import { sha256 } from "@scure/btc-signer/utils";
import { ConnectionManager } from "../services/connection.js";
import { LeafKeyTweak, TransferService } from "../services/transfer.js";
import { SparkWallet } from "../spark-sdk.js";
import { Network } from "../utils/network.js";
import { createNewTree } from "./test-util.js";
import { BitcoinFaucet } from "./utils/test-faucet.js";

describe("Transfer", () => {
  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  testFn(
    "test transfer",
    async () => {
      const faucet = new BitcoinFaucet(
        "http://127.0.0.1:18443",
        "admin1",
        "123"
      );

      const senderWallet = new SparkWallet(Network.LOCAL);
      const senderMnemonic = await senderWallet.generateMnemonic();
      await senderWallet.createSparkWallet(senderMnemonic);

      const leafPubKey = await senderWallet.getSigner().generatePublicKey();
      const rootNode = await createNewTree(
        senderWallet,
        leafPubKey,
        faucet,
        1000n
      );

      const newLeafPubKey = await senderWallet.getSigner().generatePublicKey();

      const receiverWallet = new SparkWallet(Network.LOCAL);
      const receiverMnemonic = await receiverWallet.generateMnemonic();
      const receiverPubkey = await receiverWallet.createSparkWallet(
        receiverMnemonic
      );

      const transferNode = {
        leaf: rootNode,
        signingPubKey: leafPubKey,
        newSigningPubKey: newLeafPubKey,
      };

      const senderTransfer = await senderWallet._sendTransfer(
        [transferNode],
        hexToBytes(receiverPubkey),
        new Date(Date.now() + 10 * 60 * 1000)
      );

      const pendingTransfer = await receiverWallet.queryPendingTransfers();

      expect(pendingTransfer.transfers.length).toBe(1);

      const receiverTransfer = pendingTransfer.transfers[0];

      expect(receiverTransfer.id).toBe(senderTransfer.id);

      const leafPrivKeyMap = await receiverWallet.verifyPendingTransfer(
        receiverTransfer
      );

      expect(leafPrivKeyMap.size).toBe(1);

      const leafPrivKeyMapBytes = leafPrivKeyMap.get(rootNode.id);
      expect(leafPrivKeyMapBytes).toBeDefined();
      expect(bytesToHex(leafPrivKeyMapBytes!)).toBe(bytesToHex(newLeafPubKey));

      const finalLeafPubKey = await receiverWallet
        .getSigner()
        .generatePublicKey(sha256(rootNode.id));

      const claimingNode = {
        leaf: rootNode,
        signingPubKey: newLeafPubKey,
        newSigningPubKey: finalLeafPubKey,
      };

      await receiverWallet._claimTransfer(receiverTransfer, [claimingNode]);

      const nodes = await receiverWallet.getLeaves();
      await receiverWallet.setLeaves(nodes);

      const newReceiverWallet = new SparkWallet(Network.LOCAL);
      const newReceiverMnemonic = await newReceiverWallet.generateMnemonic();
      const newReceiverPubkey = await newReceiverWallet.createSparkWallet(
        newReceiverMnemonic
      );

      await receiverWallet.sendTransfer({
        amount: 1000,
        receiverPubKey: hexToBytes(newReceiverPubkey),
      });

      const newPendingTransfer =
        await newReceiverWallet.queryPendingTransfers();

      await newReceiverWallet.claimTransfer(newPendingTransfer.transfers[0]);
    },
    30000
  );

  testFn("test transfer with separate", async () => {
    const faucet = new BitcoinFaucet("http://127.0.0.1:18443", "admin1", "123");

    const senderWallet = new SparkWallet(Network.LOCAL);
    const senderMnemonic = await senderWallet.generateMnemonic();
    await senderWallet.createSparkWallet(senderMnemonic);

    const receiverWallet = new SparkWallet(Network.LOCAL);
    const receiverMnemonic = await receiverWallet.generateMnemonic();
    const receiverPubkey = await receiverWallet.createSparkWallet(
      receiverMnemonic
    );

    const leafPubKey = await senderWallet.getSigner().generatePublicKey();

    const rootNode = await createNewTree(
      senderWallet,
      leafPubKey,
      faucet,
      100_000n
    );

    const newLeafPubKey = await senderWallet.getSigner().generatePublicKey();

    const transferNode: LeafKeyTweak = {
      leaf: rootNode,
      signingPubKey: leafPubKey,
      newSigningPubKey: newLeafPubKey,
    };

    const leavesToTransfer = [transferNode];

    const senderTransfer = await senderWallet._sendTransfer(
      leavesToTransfer,
      hexToBytes(receiverPubkey),
      new Date(Date.now() + 10 * 60 * 1000)
    );

    // Receiver queries pending transfer
    const pendingTransfer = await receiverWallet.queryPendingTransfers();

    expect(pendingTransfer.transfers.length).toBe(1);

    const receiverTransfer = pendingTransfer.transfers[0];

    expect(receiverTransfer.id).toBe(senderTransfer.id);

    const leafPrivKeyMap = await receiverWallet.verifyPendingTransfer(
      receiverTransfer
    );

    expect(leafPrivKeyMap.size).toBe(1);

    const leafPrivKeyMapBytes = leafPrivKeyMap.get(rootNode.id);
    expect(leafPrivKeyMapBytes).toBeDefined();
    expect(equalBytes(leafPrivKeyMapBytes!, newLeafPubKey)).toBe(true);

    const finalLeafPubKey = await receiverWallet
      .getSigner()
      .generatePublicKey(sha256(rootNode.id));

    const claimingNode: LeafKeyTweak = {
      leaf: receiverTransfer.leaves[0].leaf!,
      signingPubKey: newLeafPubKey,
      newSigningPubKey: finalLeafPubKey,
    };

    const transferService = new TransferService(
      receiverWallet.getConfigService(),
      new ConnectionManager(receiverWallet.getConfigService())
    );

    await transferService.claimTransferTweakKeys(receiverTransfer, [
      claimingNode,
    ]);

    const newPendingTransfer = await receiverWallet.queryPendingTransfers();

    expect(newPendingTransfer.transfers.length).toBe(1);

    const newReceiverTransfer = newPendingTransfer.transfers[0];
    expect(newReceiverTransfer.id).toBe(receiverTransfer.id);

    const newLeafPubKeyMap = await receiverWallet.verifyPendingTransfer(
      newReceiverTransfer
    );

    expect(newLeafPubKeyMap.size).toBe(1);

    const newLeafPubKeyMapBytes = newLeafPubKeyMap.get(rootNode.id);
    expect(newLeafPubKeyMapBytes).toBeDefined();
    expect(bytesToHex(newLeafPubKeyMapBytes!)).toBe(bytesToHex(newLeafPubKey));

    await transferService.claimTransferSignRefunds(newReceiverTransfer, [
      claimingNode,
    ]);

    const newNewPendingTransfer = await receiverWallet.queryPendingTransfers();
    expect(newNewPendingTransfer.transfers.length).toBe(1);

    await receiverWallet._claimTransfer(newNewPendingTransfer.transfers[0], [
      claimingNode,
    ]);
  });

  testFn("cancel transfer", async () => {
    const faucet = new BitcoinFaucet("http://127.0.0.1:18443", "admin1", "123");

    const senderWallet = new SparkWallet(Network.LOCAL);
    const senderMnemonic = await senderWallet.generateMnemonic();
    await senderWallet.createSparkWallet(senderMnemonic);

    const receiverWallet = new SparkWallet(Network.LOCAL);
    const receiverMnemonic = await receiverWallet.generateMnemonic();
    const receiverPubkey = await receiverWallet.createSparkWallet(
      receiverMnemonic
    );

    const leafPubKey = await senderWallet.getSigner().generatePublicKey();
    const rootNode = await createNewTree(
      senderWallet,
      leafPubKey,
      faucet,
      100_000n
    );

    const newLeafPubKey = await senderWallet.getSigner().generatePublicKey();

    const transferNode: LeafKeyTweak = {
      leaf: rootNode,
      signingPubKey: leafPubKey,
      newSigningPubKey: newLeafPubKey,
    };

    const senderTransferService = new TransferService(
      senderWallet.getConfigService(),
      new ConnectionManager(senderWallet.getConfigService())
    );

    const senderTransfer = await senderTransferService.sendTransferSignRefund(
      [transferNode],
      hexToBytes(receiverPubkey),
      new Date(Date.now() + 10 * 60 * 1000)
    );

    await senderTransferService.cancelSendTransfer(senderTransfer.transfer);

    const newSenderTransfer = await senderWallet._sendTransfer(
      [transferNode],
      hexToBytes(receiverPubkey),
      new Date(Date.now() + 10 * 60 * 1000)
    );

    const pendingTransfer = await receiverWallet.queryPendingTransfers();
    expect(pendingTransfer.transfers.length).toBe(1);

    const receiverTransfer = pendingTransfer.transfers[0];
    expect(receiverTransfer.id).toBe(newSenderTransfer.id);

    const leafPubKeyMap = await receiverWallet.verifyPendingTransfer(
      receiverTransfer
    );

    expect(leafPubKeyMap.size).toBe(1);

    const leafPubKeyMapBytes = leafPubKeyMap.get(rootNode.id);
    expect(leafPubKeyMapBytes).toBeDefined();
    expect(equalBytes(leafPubKeyMapBytes!, newLeafPubKey)).toBe(true);

    const finalLeafPubKey = await receiverWallet
      .getSigner()
      .generatePublicKey(sha256(rootNode.id));

    const claimingNode: LeafKeyTweak = {
      leaf: receiverTransfer.leaves[0].leaf!,
      signingPubKey: newLeafPubKey,
      newSigningPubKey: finalLeafPubKey,
    };

    await receiverWallet._claimTransfer(receiverTransfer, [claimingNode]);
  });

  xit("test transfer in wallet", async () => {
    const faucet = new BitcoinFaucet("http://127.0.0.1:18443", "admin1", "123");

    const senderWallet = new SparkWallet(Network.LOCAL);
    const senderMnemonic = await senderWallet.generateMnemonic();
    await senderWallet.createSparkWallet(senderMnemonic);

    const receiverWallet = new SparkWallet(Network.LOCAL);
    const receiverMnemonic = await receiverWallet.generateMnemonic();
    const receiverPubkey = await receiverWallet.createSparkWallet(
      receiverMnemonic
    );

    const leafPubKey = await senderWallet
      .getSigner()
      .generatePublicKey(sha256("1"));
    const rootNode = await createNewTree(
      senderWallet,
      leafPubKey,
      faucet,
      1000n
    );

    await senderWallet.setLeaves([rootNode]);

    await senderWallet.sendTransfer({
      amount: 1000,
      receiverPubKey: hexToBytes(receiverPubkey),
    });

    const pendingTransfer = await receiverWallet.queryPendingTransfers();

    await receiverWallet.claimTransfer(pendingTransfer.transfers[0]);
  });
});
