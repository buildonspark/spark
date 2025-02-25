import { LeafWithPreviousTransactionData, TokenTransaction } from "../proto/spark.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection.js";
export declare class TokenTransactionService {
    protected readonly config: WalletConfigService;
    protected readonly connectionManager: ConnectionManager;
    constructor(config: WalletConfigService, connectionManager: ConnectionManager);
    constructTransferTokenTransaction(selectedLeaves: LeafWithPreviousTransactionData[], recipientPublicKey: Uint8Array, tokenPublicKey: Uint8Array, tokenAmount: bigint, transferBackToIdentityPublicKey?: boolean): Promise<TokenTransaction>;
    collectOperatorIdentityPublicKeys(): Uint8Array[];
    broadcastTokenTransaction(tokenTransaction: TokenTransaction, leafToSpendSigningPublicKeys?: Uint8Array[], leafToSpendRevocationPublicKeys?: Uint8Array[]): Promise<TokenTransaction>;
    finalizeTokenTransaction(finalTokenTransaction: TokenTransaction, leafToSpendRevocationKeys: Uint8Array[], threshold: number): Promise<TokenTransaction>;
    constructConsolidateTokenTransaction(selectedLeaves: LeafWithPreviousTransactionData[], tokenPublicKey: Uint8Array, transferBackToIdentityPublicKey?: boolean): Promise<TokenTransaction>;
    fetchOwnedTokenLeaves(ownerPublicKeys: Uint8Array[], tokenPublicKeys: Uint8Array[]): Promise<LeafWithPreviousTransactionData[]>;
    syncTokenLeaves(tokenLeaves: Map<string, LeafWithPreviousTransactionData[]>): Promise<void>;
    selectTokenLeaves(tokenLeaves: LeafWithPreviousTransactionData[], tokenAmount: bigint): LeafWithPreviousTransactionData[];
    private signMessageWithKey;
}
