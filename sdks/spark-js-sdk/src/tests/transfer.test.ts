import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { sha256 } from "@scure/btc-signer/utils";
import { SparkWallet } from "../spark-sdk";
import { Network } from "../utils/network";
import { createNewTree } from "./test-util";

describe("Transfer", () => {
  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  testFn(
    "test transfer",
    async () => {
      const senderWallet = new SparkWallet(Network.REGTEST);
      const senderMnemonic = senderWallet.generateMnemonic();
      await senderWallet.createSparkWallet(senderMnemonic);

      const leafPubKey = senderWallet.getSigner().generatePublicKey();
      const rootNode = await createNewTree(senderWallet, leafPubKey);

      const newLeafPubKey = senderWallet
        .getSigner()
        .generatePublicKey(sha256("1"));

      const receiverWallet = new SparkWallet(Network.REGTEST);
      const receiverMnemonic = receiverWallet.generateMnemonic();
      const receiverPubkey = await receiverWallet.createSparkWallet(
        receiverMnemonic
      );

      const transferNode = {
        leaf: rootNode,
        signingPubKey: leafPubKey,
        newSigningPubKey: newLeafPubKey,
      };

      const senderTransfer = await senderWallet.sendTransfer(
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

      const finalLeafPubKey = receiverWallet
        .getSigner()
        .generatePublicKey(sha256("2"));

      const claimingNode = {
        leaf: rootNode,
        signingPubKey: newLeafPubKey,
        newSigningPubKey: finalLeafPubKey,
      };

      await receiverWallet.claimTransfer(receiverTransfer, [claimingNode]);
    },
    30000
  );
});
