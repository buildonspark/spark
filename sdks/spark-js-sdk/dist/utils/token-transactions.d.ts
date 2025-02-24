import { LeafWithPreviousTransactionData, TokenTransaction } from "../proto/spark.js";
export declare function getTokenLeavesSum(leaves: LeafWithPreviousTransactionData[]): bigint;
export declare function extractOutputLeaves(fullTokenTransaction: TokenTransaction): LeafWithPreviousTransactionData[];
export declare function calculateAvailableTokenAmount(outputLeaves: LeafWithPreviousTransactionData[]): bigint;
export declare function checkIfSelectedLeavesAreAvailable(selectedLeaves: LeafWithPreviousTransactionData[], tokenLeaves: Map<string, LeafWithPreviousTransactionData[]>, tokenPublicKey: Uint8Array): boolean;
