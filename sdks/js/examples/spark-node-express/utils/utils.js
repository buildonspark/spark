import { promises as fs } from "fs";
import { bytesToHex } from "@noble/hashes/utils";

// Helper functions for mnemonic persistence
export async function saveMnemonic(path, mnemonic) {
  try {
    await fs.writeFile(path, mnemonic, "utf8");
  } catch (error) {
    console.error("Failed to save mnemonic:", error);
  }
}

export async function loadMnemonic(path) {
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
 * @param {Object} transfer - The transfer object from SDK
 * @returns {Object} Formatted transfer response
 */
export function formatTransferResponse(transfer) {
  if (!transfer) return null;
  try {
    return {
      id: transfer.id,
      senderIdentityPublicKey: bytesToHex(transfer.senderIdentityPublicKey),
      receiverIdentityPublicKey: bytesToHex(transfer.receiverIdentityPublicKey),
      status: TRANSFER_STATUS_MAP[transfer.status] ?? "UNKNOWN",
      amount: transfer.totalValue, // BigInt handled by middleware
      expiryTime: transfer.expiryTime
        ? new Date(transfer.expiryTime).toISOString()
        : null,
      leaves:
        transfer.leaves?.map((leaf) => ({
          leaf: {
            id: leaf.leaf.id,
            treeId: leaf.leaf.treeId,
            value: leaf.leaf.value, // BigInt handled by middleware
            parentNodeId: leaf.leaf.parentNodeId,
            nodeTx: bytesToHex(leaf.leaf.nodeTx),
            refundTx: bytesToHex(leaf.leaf.refundTx),
            vout: Number(leaf.leaf.vout),
            verifyingPublicKey: bytesToHex(leaf.leaf.verifyingPublicKey),
            ownerIdentityPublicKey: bytesToHex(
              leaf.leaf.ownerIdentityPublicKey
            ),
            signingKeyshare: {
              ownerIdentifiers:
                leaf.leaf.signingKeyshare?.ownerIdentifiers ?? [],
              threshold: Number(leaf.leaf.signingKeyshare?.threshold),
            },
            status: leaf.leaf.status,
            network: NETWORK_MAP[leaf.leaf.network] ?? "UNKNOWN",
          },
          secretCipher: bytesToHex(leaf.secretCipher),
          signature: bytesToHex(leaf.signature),
          intermediateRefundTx: bytesToHex(leaf.intermediateRefundTx),
        })) ?? [],
    };
  } catch (error) {
    console.error("Error formatting transfer:", error);
    throw new Error("Failed to format transfer response");
  }
}
