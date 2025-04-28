import { Transfer } from "@buildonspark/spark-sdk/proto/spark";
import {
  WalletTransfer,
  WalletTransferLeaf,
} from "@buildonspark/spark-sdk/types";
import { promises as fs } from "fs";
/**
 * Saves a mnemonic to a file
 * @param {string} path - The path to save the mnemonic
 * @param {string} mnemonic - The mnemonic to save
 */
export async function saveMnemonic(path: string, mnemonic: string) {
  try {
    await fs.writeFile(path, mnemonic, "utf8");
  } catch (error) {
    console.error("Failed to save mnemonic:", error);
  }
}

/**
 * Loads a mnemonic from a file
 * @param {string} path - The path to load the mnemonic from
 * @returns {string | null} The mnemonic
 */
export async function loadMnemonic(path: string) {
  try {
    const mnemonic = await fs.readFile(path, "utf8");
    return mnemonic.trim();
  } catch (error) {
    return null;
  }
}

const TRANSFER_STATUS_MAP = {
  0: "SENDER_INITIATED",
  1: "SENDER_KEY_TWEAK_PENDING",
  2: "SENDER_KEY_TWEAKED",
  3: "RECEIVER_KEY_TWEAKED",
  4: "RECEIVER_REFUND_SIGNED",
  5: "COMPLETED",
  6: "EXPIRED",
  [-1]: "UNRECOGNIZED",
};

const NETWORK_MAP = {
  0: "MAINNET",
  1: "REGTEST",
  2: "TESTNET",
  3: "SIGNET",
  [-1]: "UNRECOGNIZED",
};

/**
 * Formats a transfer object for API response
 * @param {Transfer} transfer - The transfer object from SDK
 * @returns {Object} Formatted transfer response
 */
export function formatTransferResponse(transfer: WalletTransfer) {
  if (!transfer) return null;
  try {
    return {
      id: transfer.id,
      senderIdentityPublicKey: transfer.senderIdentityPublicKey,
      receiverIdentityPublicKey: transfer.receiverIdentityPublicKey,
      status: transfer.status,
      amount: transfer.totalValue,
      expiryTime: transfer.expiryTime
        ? new Date(transfer.expiryTime).toISOString()
        : null,
      leaves:
        transfer.leaves?.map((leaf: WalletTransferLeaf) => ({
          leaf: {
            id: leaf.leaf?.id,
            treeId: leaf.leaf?.treeId,
            value: leaf.leaf?.value,
            parentNodeId: leaf.leaf?.parentNodeId,
            nodeTx: leaf.leaf?.nodeTx,
            refundTx: leaf.leaf?.refundTx,
            vout: Number(leaf.leaf?.vout),
            verifyingPublicKey: leaf.leaf?.verifyingPublicKey,
            ownerIdentityPublicKey: leaf.leaf?.ownerIdentityPublicKey,
            signingKeyshare: {
              ownerIdentifiers:
                leaf.leaf?.signingKeyshare?.ownerIdentifiers ?? [],
              threshold: Number(leaf.leaf?.signingKeyshare?.threshold),
            },
            status: leaf.leaf?.status,
            network: leaf.leaf?.network,
          },
          secretCipher: leaf.secretCipher,
          signature: leaf.signature,
          intermediateRefundTx: leaf.intermediateRefundTx,
        })) ?? [],
    };
  } catch (error) {
    console.error("Error formatting transfer:", error);
    throw new Error("Failed to format transfer response");
  }
}
