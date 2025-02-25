import { TokenTransactionService } from "@buildonspark/spark-js-sdk/token-transactions";
import { TokenTransaction } from "../../proto/spark.js";
import { ConnectionManager } from "@buildonspark/spark-js-sdk/connection";
import { WalletConfigService } from "@buildonspark/spark-js-sdk/config";
export declare class IssuerTokenTransactionService extends TokenTransactionService {
    constructor(config: WalletConfigService, connectionManager: ConnectionManager);
    constructMintTokenTransaction(tokenPublicKey: Uint8Array, tokenAmount: bigint): Promise<TokenTransaction>;
}
