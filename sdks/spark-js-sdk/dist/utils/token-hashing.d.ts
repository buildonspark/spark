import { OperatorSpecificTokenTransactionSignablePayload, TokenTransaction } from "../proto/spark.js";
export declare function hashTokenTransaction(tokenTransaction: TokenTransaction, partialHash?: boolean): Uint8Array;
export declare function hashOperatorSpecificTokenTransactionSignablePayload(payload: OperatorSpecificTokenTransactionSignablePayload): Uint8Array;
