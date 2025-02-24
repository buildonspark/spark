import { Transaction } from "@scure/btc-signer";
import { CreationNode, FinalizeNodeSignaturesResponse, TreeNode } from "../proto/spark.js";
import { SigningCommitment } from "../signer/signer.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection.js";
export type DepositAddressTree = {
    address?: string | undefined;
    signingPublicKey: Uint8Array;
    verificationKey?: Uint8Array | undefined;
    children: DepositAddressTree[];
};
export type CreationNodeWithNonces = CreationNode & {
    nodeTxSigningCommitment?: SigningCommitment | undefined;
    refundTxSigningCommitment?: SigningCommitment | undefined;
};
export declare class TreeCreationService {
    private readonly config;
    private readonly connectionManager;
    constructor(config: WalletConfigService, connectionManager: ConnectionManager);
    generateDepositAddressForTree(vout: number, parentSigningPublicKey: Uint8Array, parentTx?: Transaction, parentNode?: TreeNode): Promise<DepositAddressTree>;
    createTree(vout: number, root: DepositAddressTree, createLeaves: boolean, parentTx?: Transaction, parentNode?: TreeNode): Promise<FinalizeNodeSignaturesResponse>;
    private createDepositAddressTree;
    private createAddressRequestNodeFromTreeNodes;
    private applyAddressNodesToTree;
    private buildChildCreationNode;
    private ephemeralAnchorOutput;
    private buildCreationNodesFromTree;
    private signNodeCreation;
    private signTreeCreation;
}
