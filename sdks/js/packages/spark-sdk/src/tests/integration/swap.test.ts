import { describe, expect, it } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import { hexToBytes } from "@noble/curves/utils";
import { uuidv7 } from "uuidv7";
import { ValidationError } from "../../errors/types.js";
import { KeyDerivationType } from "../../index.js";
import { WalletConfigService } from "../../services/config.js";
import { ConnectionManager } from "../../services/connection.js";
import { SigningService } from "../../services/signing.js";
import type { LeafKeyTweak } from "../../services/transfer.js";
import { TransferService } from "../../services/transfer.js";
import {
  applyAdaptorToSignature,
  generateAdaptorFromSignature,
  validateOutboundAdaptorSignature,
} from "../../utils/adaptor-signature.js";
import {
  computeTaprootKeyNoScript,
  getSigHashFromTx,
} from "../../utils/bitcoin.js";
import { walletTypes } from "../test-utils.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";

const testLocalOnly = process.env.GITHUB_ACTIONS ? it.skip : it;

describe.each(walletTypes)("swap", ({ name, Signer, createTree }) => {
  let aliceWallet: SparkWalletTesting;
  let aliceTransferService: TransferService;

  let bobWallet: SparkWalletTesting;
  let bobTransferService: TransferService;

  beforeAll(async () => {
    const { wallet: alice } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
      signer: new Signer(),
    });
    aliceWallet = alice;
    const aliceConfig = new WalletConfigService(
      {
        network: "LOCAL",
      },
      alice.getSigner(),
    );
    const aliceConnectionManager = new ConnectionManager(aliceConfig);
    const aliceSigningService = new SigningService(aliceConfig);
    aliceTransferService = new TransferService(
      aliceConfig,
      aliceConnectionManager,
      aliceSigningService,
    );

    const { wallet: bob } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
      signer: new Signer(),
    });
    bobWallet = bob;
    const bobConfig = new WalletConfigService(
      {
        network: "LOCAL",
      },
      bobWallet.getSigner(),
    );
    const bobConnectionManager = new ConnectionManager(bobConfig);
    const bobSigningService = new SigningService(bobConfig);
    bobTransferService = new TransferService(
      bobConfig,
      bobConnectionManager,
      bobSigningService,
    );
  });

  testLocalOnly(
    `${name} - test swap v1`,
    async () => {
      const faucet = BitcoinFaucet.getInstance();

      const aliceLeafId = uuidv7();
      const aliceRootNode = await createTree(aliceWallet!, aliceLeafId, faucet);

      const bobLeafId = uuidv7();
      const bobRootNode = await createTree(bobWallet!, bobLeafId, faucet);

      const aliceTransferNode: LeafKeyTweak = {
        leaf: aliceRootNode,
        keyDerivation: {
          type: KeyDerivationType.LEAF,
          path: aliceLeafId,
        },
        newKeyDerivation: {
          type: KeyDerivationType.RANDOM,
        },
      };
      const aliceLeavesToTransfer = [aliceTransferNode];

      const {
        transfer: aliceTransfer,
        signatureMap: aliceRefundSignatureMap,
        leafDataMap: aliceLeafDataMap,
        directSignatureMap: aliceDirectRefundSignatureMap,
        directFromCpfpSignatureMap: aliceDirectFromCpfpSignatureMap,
      } = await aliceTransferService!.startSwapSignRefund(
        aliceLeavesToTransfer,
        hexToBytes(await bobWallet.getIdentityPublicKey()),
        new Date(Date.now() + 10 * 60 * 1000),
      );

      expect(aliceRefundSignatureMap.size).toBe(1);
      const aliceSignature = aliceRefundSignatureMap.get(aliceRootNode.id);

      expect(aliceSignature).toBeDefined();
      expect(aliceDirectRefundSignatureMap.size).toBe(1);
      const aliceDirectSignature = aliceDirectRefundSignatureMap.get(
        aliceRootNode.id,
      );
      expect(aliceDirectSignature).toBeDefined();
      expect(aliceDirectFromCpfpSignatureMap.size).toBe(1);
      const aliceDirectFromCpfpSignature = aliceDirectFromCpfpSignatureMap.get(
        aliceRootNode.id,
      );
      expect(aliceDirectFromCpfpSignature).toBeDefined();
      expect(aliceLeafDataMap.size).toBe(1);
      const aliceLeafData = aliceLeafDataMap.get(aliceRootNode.id);
      expect(aliceLeafData).toBeDefined();

      const aliceRefundSighash = getSigHashFromTx(
        aliceLeafData!.refundTx!,
        0,
        aliceLeafData!.tx.getOutput(aliceLeafData!.vout),
      );

      let aliceDirectSighash: Uint8Array | undefined;
      let aliceDirectFromCpfpSighash: Uint8Array | undefined;
      if (aliceLeafData!.directRefundTx) {
        aliceDirectSighash = getSigHashFromTx(
          aliceLeafData!.directRefundTx,
          0,
          aliceLeafData!.directTx!.getOutput(aliceLeafData!.vout),
        );
      }
      if (aliceLeafData!.directFromCpfpRefundTx) {
        aliceDirectFromCpfpSighash = getSigHashFromTx(
          aliceLeafData!.directFromCpfpRefundTx,
          0,
          aliceLeafData!.tx.getOutput(aliceLeafData!.vout),
        );
      }

      const {
        adaptorSignature: cpfpAdaptorSignature,
        adaptorPrivateKey: cpfpAdaptorPrivateKey,
      } = generateAdaptorFromSignature(aliceSignature!);

      let directAdaptorPrivateKey: Uint8Array | undefined;
      let directFromCpfpAdaptorPrivateKey: Uint8Array | undefined;
      let directAdaptorSignature: Uint8Array | undefined;
      let directFromCpfpAdaptorSignature: Uint8Array | undefined;
      if (aliceDirectSignature && aliceDirectSignature.length > 0) {
        const { adaptorSignature, adaptorPrivateKey } =
          generateAdaptorFromSignature(aliceDirectSignature);
        directAdaptorPrivateKey = adaptorPrivateKey;
        directAdaptorSignature = adaptorSignature;
      }
      if (
        aliceDirectFromCpfpSignature &&
        aliceDirectFromCpfpSignature.length > 0
      ) {
        const { adaptorSignature, adaptorPrivateKey } =
          generateAdaptorFromSignature(aliceDirectFromCpfpSignature);
        directFromCpfpAdaptorPrivateKey = adaptorPrivateKey;
        directFromCpfpAdaptorSignature = adaptorSignature;
      }

      const cpfpAdaptorPubKey = secp256k1.getPublicKey(cpfpAdaptorPrivateKey);

      let directAdaptorPubKey: Uint8Array | undefined;
      let directFromCpfpAdaptorPubKey: Uint8Array | undefined;
      if (directAdaptorPrivateKey) {
        directAdaptorPubKey = secp256k1.getPublicKey(directAdaptorPrivateKey);
      }
      if (directFromCpfpAdaptorPrivateKey) {
        directFromCpfpAdaptorPubKey = secp256k1.getPublicKey(
          directFromCpfpAdaptorPrivateKey,
        );
      }

      const taprootKey = computeTaprootKeyNoScript(
        aliceRootNode.verifyingPublicKey.slice(1, 33),
      );

      validateOutboundAdaptorSignature(
        taprootKey.slice(1, 33),
        aliceRefundSighash,
        cpfpAdaptorSignature,
        cpfpAdaptorPubKey,
      );

      if (aliceDirectSighash) {
        validateOutboundAdaptorSignature(
          taprootKey.slice(1, 33),
          aliceDirectSighash,
          directAdaptorSignature!,
          directAdaptorPubKey!,
        );
      }

      if (aliceDirectFromCpfpSighash) {
        validateOutboundAdaptorSignature(
          taprootKey.slice(1, 33),
          aliceDirectFromCpfpSighash,
          directFromCpfpAdaptorSignature!,
          directFromCpfpAdaptorPubKey!,
        );
      }

      const receiverTransferNode: LeafKeyTweak = {
        leaf: bobRootNode,
        keyDerivation: {
          type: KeyDerivationType.LEAF,
          path: bobLeafId,
        },
        newKeyDerivation: {
          type: KeyDerivationType.RANDOM,
        },
      };
      const receiverLeavesToTransfer = [receiverTransferNode];

      const {
        transfer: bobTransfer,
        signatureMap: bobRefundSignatureMap,
        directSignatureMap: bobDirectRefundSignatureMap,
        directFromCpfpSignatureMap: bobDirectFromCpfpSignatureMap,
        leafDataMap: bobLeafDataMap,
        signingResults: bobSigningResults,
      } = await bobTransferService.counterSwapSignRefund(
        receiverLeavesToTransfer,
        hexToBytes(await aliceWallet.getIdentityPublicKey()),
        new Date(Date.now() + 10 * 60 * 1000),
        cpfpAdaptorPubKey,
        directAdaptorPubKey,
        directFromCpfpAdaptorPubKey,
      );

      const newReceiverRefundSignatureMap = new Map<string, Uint8Array>();
      const newReceiverDirectRefundSignatureMap = new Map<string, Uint8Array>();
      const newReceiverDirectFromCpfpRefundSignatureMap = new Map<
        string,
        Uint8Array
      >();

      for (const [nodeId, signature] of bobRefundSignatureMap.entries()) {
        const leafData = bobLeafDataMap.get(nodeId);
        if (!leafData?.refundTx) {
          throw new ValidationError("Refund transaction not found", {
            field: "refundTx",
            value: leafData,
          });
        }
        const sighash = getSigHashFromTx(
          leafData.refundTx,
          0,
          leafData.tx.getOutput(leafData.vout),
        );
        let verifyingPubkey: Uint8Array | undefined;
        for (const signingResult of bobSigningResults) {
          if (signingResult.leafId === nodeId) {
            verifyingPubkey = signingResult.verifyingKey;
          }
        }
        expect(verifyingPubkey).toBeDefined();
        const taprootKey = computeTaprootKeyNoScript(
          verifyingPubkey!.slice(1, 33),
        );
        const adaptorSig = applyAdaptorToSignature(
          taprootKey.slice(1, 33),
          sighash,
          signature,
          cpfpAdaptorPrivateKey,
        );
        newReceiverRefundSignatureMap.set(nodeId, adaptorSig);
      }

      for (const [nodeId, signature] of bobDirectRefundSignatureMap.entries()) {
        const leafData = bobLeafDataMap.get(nodeId);
        if (!leafData?.directRefundTx) {
          continue;
        }
        const sighash = getSigHashFromTx(
          leafData.directRefundTx,
          0,
          leafData.directTx!.getOutput(leafData.vout),
        );
        let verifyingPubkey: Uint8Array | undefined;
        for (const signingResult of bobSigningResults) {
          if (signingResult.leafId === nodeId) {
            verifyingPubkey = signingResult.verifyingKey;
          }
        }
        expect(verifyingPubkey).toBeDefined();
        const taprootKey = computeTaprootKeyNoScript(
          verifyingPubkey!.slice(1, 33),
        );
        const adaptorSig = applyAdaptorToSignature(
          taprootKey.slice(1, 33),
          sighash,
          signature,
          directAdaptorPrivateKey ?? new Uint8Array(),
        );
        newReceiverDirectRefundSignatureMap.set(nodeId, adaptorSig);
      }

      for (const [
        nodeId,
        signature,
      ] of bobDirectFromCpfpSignatureMap.entries()) {
        const leafData = bobLeafDataMap.get(nodeId);
        if (!leafData?.directFromCpfpRefundTx) {
          continue;
        }
        const sighash = getSigHashFromTx(
          leafData.directFromCpfpRefundTx,
          0,
          leafData.tx.getOutput(leafData.vout),
        );
        let verifyingPubkey: Uint8Array | undefined;
        for (const signingResult of bobSigningResults) {
          if (signingResult.leafId === nodeId) {
            verifyingPubkey = signingResult.verifyingKey;
          }
        }
        expect(verifyingPubkey).toBeDefined();
        const taprootKey = computeTaprootKeyNoScript(
          verifyingPubkey!.slice(1, 33),
        );
        const adaptorSig = applyAdaptorToSignature(
          taprootKey.slice(1, 33),
          sighash,
          signature,
          directFromCpfpAdaptorPrivateKey ?? new Uint8Array(),
        );
        newReceiverDirectFromCpfpRefundSignatureMap.set(nodeId, adaptorSig);
      }

      const senderTransferTweakKey =
        await aliceTransferService.deliverTransferPackage(
          aliceTransfer,
          aliceLeavesToTransfer,
          aliceRefundSignatureMap,
          aliceDirectRefundSignatureMap,
          aliceDirectFromCpfpSignatureMap,
        );

      const pendingTransfer = await bobTransferService.queryPendingTransfers();
      expect(pendingTransfer.transfers.length).toBe(1);
      const bobPendingTransfer = pendingTransfer.transfers[0];
      expect(bobPendingTransfer!.id).toBe(senderTransferTweakKey.id);

      const leafPrivKeyMap = await bobTransferService.verifyPendingTransfer(
        bobPendingTransfer!,
      );
      expect(leafPrivKeyMap.size).toBe(1);
      expect(leafPrivKeyMap.get(aliceRootNode.id)).toBeDefined();

      const claimingNodes: LeafKeyTweak[] = bobPendingTransfer!.leaves.map(
        (leaf) => ({
          leaf: leaf.leaf!,
          keyDerivation: {
            type: KeyDerivationType.ECIES,
            path: leaf.secretCipher!,
          },
          newKeyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leaf.leaf!.id,
          },
        }),
      );

      await bobTransferService.claimTransfer(
        bobPendingTransfer!,
        claimingNodes,
      );

      const senderTransferTweakKey2 =
        await bobTransferService.deliverTransferPackage(
          bobTransfer,
          receiverLeavesToTransfer,
          newReceiverRefundSignatureMap,
          newReceiverDirectRefundSignatureMap,
          newReceiverDirectFromCpfpRefundSignatureMap,
        );

      const pendingTransfer2 =
        await aliceTransferService.queryPendingTransfers();
      expect(pendingTransfer2.transfers.length).toBe(1);
      const alicePendingTransfer2 = pendingTransfer2.transfers[0];
      expect(alicePendingTransfer2!.id).toBe(senderTransferTweakKey2.id);

      const leafPrivKeyMap2 = await aliceTransferService.verifyPendingTransfer(
        alicePendingTransfer2!,
      );
      expect(leafPrivKeyMap2.size).toBe(1);

      const claimingNodes2: LeafKeyTweak[] = alicePendingTransfer2!.leaves.map(
        (leaf) => ({
          leaf: leaf.leaf!,
          keyDerivation: {
            type: KeyDerivationType.ECIES,
            path: leaf.secretCipher!,
          },
          newKeyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leaf.leaf!.id,
          },
        }),
      );

      await aliceTransferService.claimTransfer(
        alicePendingTransfer2!,
        claimingNodes2,
      );
    },
    30_000,
  );

  testLocalOnly(
    `${name} - test swap v2`,
    async () => {
      const faucet = BitcoinFaucet.getInstance();

      const aliceLeafId = uuidv7();
      const aliceRootNode = await createTree(aliceWallet!, aliceLeafId, faucet);

      const bobLeafId = uuidv7();
      const bobRootNode = await createTree(bobWallet!, bobLeafId, faucet);

      const aliceTransferNode: LeafKeyTweak = {
        leaf: aliceRootNode,
        keyDerivation: {
          type: KeyDerivationType.LEAF,
          path: aliceLeafId,
        },
        newKeyDerivation: {
          type: KeyDerivationType.RANDOM,
        },
      };
      const aliceLeavesToTransfer = [aliceTransferNode];

      const {
        transfer: aliceTransfer,
        signatureMap: aliceRefundSignatureMap,
        directSignatureMap: aliceDirectRefundSignatureMap,
        directFromCpfpSignatureMap: aliceDirectFromCpfpSignatureMap,
        leafDataMap: aliceLeafDataMap,
      } = await aliceTransferService.startSwapSignRefund(
        aliceLeavesToTransfer,
        hexToBytes(await bobWallet.getIdentityPublicKey()),
        new Date(Date.now() + 10 * 60 * 1000),
      );

      expect(aliceRefundSignatureMap.size).toBe(1);
      const aliceSignature = aliceRefundSignatureMap.get(aliceRootNode.id);
      expect(aliceSignature).toBeDefined();

      expect(aliceDirectRefundSignatureMap.size).toBe(1);
      const aliceDirectSignature = aliceDirectRefundSignatureMap.get(
        aliceRootNode.id,
      );
      expect(aliceDirectSignature).toBeDefined();

      expect(aliceDirectFromCpfpSignatureMap.size).toBe(1);
      const aliceDirectFromCpfpSignature = aliceDirectFromCpfpSignatureMap.get(
        aliceRootNode.id,
      );
      expect(aliceDirectFromCpfpSignature).toBeDefined();

      expect(aliceLeafDataMap.size).toBe(1);
      const aliceLeafData = aliceLeafDataMap.get(aliceRootNode.id);
      expect(aliceLeafData).toBeDefined();

      const aliceRefundSighash = getSigHashFromTx(
        aliceLeafData!.refundTx!,
        0,
        aliceLeafData!.tx.getOutput(aliceLeafData!.vout),
      );

      let aliceDirectSighash: Uint8Array | undefined;
      let aliceDirectFromCpfpSighash: Uint8Array | undefined;
      if (aliceLeafData!.directRefundTx) {
        aliceDirectSighash = getSigHashFromTx(
          aliceLeafData!.directRefundTx,
          0,
          aliceLeafData!.directTx!.getOutput(aliceLeafData!.vout),
        );
      }
      if (aliceLeafData!.directFromCpfpRefundTx) {
        aliceDirectFromCpfpSighash = getSigHashFromTx(
          aliceLeafData!.directFromCpfpRefundTx,
          0,
          aliceLeafData!.tx.getOutput(aliceLeafData!.vout),
        );
      }

      const {
        adaptorSignature: cpfpAdaptorSignature,
        adaptorPrivateKey: cpfpAdaptorPrivateKey,
      } = generateAdaptorFromSignature(aliceSignature!);

      let directAdaptorPrivateKey: Uint8Array | undefined;
      let directFromCpfpAdaptorPrivateKey: Uint8Array | undefined;
      let directAdaptorSignature: Uint8Array | undefined;
      let directFromCpfpAdaptorSignature: Uint8Array | undefined;
      if (aliceDirectSignature && aliceDirectSignature.length > 0) {
        const { adaptorSignature, adaptorPrivateKey } =
          generateAdaptorFromSignature(aliceDirectSignature);
        directAdaptorPrivateKey = adaptorPrivateKey;
        directAdaptorSignature = adaptorSignature;
      }
      if (
        aliceDirectFromCpfpSignature &&
        aliceDirectFromCpfpSignature.length > 0
      ) {
        const { adaptorSignature, adaptorPrivateKey } =
          generateAdaptorFromSignature(aliceDirectFromCpfpSignature);
        directFromCpfpAdaptorPrivateKey = adaptorPrivateKey;
        directFromCpfpAdaptorSignature = adaptorSignature;
      }

      const cpfpAdaptorPubKey = secp256k1.getPublicKey(cpfpAdaptorPrivateKey);

      let directAdaptorPubKey: Uint8Array | undefined;
      let directFromCpfpAdaptorPubKey: Uint8Array | undefined;
      if (directAdaptorPrivateKey) {
        directAdaptorPubKey = secp256k1.getPublicKey(directAdaptorPrivateKey);
      }
      if (directFromCpfpAdaptorPrivateKey) {
        directFromCpfpAdaptorPubKey = secp256k1.getPublicKey(
          directFromCpfpAdaptorPrivateKey,
        );
      }

      const taprootKey = computeTaprootKeyNoScript(
        aliceRootNode.verifyingPublicKey.slice(1, 33),
      );

      validateOutboundAdaptorSignature(
        taprootKey.slice(1, 33),
        aliceRefundSighash,
        cpfpAdaptorSignature,
        cpfpAdaptorPubKey,
      );

      if (aliceDirectSighash) {
        validateOutboundAdaptorSignature(
          taprootKey.slice(1, 33),
          aliceDirectSighash,
          directAdaptorSignature!,
          directAdaptorPubKey!,
        );
      }

      if (aliceDirectFromCpfpSighash) {
        validateOutboundAdaptorSignature(
          taprootKey.slice(1, 33),
          aliceDirectFromCpfpSighash,
          directFromCpfpAdaptorSignature!,
          directFromCpfpAdaptorPubKey!,
        );
      }

      const receiverTransferNode: LeafKeyTweak = {
        leaf: bobRootNode,
        keyDerivation: {
          type: KeyDerivationType.LEAF,
          path: bobLeafId,
        },
        newKeyDerivation: {
          type: KeyDerivationType.RANDOM,
        },
      };
      const receiverLeavesToTransfer = [receiverTransferNode];

      const {
        transfer: bobTransfer,
        signatureMap: bobRefundSignatureMap,
        directSignatureMap: bobDirectRefundSignatureMap,
        directFromCpfpSignatureMap: bobDirectFromCpfpSignatureMap,
        leafDataMap: bobLeafDataMap,
        signingResults: bobSigningResults,
      } = await bobTransferService.counterSwapSignRefund(
        receiverLeavesToTransfer,
        hexToBytes(await aliceWallet.getIdentityPublicKey()),
        new Date(Date.now() + 10 * 60 * 1000),
        cpfpAdaptorPubKey,
        directAdaptorPubKey,
        directFromCpfpAdaptorPubKey,
      );

      const newReceiverRefundSignatureMap = new Map<string, Uint8Array>();
      const newReceiverDirectRefundSignatureMap = new Map<string, Uint8Array>();
      const newReceiverDirectFromCpfpRefundSignatureMap = new Map<
        string,
        Uint8Array
      >();
      for (const [nodeId, signature] of bobRefundSignatureMap.entries()) {
        const leafData = bobLeafDataMap.get(nodeId);
        if (!leafData?.refundTx) {
          throw new ValidationError("Refund transaction not found", {
            field: "refundTx",
            value: leafData,
          });
        }
        const sighash = getSigHashFromTx(
          leafData.refundTx,
          0,
          leafData.tx.getOutput(leafData.vout),
        );
        let verifyingPubkey: Uint8Array | undefined;
        for (const signingResult of bobSigningResults) {
          if (signingResult.leafId === nodeId) {
            verifyingPubkey = signingResult.verifyingKey;
          }
        }
        expect(verifyingPubkey).toBeDefined();
        const taprootKey = computeTaprootKeyNoScript(
          verifyingPubkey!.slice(1, 33),
        );
        const adaptorSig = applyAdaptorToSignature(
          taprootKey.slice(1, 33),
          sighash,
          signature,
          cpfpAdaptorPrivateKey,
        );
        newReceiverRefundSignatureMap.set(nodeId, adaptorSig);
      }

      for (const [nodeId, signature] of bobDirectRefundSignatureMap.entries()) {
        const leafData = bobLeafDataMap.get(nodeId);
        if (!leafData?.directRefundTx) {
          continue;
        }
        const sighash = getSigHashFromTx(
          leafData.directRefundTx,
          0,
          leafData.directTx!.getOutput(leafData.vout),
        );
        let verifyingPubkey: Uint8Array | undefined;
        for (const signingResult of bobSigningResults) {
          if (signingResult.leafId === nodeId) {
            verifyingPubkey = signingResult.verifyingKey;
          }
        }
        expect(verifyingPubkey).toBeDefined();
        const taprootKey = computeTaprootKeyNoScript(
          verifyingPubkey!.slice(1, 33),
        );
        const adaptorSig = applyAdaptorToSignature(
          taprootKey.slice(1, 33),
          sighash,
          signature,
          directAdaptorPrivateKey ?? new Uint8Array(),
        );
        newReceiverDirectRefundSignatureMap.set(nodeId, adaptorSig);
      }

      for (const [
        nodeId,
        signature,
      ] of bobDirectFromCpfpSignatureMap.entries()) {
        const leafData = bobLeafDataMap.get(nodeId);
        if (!leafData?.directFromCpfpRefundTx) {
          continue;
        }
        const sighash = getSigHashFromTx(
          leafData.directFromCpfpRefundTx,
          0,
          leafData.tx.getOutput(leafData.vout),
        );
        let verifyingPubkey: Uint8Array | undefined;
        for (const signingResult of bobSigningResults) {
          if (signingResult.leafId === nodeId) {
            verifyingPubkey = signingResult.verifyingKey;
          }
        }
        expect(verifyingPubkey).toBeDefined();
        const taprootKey = computeTaprootKeyNoScript(
          verifyingPubkey!.slice(1, 33),
        );
        const adaptorSig = applyAdaptorToSignature(
          taprootKey.slice(1, 33),
          sighash,
          signature,
          directFromCpfpAdaptorPrivateKey ?? new Uint8Array(),
        );
        newReceiverDirectFromCpfpRefundSignatureMap.set(nodeId, adaptorSig);
      }

      const senderTransferTweakKey =
        await aliceTransferService.deliverTransferPackage(
          aliceTransfer,
          aliceLeavesToTransfer,
          aliceRefundSignatureMap,
          aliceDirectRefundSignatureMap,
          aliceDirectFromCpfpSignatureMap,
        );

      const pendingTransfer = await bobTransferService.queryPendingTransfers();
      expect(pendingTransfer.transfers.length).toBe(1);
      const bobPendingTransfer = pendingTransfer.transfers[0];
      expect(bobPendingTransfer!.id).toBe(senderTransferTweakKey.id);

      const leafPrivKeyMap = await bobTransferService.verifyPendingTransfer(
        bobPendingTransfer!,
      );
      expect(leafPrivKeyMap.size).toBe(1);

      const claimingNodes: LeafKeyTweak[] = bobPendingTransfer!.leaves.map(
        (leaf) => ({
          leaf: leaf.leaf!,
          keyDerivation: {
            type: KeyDerivationType.ECIES,
            path: leaf.secretCipher!,
          },
          newKeyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leaf.leaf!.id,
          },
        }),
      );

      await bobTransferService.claimTransfer(
        bobPendingTransfer!,
        claimingNodes,
      );

      await bobTransferService.deliverTransferPackage(
        bobTransfer,
        receiverLeavesToTransfer,
        newReceiverRefundSignatureMap,
        newReceiverDirectRefundSignatureMap,
        newReceiverDirectFromCpfpRefundSignatureMap,
      );

      const pendingTransfer1 =
        await aliceTransferService.queryPendingTransfers();
      expect(pendingTransfer1.transfers.length).toBe(1);
      const alicePendingTransfer = pendingTransfer1.transfers[0];
      expect(alicePendingTransfer!.id).toBe(bobTransfer.id);

      const leafPrivKeyMap1 = await aliceTransferService.verifyPendingTransfer(
        alicePendingTransfer!,
      );
      expect(leafPrivKeyMap1.size).toBe(1);

      const claimingNodes1: LeafKeyTweak[] = alicePendingTransfer!.leaves.map(
        (leaf) => ({
          leaf: leaf.leaf!,
          keyDerivation: {
            type: KeyDerivationType.ECIES,
            path: leaf.secretCipher!,
          },
          newKeyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leaf.leaf!.id,
          },
        }),
      );

      await aliceTransferService.claimTransfer(
        alicePendingTransfer!,
        claimingNodes1,
      );
    },
    30_000,
  );
});
