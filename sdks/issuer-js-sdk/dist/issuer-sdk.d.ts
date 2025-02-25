import { Network } from "@buildonspark/spark-js-sdk/utils";
import { IssuerSparkWallet } from "./services/spark/wallet.js";
export declare class IssuerWallet {
    private bitcoinWallet;
    private sparkWallet;
    private initialized;
    constructor(network: Network);
    initWalletFromMnemonic(mnemonic: string, enableL1Wallet?: boolean): Promise<void>;
    getSparkWallet(): IssuerSparkWallet;
    getBitcoinWallet(): any;
    isSparkInitialized(): boolean;
    isL1Initialized(): boolean;
    getTokenPublicKey(): Promise<string>;
    /**
     * Gets token balance and number of held leaves.
     * @returns An object containing the token balance and the number of owned leaves
     */
    getTokenBalance(): Promise<{
        balance: bigint;
        leafCount: number;
    }>;
    /**
     * Mints new tokens to the specified address
     * TODO: Add support for minting directly to recipient address.
     */
    mintTokens(amountToMint: bigint): Promise<void>;
    /**
     * Transfers tokens to the specified receipient.
     */
    transferTokens(amountToTransfer: bigint, recipientPublicKey: string): Promise<void>;
    /**
     * Consolidate all leaves into a single leaf.
     */
    consolidateTokens(): Promise<void>;
    /**
     * Burns issuer tokens at the specified receipient.
     */
    burnTokens(amountToBurn: bigint): Promise<void>;
    /**
     * Freezes tokens at the specified public key.
     */
    freezeTokens(freezePublicKey: string): Promise<{
        impactedLeafIds: string[];
        impactedTokenAmount: bigint;
    }>;
    /**
     * Unfreezes tokens at the specified public key.
     */
    unfreezeTokens(unfreezePublicKey: string): Promise<{
        impactedLeafIds: string[];
        impactedTokenAmount: bigint;
    }>;
}
