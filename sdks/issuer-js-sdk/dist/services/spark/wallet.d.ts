import { SparkWallet } from "@buildonspark/spark-js-sdk";
import { SparkSigner } from "@buildonspark/spark-js-sdk/signer";
import { LeafWithPreviousTransactionData } from "../../proto/spark.js";
import { Network } from "@buildonspark/spark-js-sdk/utils";
export declare class IssuerSparkWallet extends SparkWallet {
    private issuerTokenTransactionService;
    private tokenFreezeService;
    constructor(network: Network, signer?: SparkSigner);
    getIssuerTokenBalance(): Promise<{
        balance: bigint;
        leafCount: number;
    }>;
    mintIssuerTokens(tokenAmount: bigint): Promise<string>;
    transferIssuerTokens(tokenAmount: bigint, recipientPublicKey: string): Promise<string>;
    consolidateIssuerTokenLeaves(): Promise<string>;
    burnIssuerTokens(tokenAmount: bigint, selectedLeaves?: LeafWithPreviousTransactionData[]): Promise<void>;
    freezeIssuerTokens(ownerPublicKey: string): Promise<{
        impactedLeafIds: string[];
        impactedTokenAmount: bigint;
    }>;
    unfreezeIssuerTokens(ownerPublicKey: string): Promise<{
        impactedLeafIds: string[];
        impactedTokenAmount: bigint;
    }>;
}
