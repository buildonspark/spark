import { describe, expect, it, jest } from "@jest/globals";
import { bytesToHex, hexToBytes } from "@noble/curves/utils";
import { uuidv7 } from "uuidv7";
import { KeyDerivation, KeyDerivationType } from "../../index.js";
import { WalletConfigService } from "../../services/config.js";
import { ConnectionManagerNodeJS } from "../../services/connection/connection.node.js";
import { SigningService } from "../../services/signing.js";
import type { LeafKeyTweak } from "../../services/transfer.js";
import { TransferService } from "../../services/transfer.js";
import { ConfigOptions } from "../../services/wallet-config.js";
import {
  createNewTree,
  createNewTreeWithoutDirectTx,
  walletTypes,
} from "../test-utils.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";

describe.each(walletTypes)("Node compatibility tests", ({ name, Signer }) => {
  jest.setTimeout(30_000);

  it(`${name} - test transfers between old and new node types`, async () => {
    const faucet = BitcoinFaucet.getInstance();

    const options: ConfigOptions = {
      network: "LOCAL",
    };

    // Initialize wallets
    const { wallet: oldWallet } = await SparkWalletTesting.initialize({
      options,
      signer: new Signer(),
    });

    const { wallet: newWallet } = await SparkWalletTesting.initialize({
      options,
      signer: new Signer(),
    });

    // Setup services for both wallets
    const oldConfigService = new WalletConfigService(
      options,
      oldWallet.getSigner(),
    );
    const oldConnectionManager = new ConnectionManagerNodeJS(oldConfigService);
    const oldSigningService = new SigningService(oldConfigService);
    const oldTransferService = new TransferService(
      oldConfigService,
      oldConnectionManager,
      oldSigningService,
    );

    const newConfigService = new WalletConfigService(
      options,
      newWallet.getSigner(),
    );
    const newConnectionManager = new ConnectionManagerNodeJS(newConfigService);
    const newSigningService = new SigningService(newConfigService);
    const newTransferService = new TransferService(
      newConfigService,
      newConnectionManager,
      newSigningService,
    );

    // Create nodes for both wallets
    const oldLeafId = uuidv7();
    const oldRootNode = await createNewTreeWithoutDirectTx(
      oldWallet,
      oldLeafId,
      faucet,
      1000n,
    );

    const newLeafId = uuidv7();
    const newRootNode = await createNewTree(
      newWallet,
      newLeafId,
      faucet,
      1000n,
    );

    // Test 1: Transfer from old wallet to new wallet
    const newWalletPubkey = await newWallet.getIdentityPublicKey();
    const oldToNewDerivationPath: KeyDerivation = {
      type: KeyDerivationType.LEAF,
      path: uuidv7(),
    };

    const oldToNewTransferNode: LeafKeyTweak = {
      leaf: oldRootNode,
      keyDerivation: {
        type: KeyDerivationType.LEAF,
        path: oldLeafId,
      },
      newKeyDerivation: oldToNewDerivationPath,
    };

    // Send transfer from old wallet to new wallet
    const oldToNewTransfer = await oldTransferService.sendTransferWithKeyTweaks(
      [oldToNewTransferNode],
      hexToBytes(newWalletPubkey),
    );

    // Verify and claim transfer on new wallet side
    const newWalletPendingTransfer = await newWallet.queryPendingTransfers();
    expect(newWalletPendingTransfer.transfers.length).toBe(1);
    const receiverTransfer = newWalletPendingTransfer.transfers[0];
    expect(receiverTransfer!.id).toBe(oldToNewTransfer.id);

    const leafPrivKeyMap = await newWallet.verifyPendingTransfer(
      receiverTransfer!,
    );
    expect(leafPrivKeyMap.size).toBe(1);
    const leafPrivKeyMapBytes = leafPrivKeyMap.get(oldRootNode.id);
    expect(leafPrivKeyMapBytes).toBeDefined();
    expect(bytesToHex(leafPrivKeyMapBytes!)).toBe(
      bytesToHex(
        await oldWallet
          .getSigner()
          .getPublicKeyFromDerivation(oldToNewDerivationPath),
      ),
    );

    const oldToNewClaimingNodes: LeafKeyTweak[] = receiverTransfer!.leaves.map(
      (leaf) => ({
        leaf: leaf.leaf!,
        keyDerivation: {
          type: KeyDerivationType.ECIES,
          path: leaf.secretCipher,
        },
        newKeyDerivation: {
          type: KeyDerivationType.LEAF,
          path: leaf.leaf!.id,
        },
      }),
    );

    await newTransferService.claimTransfer(
      receiverTransfer!,
      oldToNewClaimingNodes,
    );

    // Verify new wallet received the funds
    const newWalletBalance = await newWallet.getBalance();
    expect(newWalletBalance.balance).toBe(2000n); // Initial 1000 + transferred 1000

    // Test 2: Transfer from new wallet to old wallet
    const oldWalletPubkey = await oldWallet.getIdentityPublicKey();
    const newToOldDerivationPath: KeyDerivation = {
      type: KeyDerivationType.LEAF,
      path: uuidv7(),
    };

    const newToOldTransferNode: LeafKeyTweak = {
      leaf: newRootNode,
      keyDerivation: {
        type: KeyDerivationType.LEAF,
        path: newLeafId,
      },
      newKeyDerivation: newToOldDerivationPath,
    };

    // Send transfer from new wallet to old wallet
    const newToOldTransfer = await newTransferService.sendTransferWithKeyTweaks(
      [newToOldTransferNode],
      hexToBytes(oldWalletPubkey),
    );

    // Verify and claim transfer on old wallet side
    const oldWalletPendingTransfer = await oldWallet.queryPendingTransfers();
    expect(oldWalletPendingTransfer.transfers.length).toBe(1);
    const oldReceiverTransfer = oldWalletPendingTransfer.transfers[0];
    expect(oldReceiverTransfer!.id).toBe(newToOldTransfer.id);

    const oldLeafPrivKeyMap = await oldWallet.verifyPendingTransfer(
      oldReceiverTransfer!,
    );
    expect(oldLeafPrivKeyMap.size).toBe(1);
    const oldLeafPrivKeyMapBytes = oldLeafPrivKeyMap.get(newRootNode.id);
    expect(oldLeafPrivKeyMapBytes).toBeDefined();
    expect(bytesToHex(oldLeafPrivKeyMapBytes!)).toBe(
      bytesToHex(
        await newWallet
          .getSigner()
          .getPublicKeyFromDerivation(newToOldDerivationPath),
      ),
    );

    const newToOldClaimingNodes: LeafKeyTweak[] =
      oldReceiverTransfer!.leaves.map((leaf) => ({
        leaf: leaf.leaf!,
        keyDerivation: {
          type: KeyDerivationType.ECIES,
          path: leaf.secretCipher,
        },
        newKeyDerivation: {
          type: KeyDerivationType.LEAF,
          path: leaf.leaf!.id,
        },
      }));

    await oldTransferService.claimTransfer(
      oldReceiverTransfer!,
      newToOldClaimingNodes,
    );

    // Verify old wallet received the funds
    const oldWalletBalance = await oldWallet.getBalance();
    expect(oldWalletBalance.balance).toBe(1000n); // Initial 1000 - sent 1000 + received 1000
  });
});
