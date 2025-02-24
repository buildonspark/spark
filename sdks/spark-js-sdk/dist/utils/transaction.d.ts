import { Transaction } from "@scure/btc-signer";
import { TreeNode } from "../proto/spark.js";
import { Network } from "./network.js";
export declare function createRefundTx(leaf: TreeNode, receivingPubkey: Uint8Array, network: Network): {
    refundTx: Transaction;
    sighash: Uint8Array;
};
export declare function getNextTransactionSequence(currSequence?: number): number;
