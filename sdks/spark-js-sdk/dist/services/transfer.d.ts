import { Transaction } from "@scure/btc-signer";
import { LeafRefundTxSigningResult, NodeSignatures, QueryPendingTransfersResponse, Transfer, TreeNode } from "../proto/spark.js";
import { SigningCommitment } from "../signer/signer.js";
import { VerifiableSecretShare } from "../utils/secret-sharing.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection.js";
export type LeafKeyTweak = {
    leaf: TreeNode;
    signingPubKey: Uint8Array;
    newSigningPubKey: Uint8Array;
};
export type ClaimLeafData = {
    signingPubKey: Uint8Array;
    tx?: Transaction;
    refundTx?: Transaction;
    signingNonceCommitment: SigningCommitment;
    vout?: number;
};
export type LeafRefundSigningData = {
    signingPubKey: Uint8Array;
    receivingPubkey: Uint8Array;
    tx: Transaction;
    refundTx?: Transaction;
    signingNonceCommitment: SigningCommitment;
    vout: number;
};
export declare class BaseTransferService {
    protected readonly config: WalletConfigService;
    protected readonly connectionManager: ConnectionManager;
    constructor(config: WalletConfigService, connectionManager: ConnectionManager);
    sendTransferTweakKey(transfer: Transfer, leaves: LeafKeyTweak[], refundSignatureMap: Map<string, Uint8Array>): Promise<Transfer>;
    signRefunds(leafDataMap: Map<string, ClaimLeafData>, operatorSigningResults: LeafRefundTxSigningResult[], adaptorPubKey?: Uint8Array): Promise<NodeSignatures[]>;
    private prepareSendTransferKeyTweaks;
    private prepareSingleSendTransferKeyTweak;
    protected findShare(shares: VerifiableSecretShare[], operatorID: number): VerifiableSecretShare | undefined;
    private compareTransfers;
}
export declare class TransferService extends BaseTransferService {
    constructor(config: WalletConfigService, connectionManager: ConnectionManager);
    sendTransfer(leaves: LeafKeyTweak[], receiverIdentityPubkey: Uint8Array, expiryTime: Date): Promise<Transfer>;
    claimTransfer(transfer: Transfer, leaves: LeafKeyTweak[]): Promise<import("../proto/spark.js").FinalizeNodeSignaturesResponse>;
    queryPendingTransfers(): Promise<QueryPendingTransfersResponse>;
    verifyPendingTransfer(transfer: Transfer): Promise<Map<string, Uint8Array>>;
    sendSwapSignRefund(leaves: LeafKeyTweak[], receiverIdentityPubkey: Uint8Array, expiryTime: Date, adaptorPubKey?: Uint8Array): Promise<{
        transfer: Transfer;
        signatureMap: Map<string, Uint8Array>;
        leafDataMap: Map<string, LeafRefundSigningData>;
        signingResults: LeafRefundTxSigningResult[];
    }>;
    sendTransferSignRefund(leaves: LeafKeyTweak[], receiverIdentityPubkey: Uint8Array, expiryTime: Date): Promise<{
        transfer: Transfer;
        signatureMap: Map<string, Uint8Array>;
        leafDataMap: Map<string, LeafRefundSigningData>;
    }>;
    private prepareRefundSoSigningJobs;
    claimTransferTweakKeys(transfer: Transfer, leaves: LeafKeyTweak[]): Promise<void>;
    private prepareClaimLeavesKeyTweaks;
    private prepareClaimLeafKeyTweaks;
    claimTransferSignRefunds(transfer: Transfer, leafKeys: LeafKeyTweak[]): Promise<NodeSignatures[]>;
    private finalizeTransfer;
    cancelSendTransfer(transfer: Transfer): Promise<Transfer | undefined>;
}
