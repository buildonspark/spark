import { WalletConfigService } from "@buildonspark/spark-js-sdk/config";
import { ConnectionManager } from "@buildonspark/spark-js-sdk/connection";
import { FreezeTokensResponse } from "../proto/spark.js";
export declare class TokenFreezeService {
    private readonly config;
    private readonly connectionManager;
    constructor(config: WalletConfigService, connectionManager: ConnectionManager);
    freezeTokens(ownerPublicKey: Uint8Array, tokenPublicKey: Uint8Array): Promise<FreezeTokensResponse>;
    unfreezeTokens(ownerPublicKey: Uint8Array, tokenPublicKey: Uint8Array): Promise<FreezeTokensResponse>;
    private freezeOperation;
}
