import { promises as fs } from "fs";
import { bytesToHex } from "@noble/hashes/utils";
import { SparkProto } from "@buildonspark/spark-sdk/types";
import { Lrc20Protos } from "@buildonspark/lrc20-sdk";

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

export function formatTokenTransactionResponse(
  transaction: Lrc20Protos.Transaction
) {
  if (!transaction) return null;
  try {
    if (transaction.transaction) {
      switch (transaction.transaction.$case) {
        case "onChain":
          return {
            type: "OnChain",
            details: formatOnChainTokenTransactionResponse(
              transaction.transaction.onChain
            ),
          };
        case "spark":
          return {
            type: "Spark",
            details: formatSparkTokenTransactionResponse(
              transaction.transaction.spark
            ),
          };
        default:
          throw new Error("Unknown transaction type");
      }
    }
  } catch (error) {
    console.error("Error formatting token transaction:", error);
    throw new Error("Failed to format token transaction response");
  }
}

export function formatOnChainTokenTransactionResponse(
  transaction: Lrc20Protos.OnChainTransaction
) {
  if (!transaction) return null;
  try {
    return {
      id: transaction.transactionHash,
      operationType: Lrc20Protos.OperationType[transaction.operationType],
      status: Lrc20Protos.OnChainTransactionStatus[transaction.status],
      rawTx: transaction.rawtx,
      inputs: transaction.inputs.map((input) => ({
        rawTx: input.rawTx,
        vout: input.vout,
        amountSats: input.amountSats,
        tokenPublicKey: input.tokenPublicKey,
        tokenAmount: input.tokenAmount,
      })),
      outputs: transaction.outputs.map((output) => ({
        rawTx: output.rawTx,
        vout: output.vout,
        amountSats: output.amountSats,
        tokenPublicKey: output.tokenPublicKey,
        tokenAmount: output.tokenAmount,
      })),
      broadcastedAt: transaction.broadcastedAt,
      confirmedAt: transaction.confirmedAt,
    };
  } catch (error) {
    console.error("Error formatting on-chain token transaction:", error);
    throw new Error("Failed to format on-chain token transaction response");
  }
}

export function formatSparkTokenTransactionResponse(
  transaction: Lrc20Protos.SparkTransaction
) {
  if (!transaction) return null;
  try {
    return {
      id: transaction.transactionHash,
      operationType: Lrc20Protos.OperationType[transaction.operationType],
      status: Lrc20Protos.SparkTransactionStatus[transaction.status],
      confirmedAt: transaction.confirmedAt,
      leavesToCreate: transaction.leavesToCreate.map((leaf) => ({
        id: leaf.id,
        tokenPublicKey: leaf.tokenPublicKey,
        ownerPublicKey: leaf.ownerPublicKey,
        revocationPublicKey: leaf.revocationPublicKey,
        tokenAmount: leaf.tokenAmount,
        createTxHash: leaf.createTxHash,
        createTxVoutIndex: leaf.createTxVoutIndex,
        spendTxHash: leaf.spendTxHash,
        spendTxVoutIndex: leaf.spendTxVoutIndex,
        isFrozen: leaf.isFrozen,
      })),
    };
  } catch (error) {
    console.error("Error formatting spark token transaction:", error);
    throw new Error("Failed to format spark token transaction response");
  }
}

const LAYER_MAP = {
  [Lrc20Protos.Layer.L1]: "L1",
  [Lrc20Protos.Layer.SPARK]: "SPARK",
  [Lrc20Protos.Layer.UNRECOGNIZED]: "UNRECOGNIZED",
};

/**
 * Formats a next cursor object for API response
 * @param {Lrc20Protos.ListAllTokenTransactionsCursor} nextCursor - The next cursor object from SDK
 * @returns {{
 *   lastTransactionHash: string,
 *   layer: string
 * }} Formatted next cursor response
 */
export function formatNextCursor(
  nextCursor: Lrc20Protos.ListAllTokenTransactionsCursor | undefined
) {
  if (!nextCursor) return null;
  try {
    return {
      lastTransactionHash: bytesToHex(nextCursor.lastTransactionHash),
      layer:
        nextCursor.layer in LAYER_MAP
          ? LAYER_MAP[nextCursor.layer as keyof typeof LAYER_MAP]
          : "UNRECOGNIZED",
    };
  } catch (error) {
    console.error("Error formatting next cursor:", error);
    throw new Error("Failed to format next cursor");
  }
}
