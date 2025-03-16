import { ListAllTokenTransactionsResponse, TokenPubkeyInfo, Lrc20Protos } from "@buildonspark/lrc20-sdk";
import { GetTokenActivityResponse, TokenPubKeyInfoResponse, OperationType, OnChainTransactionStatus, SparkTransactionStatus, LayerType } from "../types.js";
import { bytesToHex, bytesToNumberBE } from "@noble/curves/abstract/utils";
import { mapOperationType, mapOnChainTransactionStatus, mapSparkTransactionStatus, mapLayer } from "./enum-mappers.js";

export function convertToTokenPubKeyInfoResponse(
  tokenPubkeyInfo: TokenPubkeyInfo
): TokenPubKeyInfoResponse {
  return {
    announcement: tokenPubkeyInfo.announcement ? {
      tokenPubkey: {
        pubkey: bytesToHex(tokenPubkeyInfo.announcement.tokenPubkey.pubkey)
      },
      name: tokenPubkeyInfo.announcement.name,
      symbol: tokenPubkeyInfo.announcement.symbol,
      decimal: tokenPubkeyInfo.announcement.decimal,
      maxSupply: tokenPubkeyInfo.announcement.maxSupply,
      isFreezable: tokenPubkeyInfo.announcement.isFreezable
    } : null,
    totalSupply: tokenPubkeyInfo.totalSupply
  };
}

export function convertTokenActivityToHexEncoded(
    rawTransactions: ListAllTokenTransactionsResponse
  ): GetTokenActivityResponse {
    const response: GetTokenActivityResponse = {
      transactions: rawTransactions.transactions.map((transaction) => {
        if (!transaction.transaction) {
          return { transaction: undefined };
        }

        if (transaction.transaction.$case === "onChain") {
          const onChain = transaction.transaction.onChain;
          return {
            transaction: {
              $case: "onChain",
              onChain: {
                operationType: mapOperationType(onChain.operationType),
                transactionHash: bytesToHex(onChain.transactionHash),
                rawtx: bytesToHex(onChain.rawtx),
                status: mapOnChainTransactionStatus(onChain.status),
                inputs: onChain.inputs.map(input => ({
                  rawTx: bytesToHex(input.rawTx),
                  vout: input.vout,
                  amountSats: input.amountSats,
                  tokenPublicKey: input.tokenPublicKey,
                  tokenAmount: input.tokenAmount ? bytesToNumberBE(input.tokenAmount).toString() : undefined
                })),
                outputs: onChain.outputs.map(output => ({
                  rawTx: bytesToHex(output.rawTx),
                  vout: output.vout,
                  amountSats: output.amountSats,
                  tokenPublicKey: output.tokenPublicKey,
                  tokenAmount: output.tokenAmount ? bytesToNumberBE(output.tokenAmount).toString() : undefined
                })),
                broadcastedAt: onChain.broadcastedAt,
                confirmedAt: onChain.confirmedAt
              }
            }
          };
        } else if (transaction.transaction.$case === "spark") {
          const spark = transaction.transaction.spark;
          return {
            transaction: {
              $case: "spark",
              spark: {
                operationType: mapOperationType(spark.operationType),
                transactionHash: bytesToHex(spark.transactionHash),
                status: mapSparkTransactionStatus(spark.status),
                confirmedAt: spark.confirmedAt,
                leavesToCreate: spark.leavesToCreate.map(leaf => ({
                  tokenPublicKey: bytesToHex(leaf.tokenPublicKey),
                  id: leaf.id,
                  ownerPublicKey: bytesToHex(leaf.ownerPublicKey),
                  revocationPublicKey: bytesToHex(leaf.revocationPublicKey),
                  withdrawalBondSats: leaf.withdrawalBondSats,
                  withdrawalLocktime: leaf.withdrawalLocktime,
                  tokenAmount: bytesToNumberBE(leaf.tokenAmount).toString(),
                  createTxHash: bytesToHex(leaf.createTxHash),
                  createTxVoutIndex: leaf.createTxVoutIndex,
                  spendTxHash: leaf.spendTxHash ? bytesToHex(leaf.spendTxHash) : undefined,
                  spendTxVoutIndex: leaf.spendTxVoutIndex,
                  isFrozen: leaf.isFrozen
                })),
                leavesToSpend: spark.leavesToSpend.map(leaf => ({
                  tokenPublicKey: bytesToHex(leaf.tokenPublicKey),
                  id: leaf.id,
                  ownerPublicKey: bytesToHex(leaf.ownerPublicKey),
                  revocationPublicKey: bytesToHex(leaf.revocationPublicKey),
                  withdrawalBondSats: leaf.withdrawalBondSats,
                  withdrawalLocktime: leaf.withdrawalLocktime,
                  tokenAmount: bytesToNumberBE(leaf.tokenAmount).toString(),
                  createTxHash: bytesToHex(leaf.createTxHash),
                  createTxVoutIndex: leaf.createTxVoutIndex,
                  spendTxHash: leaf.spendTxHash ? bytesToHex(leaf.spendTxHash) : undefined,
                  spendTxVoutIndex: leaf.spendTxVoutIndex,
                  isFrozen: leaf.isFrozen
                })),
                sparkOperatorIdentityPublicKeys: spark.sparkOperatorIdentityPublicKeys.map(key => bytesToHex(key))
              }
            }
          };
        }
        
        return { transaction: undefined };
      }),
      nextCursor: rawTransactions.nextCursor ? {
        lastTransactionHash: bytesToHex(rawTransactions.nextCursor.lastTransactionHash),
        layer: mapLayer(rawTransactions.nextCursor.layer)
      } : undefined
    };

    return response;
  }