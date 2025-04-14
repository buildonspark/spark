import { Lrc20Protos } from "@buildonspark/lrc20-sdk";

// String enums to replace numeric enums
export enum LayerType {
    L1 = "L1",
    SPARK = "SPARK"
}

export enum OperationType {
    USER_TRANSFER = "USER_TRANSFER",
    USER_BURN = "USER_BURN",
    ISSUER_ANNOUNCE = "ISSUER_ANNOUNCE",
    ISSUER_MINT = "ISSUER_MINT",
    ISSUER_TRANSFER = "ISSUER_TRANSFER",
    ISSUER_FREEZE = "ISSUER_FREEZE",
    ISSUER_UNFREEZE = "ISSUER_UNFREEZE",
    ISSUER_BURN = "ISSUER_BURN"
}

export enum OnChainTransactionStatus {
    PENDING = "PENDING",
    CONFIRMED = "CONFIRMED",
    WAITING_MINED = "WAITING_MINED",
    MINED = "MINED",
    ATTACHING = "ATTACHING",
    ATTACHED = "ATTACHED"
}

export enum SparkTransactionStatus {
    STARTED = "STARTED",
    SIGNED = "SIGNED",
    FINALIZED = "FINALIZED"
}

export type GetTokenActivityResponse = {
    transactions: Transaction[];
    nextCursor?: ListAllTokenTransactionsCursor | undefined;
}

export interface Transaction {
    transaction?: {
        $case: "onChain";
        onChain: OnChainTransaction;
    } | {
        $case: "spark";
        spark: SparkTransaction;
    } | undefined;
}

export interface TokenPubKeyInfoResponse {
    announcement: {
        tokenPubkey: {
            pubkey: string;
        };
        name: string;
        symbol: string;
        decimal: number;
        maxSupply: bigint;
        isFreezable: boolean;
    } | null;
    totalSupply: bigint;
}

export interface OnChainTokenOutput {
    rawTx: string;
    vout: number;
    amountSats: number;
    tokenPublicKey?: string | undefined;
    tokenAmount?: string | undefined;
}
export interface OnChainTransaction {
    operationType: OperationType;
    transactionHash: string;
    rawtx: string;
    status: OnChainTransactionStatus;
    inputs: OnChainTokenOutput[];
    outputs: OnChainTokenOutput[];
    broadcastedAt: Date | undefined;
    confirmedAt: Date | undefined;
}
export interface SparkTransaction {
    operationType: OperationType;
    transactionHash: string;
    status: SparkTransactionStatus;
    confirmedAt: Date | undefined;
    leavesToCreate: SparkLeaf[];
    leavesToSpend: SparkLeaf[];
    sparkOperatorIdentityPublicKeys: string[];
}
export interface SparkLeaf {
    tokenPublicKey: string;
    id: string;
    ownerPublicKey: string;
    revocationPublicKey: string;
    withdrawalBondSats: number;
    withdrawalLocktime: number;
    tokenAmount: string;
    createTxHash: string;
    createTxVoutIndex: number;
    spendTxHash?: string | undefined;
    spendTxVoutIndex?: number | undefined;
    isFrozen?: boolean | undefined;
}

export interface ListAllTokenTransactionsCursor {
    lastTransactionHash: string;
    layer: LayerType;
}
