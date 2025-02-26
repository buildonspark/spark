//@ts-nocheck

import { describe, expect, it } from "@jest/globals";
import { equalBytes, hexToBytes } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { sha256 } from "@scure/btc-signer/utils";
import { WalletConfigService } from "../services/config.js";
import { ConnectionManager } from "../services/connection.js";
import { LeafKeyTweak, TransferService } from "../services/transfer.js";
import {
  applyAdaptorToSignature,
  generateAdaptorFromSignature,
} from "../utils/adaptor-signature.js";
import {
  computeTaprootKeyNoScript,
  getSigHashFromTx,
} from "../utils/bitcoin.js";
import { Network } from "../utils/network.js";
import { createNewTree } from "./test-util.js";
import { SparkWalletTesting } from "./utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "./utils/test-faucet.js";

describe("swap", () => {
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  testFn(
    "test swap",
    async () => {
      const faucet = new BitcoinFaucet(
        "http://127.0.0.1:18443",
        "admin1",
        "123"
      );
      // Initiate sender
      const senderWallet = new SparkWalletTesting(Network.LOCAL);
      await senderWallet.initWalletFromMnemonic();
      const senderPubkey = await senderWallet.getIdentityPublicKey();

      const senderConfig = new WalletConfigService(
        Network.LOCAL,
        senderWallet.getSigner()
      );
      const senderConnectionManager = new ConnectionManager(senderConfig);
      const senderTransferService = new TransferService(
        senderConfig,
        senderConnectionManager
      );

      // Initiate receiver
      const receiverWallet = new SparkWalletTesting(Network.LOCAL);
      await receiverWallet.initWalletFromMnemonic();
      const receiverPubkey = await receiverWallet.getIdentityPublicKey();

      const receiverConfig = new WalletConfigService(
        Network.LOCAL,
        receiverWallet.getSigner()
      );
      const receiverConnectionManager = new ConnectionManager(receiverConfig);
      const receiverTransferService = new TransferService(
        receiverConfig,
        receiverConnectionManager
      );

      const senderLeafPubKey = await senderWallet
        .getSigner()
        .generatePublicKey();
      const senderRootNode = await createNewTree(
        senderWallet,
        senderLeafPubKey,
        faucet
      );

      const receiverLeafPubKey = await receiverWallet
        .getSigner()
        .generatePublicKey();
      const receiverRootNode = await createNewTree(
        receiverWallet,
        receiverLeafPubKey,
        faucet
      );

      // Sender initiates transfer
      const senderNewLeafPubKey = await senderWallet
        .getSigner()
        .generatePublicKey(sha256("1"));
      const senderTransferNode: LeafKeyTweak = {
        leaf: senderRootNode,
        signingPubKey: senderLeafPubKey,
        newSigningPubKey: senderNewLeafPubKey,
      };
      const senderLeavesToTransfer = [senderTransferNode];

      // Get signature for refunds (normal flow)
      const {
        transfer: senderTransfer,
        signatureMap: senderRefundSignatureMap,
        leafDataMap: senderLeafDataMap,
      } = await senderTransferService.sendTransferSignRefund(
        senderLeavesToTransfer,
        hexToBytes(receiverPubkey),
        new Date(Date.now() + 10 * 60 * 1000)
      );

      expect(senderRefundSignatureMap.size).toBe(1);
      const senderSignature = senderRefundSignatureMap.get(senderRootNode.id);
      expect(senderSignature).toBeDefined();
      expect(senderLeafDataMap.size).toBe(1);

      const { adaptorPrivateKey, adaptorSignature } =
        generateAdaptorFromSignature(senderSignature!);
      const adaptorPubKey = secp256k1.getPublicKey(adaptorPrivateKey);

      const receiverNewLeafPubKey = await receiverWallet
        .getSigner()
        .generatePublicKey(sha256("1"));

      const receiverTransferNode: LeafKeyTweak = {
        leaf: receiverRootNode,
        signingPubKey: receiverLeafPubKey,
        newSigningPubKey: receiverNewLeafPubKey,
      };
      const receiverLeavesToTransfer = [receiverTransferNode];

      const {
        transfer: receiverTransfer,
        signatureMap: receiverRefundSignatureMap,
        leafDataMap: receiverLeafDataMap,
        signingResults: operatorSigningResults,
      } = await receiverTransferService.sendSwapSignRefund(
        receiverLeavesToTransfer,
        hexToBytes(senderPubkey),
        new Date(Date.now() + 10 * 60 * 1000),
        adaptorPubKey
      );

      const newReceiverRefundSignatureMap = new Map<string, Uint8Array>();
      for (const [nodeId, signature] of receiverRefundSignatureMap.entries()) {
        const leafData = receiverLeafDataMap.get(nodeId);
        if (!leafData?.refundTx) {
          throw new Error(`No refund tx for leaf ${nodeId}`);
        }
        const sighash = getSigHashFromTx(
          leafData.refundTx,
          0,
          leafData.tx.getOutput(leafData.vout)
        );
        let verifyingPubkey: Uint8Array | undefined;
        for (const signingResult of operatorSigningResults) {
          if (signingResult.leafId === nodeId) {
            verifyingPubkey = signingResult.verifyingKey;
          }
        }
        expect(verifyingPubkey).toBeDefined();
        const taprootKey = computeTaprootKeyNoScript(
          verifyingPubkey!.slice(1, 33)
        );
        const adaptorSig = applyAdaptorToSignature(
          taprootKey.slice(1, 33),
          sighash,
          signature,
          adaptorPrivateKey
        );
        newReceiverRefundSignatureMap.set(nodeId, adaptorSig);
      }
      const senderTransferTweakKey =
        await senderTransferService.sendTransferTweakKey(
          senderTransfer,
          senderLeavesToTransfer,
          senderRefundSignatureMap
        );

      const pendingTransfer =
        await receiverTransferService.queryPendingTransfers();
      expect(pendingTransfer.transfers.length).toBe(1);
      const receiverPendingTransfer = pendingTransfer.transfers[0];
      expect(receiverPendingTransfer.id).toBe(senderTransferTweakKey.id);

      const leafPrivKeyMap =
        await receiverTransferService.verifyPendingTransfer(
          receiverPendingTransfer
        );

      expect(leafPrivKeyMap.size).toBe(1);
      expect(leafPrivKeyMap.get(senderRootNode.id)).toBeDefined();
      const bytesEqual = equalBytes(
        leafPrivKeyMap.get(senderRootNode.id)!,
        senderNewLeafPubKey
      );
      expect(bytesEqual).toBe(true);
      expect(receiverPendingTransfer.leaves[0].leaf).toBeDefined();
      const finalLeafPubKey = await receiverWallet
        .getSigner()
        .generatePublicKey(sha256("2"));
      const claimingNode: LeafKeyTweak = {
        leaf: receiverPendingTransfer.leaves[0].leaf!,
        signingPubKey: senderNewLeafPubKey,
        newSigningPubKey: finalLeafPubKey,
      };
      const leavesToClaim = [claimingNode];
      await receiverTransferService.claimTransfer(
        receiverPendingTransfer,
        leavesToClaim
      );
      await receiverTransferService.sendTransferTweakKey(
        receiverTransfer,
        receiverLeavesToTransfer,
        newReceiverRefundSignatureMap
      );

      const sPendingTransfer =
        await senderTransferService.queryPendingTransfers();
      expect(sPendingTransfer.transfers.length).toBe(1);
      const senderPendingTransfer = sPendingTransfer.transfers[0];
      expect(senderPendingTransfer.id).toBe(receiverTransfer.id);

      const senderLeafPrivKeyMap =
        await senderTransferService.verifyPendingTransfer(
          senderPendingTransfer
        );
      expect(senderLeafPrivKeyMap.size).toBe(1);
      expect(senderLeafPrivKeyMap.get(receiverRootNode.id)).toBeDefined();
      const bytesEqual_1 = equalBytes(
        senderLeafPrivKeyMap.get(receiverRootNode.id)!,
        receiverNewLeafPubKey
      );
      expect(bytesEqual_1).toBe(true);
      expect(senderPendingTransfer.leaves[0].leaf).toBeDefined();

      const finalLeafPubKey_1 = await senderWallet
        .getSigner()
        .generatePublicKey(sha256("3"));
      const claimingNode_1: LeafKeyTweak = {
        leaf: senderPendingTransfer.leaves[0].leaf!,
        signingPubKey: receiverNewLeafPubKey,
        newSigningPubKey: finalLeafPubKey_1,
      };
      const leavesToClaim_1 = [claimingNode_1];
      await senderTransferService.claimTransfer(
        senderPendingTransfer,
        leavesToClaim_1
      );
    },
    30000
  );
});
