import { promises as fs } from "fs";
import { bytesToHex } from "@noble/hashes/utils";
import { SparkProto } from "@buildonspark/spark-sdk/types";

// Helper functions for mnemonic persistence
export async function saveMnemonic(path: string, mnemonic: string) {
  try {
    await fs.writeFile(path, mnemonic, "utf8");
  } catch (error) {
    console.error("Failed to save mnemonic:", error);
  }
}

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
 * @param {SparkProto.Transfer} transfer - The transfer object from SDK
 * @returns {Object} Formatted transfer response
 */
export function formatTransferResponse(transfer: SparkProto.Transfer) {
  if (!transfer) return null;
  try {
    return {
      id: transfer.id,
      senderIdentityPublicKey: bytesToHex(transfer.senderIdentityPublicKey),
      receiverIdentityPublicKey: bytesToHex(transfer.receiverIdentityPublicKey),
      status:
        TRANSFER_STATUS_MAP[
          transfer.status as keyof typeof TRANSFER_STATUS_MAP
        ] ?? "UNKNOWN",
      amount: transfer.totalValue, // BigInt handled by middleware
      expiryTime: transfer.expiryTime
        ? new Date(transfer.expiryTime).toISOString()
        : null,
      leaves:
        transfer.leaves?.map((leaf: SparkProto.TransferLeaf) => ({
          leaf: {
            id: leaf.leaf?.id,
            treeId: leaf.leaf?.treeId,
            value: leaf.leaf?.value, // BigInt handled by middleware
            parentNodeId: leaf.leaf?.parentNodeId,
            nodeTx: leaf.leaf?.nodeTx
              ? bytesToHex(leaf.leaf?.nodeTx)
              : undefined,
            refundTx: leaf.leaf?.refundTx
              ? bytesToHex(leaf.leaf?.refundTx)
              : undefined,
            vout: Number(leaf.leaf?.vout),
            verifyingPublicKey: leaf.leaf?.verifyingPublicKey
              ? bytesToHex(leaf.leaf?.verifyingPublicKey)
              : undefined,
            ownerIdentityPublicKey: leaf.leaf?.ownerIdentityPublicKey
              ? bytesToHex(leaf.leaf?.ownerIdentityPublicKey)
              : undefined,
            signingKeyshare: {
              ownerIdentifiers:
                leaf.leaf?.signingKeyshare?.ownerIdentifiers ?? [],
              threshold: Number(leaf.leaf?.signingKeyshare?.threshold),
            },
            status: leaf.leaf?.status,
            network:
              NETWORK_MAP[leaf.leaf?.network as keyof typeof NETWORK_MAP] ??
              "UNKNOWN",
          },
          secretCipher: leaf.secretCipher
            ? bytesToHex(leaf.secretCipher)
            : undefined,
          signature: leaf.signature ? bytesToHex(leaf.signature) : undefined,
          intermediateRefundTx: leaf.intermediateRefundTx
            ? bytesToHex(leaf.intermediateRefundTx)
            : undefined,
        })) ?? [],
    };
  } catch (error) {
    console.error("Error formatting transfer:", error);
    throw new Error("Failed to format transfer response");
  }
}
