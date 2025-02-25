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
    mintIssuerTokens(tokenAmount: bigint): Promise<void>;
    transferIssuerTokens(tokenAmount: bigint, recipientPublicKey: string): Promise<void>;
    consolidateIssuerTokenLeaves(): Promise<void>;
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
