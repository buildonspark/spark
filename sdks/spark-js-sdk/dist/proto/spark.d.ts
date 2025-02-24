import { BinaryReader, BinaryWriter } from "@bufbuild/protobuf/wire";
import { type CallContext, type CallOptions } from "nice-grpc-common";
import { SignatureIntent, SigningCommitment } from "./common.js";
import { Empty } from "./google/protobuf/empty.js";
export declare const protobufPackage = "spark";
export declare enum Network {
    MAINNET = 0,
    REGTEST = 1,
    TESTNET = 2,
    SIGNET = 3,
    UNRECOGNIZED = -1
}
export declare function networkFromJSON(object: any): Network;
export declare function networkToJSON(object: Network): string;
export declare enum TransferStatus {
    TRANSFER_STATUS_SENDER_INITIATED = 0,
    TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING = 1,
    TRANSFER_STATUS_SENDER_KEY_TWEAKED = 2,
    TRANSFER_STATUS_RECEIVER_KEY_TWEAKED = 3,
    TRANSFER_STATUSR_RECEIVER_REFUND_SIGNED = 4,
    TRANSFER_STATUS_COMPLETED = 5,
    TRANSFER_STATUS_EXPIRED = 6,
    UNRECOGNIZED = -1
}
export declare function transferStatusFromJSON(object: any): TransferStatus;
export declare function transferStatusToJSON(object: TransferStatus): string;
export interface DepositAddressProof {
    addressSignatures: {
        [key: string]: Uint8Array;
    };
    proofOfPossessionSignature: Uint8Array;
}
export interface DepositAddressProof_AddressSignaturesEntry {
    key: string;
    value: Uint8Array;
}
export interface GenerateDepositAddressRequest {
    signingPublicKey: Uint8Array;
    identityPublicKey: Uint8Array;
    network: Network;
}
export interface Address {
    address: string;
    verifyingKey: Uint8Array;
    depositAddressProof: DepositAddressProof | undefined;
}
export interface GenerateDepositAddressResponse {
    depositAddress: Address | undefined;
}
export interface UTXO {
    rawTx: Uint8Array;
    vout: number;
    network: Network;
}
export interface NodeOutput {
    nodeId: string;
    vout: number;
}
export interface SigningJob {
    signingPublicKey: Uint8Array;
    rawTx: Uint8Array;
    signingNonceCommitment: SigningCommitment | undefined;
}
export interface SigningKeyshare {
    ownerIdentifiers: string[];
    threshold: number;
}
export interface SigningResult {
    publicKeys: {
        [key: string]: Uint8Array;
    };
    signingNonceCommitments: {
        [key: string]: SigningCommitment;
    };
    signatureShares: {
        [key: string]: Uint8Array;
    };
    signingKeyshare: SigningKeyshare | undefined;
}
export interface SigningResult_PublicKeysEntry {
    key: string;
    value: Uint8Array;
}
export interface SigningResult_SigningNonceCommitmentsEntry {
    key: string;
    value: SigningCommitment | undefined;
}
export interface SigningResult_SignatureSharesEntry {
    key: string;
    value: Uint8Array;
}
export interface NodeSignatureShares {
    nodeId: string;
    nodeTxSigningResult: SigningResult | undefined;
    refundTxSigningResult: SigningResult | undefined;
    verifyingKey: Uint8Array;
}
export interface NodeSignatures {
    nodeId: string;
    nodeTxSignature: Uint8Array;
    refundTxSignature: Uint8Array;
}
export interface StartTreeCreationRequest {
    identityPublicKey: Uint8Array;
    onChainUtxo: UTXO | undefined;
    rootTxSigningJob: SigningJob | undefined;
    refundTxSigningJob: SigningJob | undefined;
}
export interface StartTreeCreationResponse {
    treeId: string;
    rootNodeSignatureShares: NodeSignatureShares | undefined;
}
/**
 * This proto is constructed by the wallet to specify leaves it wants to spend as
 * part of the token transaction.
 */
export interface TokenLeafToSpend {
    prevTokenTransactionHash: Uint8Array;
    prevTokenTransactionLeafVout: number;
}
export interface TransferInput {
    leavesToSpend: TokenLeafToSpend[];
}
export interface MintInput {
    issuerPublicKey: Uint8Array;
    /**
     * Issuer provided timestamp of when the transaction was signed/constructed.
     * Helps provide idempotency and ensures that each mint input signature is unique
     * as long as multiple mint signatures are not happening at the same time. Also gives a
     * potentially useful data point for when the issuer authorized from their
     * perspective.  Note that we have no way of proving this is accurate.
     * TODO: Consider whether implementing generic idempotency controls and/or a
     * random nonce would be favorable to populating this field.
     */
    issuerProvidedTimestamp: number;
}
/**
 * This proto is constructed by the wallet to specify leaves it wants to create
 * as part of a token transaction.  id and revocation public key should remain unfilled
 * so that the SE can fill them as part of the StartTokenTransaction() call.
 */
export interface TokenLeafOutput {
    id?: string | undefined;
    ownerPublicKey: Uint8Array;
    revocationPublicKey?: Uint8Array | undefined;
    withdrawBondSats?: number | undefined;
    withdrawRelativeBlockLocktime?: number | undefined;
    tokenPublicKey: Uint8Array;
    /** Decoded uint128 */
    tokenAmount: Uint8Array;
}
/**
 * This proto is constructed by the wallet and is the core transaction data structure.
 * This proto is deterministically hashed to generate the token_transaction_hash that
 * is cooperatively signed by the SO group to confirm a token transaction.
 */
export interface TokenTransaction {
    /**
     * For mint transactions issuer_public_key will be specified without any leaves_to_spend.
     * For transfer transactions the token amount in the input leaves must match the token amount in the output leaves.
     */
    tokenInput?: {
        $case: "mintInput";
        mintInput: MintInput;
    } | {
        $case: "transferInput";
        transferInput: TransferInput;
    } | undefined;
    outputLeaves: TokenLeafOutput[];
    sparkOperatorIdentityPublicKeys: Uint8Array[];
}
export interface TokenTransactionSignatures {
    /**
     * Filled by signing the partial token transaction hash with the owner/issuer private key.
     * For mint transactions this will be one signature for the input issuer_public_key
     * For transfer transactions this will be one for each leaf for the leaf owner_public_key
     * This is a DER signature which can be between 68 and 73 bytes.
     */
    ownerSignatures: Uint8Array[];
}
export interface StartTokenTransactionRequest {
    identityPublicKey: Uint8Array;
    partialTokenTransaction: TokenTransaction | undefined;
    /** List of ecdsa signatures authorizing movement of tokens from the token input. */
    tokenTransactionSignatures: TokenTransactionSignatures | undefined;
}
export interface StartTokenTransactionResponse {
    /**
     * This is the same token transaction sent by the wallet with leaf revocation public keys
     * filled. This is the final transaction that is published and gossiped among LRC20 nodes.
     */
    finalTokenTransaction: TokenTransaction | undefined;
    /**
     * Information for fetching and resolving the revocation keyshare on a transfer operation.
     * Contains the threshold of keyshares needed and the SO owners of those keyshares.
     */
    keyshareInfo: SigningKeyshare | undefined;
}
export interface OperatorSpecificTokenTransactionSignablePayload {
    finalTokenTransactionHash: Uint8Array;
    operatorIdentityPublicKey: Uint8Array;
}
/**
 * This message allows the sender of a leaf being spent to provide final evidence
 * that it owns a leaf to an SO when requesting signing and release of the  revocation keyshare.
 */
export interface OperatorSpecificTokenTransactionSignature {
    ownerPublicKey: Uint8Array;
    /** This is a DER signature which can be between 68 and 73 bytes. */
    ownerSignature: Uint8Array;
    payload: OperatorSpecificTokenTransactionSignablePayload | undefined;
}
export interface SignTokenTransactionRequest {
    finalTokenTransaction: TokenTransaction | undefined;
    operatorSpecificSignatures: OperatorSpecificTokenTransactionSignature[];
    identityPublicKey: Uint8Array;
}
export interface SignTokenTransactionResponse {
    sparkOperatorSignature: Uint8Array;
    tokenTransactionRevocationKeyshares: Uint8Array[];
}
export interface FinalizeTokenTransactionRequest {
    finalTokenTransaction: TokenTransaction | undefined;
    /**
     * List of ordered revocation keys that map 1:1 with leaves being spent in the
     * token transaction.
     */
    leafToSpendRevocationKeys: Uint8Array[];
    identityPublicKey: Uint8Array;
}
export interface FreezeTokensPayload {
    ownerPublicKey: Uint8Array;
    tokenPublicKey: Uint8Array;
    issuerProvidedTimestamp: number;
    operatorIdentityPublicKey: Uint8Array;
    /** Set to false when requesting a freeze. */
    shouldUnfreeze: boolean;
}
export interface FreezeTokensRequest {
    freezeTokensPayload: FreezeTokensPayload | undefined;
    /** This is a DER signature which can be between 68 and 73 bytes. */
    issuerSignature: Uint8Array;
}
export interface FreezeTokensResponse {
    impactedLeafIds: string[];
    /** Decoded uint128 */
    impactedTokenAmount: Uint8Array;
}
export interface GetOwnedTokenLeavesRequest {
    ownerPublicKeys: Uint8Array[];
    /** Optionally provide token public keys. If not set return leaves for all tokens. */
    tokenPublicKeys: Uint8Array[];
}
export interface LeafWithPreviousTransactionData {
    leaf: TokenLeafOutput | undefined;
    previousTransactionHash: Uint8Array;
    previousTransactionVout: number;
}
export interface GetOwnedTokenLeavesResponse {
    leavesWithPreviousTransactionData: LeafWithPreviousTransactionData[];
}
export interface TreeNode {
    id: string;
    treeId: string;
    value: number;
    parentNodeId?: string | undefined;
    nodeTx: Uint8Array;
    refundTx: Uint8Array;
    vout: number;
    verifyingPublicKey: Uint8Array;
    ownerIdentityPublicKey: Uint8Array;
    signingKeyshare: SigningKeyshare | undefined;
    status: string;
    network: Network;
}
export interface FinalizeNodeSignaturesRequest {
    intent: SignatureIntent;
    nodeSignatures: NodeSignatures[];
}
export interface FinalizeNodeSignaturesResponse {
    nodes: TreeNode[];
}
export interface SecretShare {
    secretShare: Uint8Array;
    proofs: Uint8Array[];
}
export interface LeafRefundTxSigningJob {
    leafId: string;
    refundTxSigningJob: SigningJob | undefined;
}
export interface LeafRefundTxSigningResult {
    leafId: string;
    refundTxSigningResult: SigningResult | undefined;
    verifyingKey: Uint8Array;
}
export interface StartSendTransferRequest {
    transferId: string;
    ownerIdentityPublicKey: Uint8Array;
    leavesToSend: LeafRefundTxSigningJob[];
    receiverIdentityPublicKey: Uint8Array;
    expiryTime: Date | undefined;
}
export interface StartSendTransferResponse {
    transfer: Transfer | undefined;
    signingResults: LeafRefundTxSigningResult[];
}
export interface SendLeafKeyTweak {
    leafId: string;
    secretShareTweak: SecretShare | undefined;
    pubkeySharesTweak: {
        [key: string]: Uint8Array;
    };
    secretCipher: Uint8Array;
    /** Signature over Sha256(leaf_id||transfer_id||secret_cipher) */
    signature: Uint8Array;
    refundSignature: Uint8Array;
}
export interface SendLeafKeyTweak_PubkeySharesTweakEntry {
    key: string;
    value: Uint8Array;
}
export interface CompleteSendTransferRequest {
    transferId: string;
    ownerIdentityPublicKey: Uint8Array;
    leavesToSend: SendLeafKeyTweak[];
}
export interface Transfer {
    id: string;
    senderIdentityPublicKey: Uint8Array;
    receiverIdentityPublicKey: Uint8Array;
    status: TransferStatus;
    totalValue: number;
    expiryTime: Date | undefined;
    leaves: TransferLeaf[];
}
export interface TransferLeaf {
    leaf: TreeNode | undefined;
    secretCipher: Uint8Array;
    signature: Uint8Array;
    intermediateRefundTx: Uint8Array;
}
export interface CompleteSendTransferResponse {
    transfer: Transfer | undefined;
}
export interface QueryPendingTransfersRequest {
    participant?: {
        $case: "receiverIdentityPublicKey";
        receiverIdentityPublicKey: Uint8Array;
    } | {
        $case: "senderIdentityPublicKey";
        senderIdentityPublicKey: Uint8Array;
    } | undefined;
    transferIds: string[];
}
export interface QueryPendingTransfersResponse {
    transfers: Transfer[];
}
export interface ClaimLeafKeyTweak {
    leafId: string;
    secretShareTweak: SecretShare | undefined;
    pubkeySharesTweak: {
        [key: string]: Uint8Array;
    };
}
export interface ClaimLeafKeyTweak_PubkeySharesTweakEntry {
    key: string;
    value: Uint8Array;
}
export interface ClaimTransferTweakKeysRequest {
    transferId: string;
    ownerIdentityPublicKey: Uint8Array;
    leavesToReceive: ClaimLeafKeyTweak[];
}
export interface ClaimTransferSignRefundsRequest {
    transferId: string;
    ownerIdentityPublicKey: Uint8Array;
    signingJobs: LeafRefundTxSigningJob[];
}
export interface ClaimTransferSignRefundsResponse {
    signingResults: LeafRefundTxSigningResult[];
}
export interface AggregateNodesRequest {
    nodeIds: string[];
    signingJob: SigningJob | undefined;
    /** Serves as a temporary identity public key, this should be get from auth process. */
    ownerIdentityPublicKey: Uint8Array;
}
export interface AggregateNodesResponse {
    aggregateSignature: SigningResult | undefined;
    verifyingKey: Uint8Array;
    parentNodeTx: Uint8Array;
    parentNodeVout: number;
}
export interface StorePreimageShareRequest {
    paymentHash: Uint8Array;
    preimageShare: SecretShare | undefined;
    threshold: number;
    invoiceString: string;
    userIdentityPublicKey: Uint8Array;
}
export interface RequestedSigningCommitments {
    signingNonceCommitments: {
        [key: string]: SigningCommitment;
    };
}
export interface RequestedSigningCommitments_SigningNonceCommitmentsEntry {
    key: string;
    value: SigningCommitment | undefined;
}
export interface GetSigningCommitmentsRequest {
    nodeIds: string[];
}
export interface GetSigningCommitmentsResponse {
    signingCommitments: RequestedSigningCommitments[];
}
export interface SigningCommitments {
    signingCommitments: {
        [key: string]: SigningCommitment;
    };
}
export interface SigningCommitments_SigningCommitmentsEntry {
    key: string;
    value: SigningCommitment | undefined;
}
export interface UserSignedRefund {
    nodeId: string;
    refundTx: Uint8Array;
    userSignature: Uint8Array;
    signingCommitments: SigningCommitments | undefined;
    userSignatureCommitment: SigningCommitment | undefined;
}
export interface InvoiceAmountProof {
    bolt11Invoice: string;
}
export interface InvoiceAmount {
    valueSats: number;
    invoiceAmountProof: InvoiceAmountProof | undefined;
}
export interface InitiatePreimageSwapRequest {
    paymentHash: Uint8Array;
    userSignedRefunds: UserSignedRefund[];
    invoiceAmount: InvoiceAmount | undefined;
    reason: InitiatePreimageSwapRequest_Reason;
    transfer: StartSendTransferRequest | undefined;
    receiverIdentityPublicKey: Uint8Array;
    feeSats: number;
}
export declare enum InitiatePreimageSwapRequest_Reason {
    /** REASON_SEND - The associated lightning service is sending the payment. */
    REASON_SEND = 0,
    /** REASON_RECEIVE - The associated lightning service is receiving the payment. */
    REASON_RECEIVE = 1,
    UNRECOGNIZED = -1
}
export declare function initiatePreimageSwapRequest_ReasonFromJSON(object: any): InitiatePreimageSwapRequest_Reason;
export declare function initiatePreimageSwapRequest_ReasonToJSON(object: InitiatePreimageSwapRequest_Reason): string;
export interface InitiatePreimageSwapResponse {
    preimage: Uint8Array;
    transfer: Transfer | undefined;
}
export interface OutPoint {
    txid: Uint8Array;
    vout: number;
}
export interface CooperativeExitRequest {
    transfer: StartSendTransferRequest | undefined;
    exitId: string;
    exitTxid: Uint8Array;
}
export interface CooperativeExitResponse {
    transfer: Transfer | undefined;
    signingResults: LeafRefundTxSigningResult[];
}
export interface LeafSwapRequest {
    transfer: StartSendTransferRequest | undefined;
    swapId: string;
    adaptorPublicKey: Uint8Array;
}
export interface LeafSwapResponse {
    transfer: Transfer | undefined;
    signingResults: LeafRefundTxSigningResult[];
}
export interface RefreshTimelockRequest {
    leafId: string;
    ownerIdentityPublicKey: Uint8Array;
    signingJobs: SigningJob[];
}
export interface RefreshTimelockSigningResult {
    signingResult: SigningResult | undefined;
    /** Should maybe just be a part of SigningResult? */
    verifyingKey: Uint8Array;
}
export interface RefreshTimelockResponse {
    signingResults: RefreshTimelockSigningResult[];
}
export interface ExtendLeafRequest {
    leafId: string;
    ownerIdentityPublicKey: Uint8Array;
    nodeTxSigningJob: SigningJob | undefined;
    refundTxSigningJob: SigningJob | undefined;
}
export interface ExtendLeafSigningResult {
    signingResult: SigningResult | undefined;
    verifyingKey: Uint8Array;
}
export interface ExtendLeafResponse {
    leafId: string;
    nodeTxSigningResult: ExtendLeafSigningResult | undefined;
    refundTxSigningResult: ExtendLeafSigningResult | undefined;
}
export interface AddressRequestNode {
    userPublicKey: Uint8Array;
    children: AddressRequestNode[];
}
export interface PrepareTreeAddressRequest {
    source?: {
        $case: "parentNodeOutput";
        parentNodeOutput: NodeOutput;
    } | {
        $case: "onChainUtxo";
        onChainUtxo: UTXO;
    } | undefined;
    /**
     * The tx on this node is to spend the source's utxo.
     * The user's public key should already be registered with the SE for the root node.
     */
    node: AddressRequestNode | undefined;
    userIdentityPublicKey: Uint8Array;
}
export interface AddressNode {
    address: Address | undefined;
    children: AddressNode[];
}
export interface PrepareTreeAddressResponse {
    node: AddressNode | undefined;
}
export interface CreationNode {
    /** This is the tx that spends the parent node's output. */
    nodeTxSigningJob: SigningJob | undefined;
    /** The refund tx can only exist if there's no children. */
    refundTxSigningJob: SigningJob | undefined;
    /** The children will spend the output of the node's tx. Vout is the index of the child. */
    children: CreationNode[];
}
export interface CreateTreeRequest {
    source?: {
        $case: "parentNodeOutput";
        parentNodeOutput: NodeOutput;
    } | {
        $case: "onChainUtxo";
        onChainUtxo: UTXO;
    } | undefined;
    /** The node should contain the tx that spends the source's utxo. */
    node: CreationNode | undefined;
    /** The owner of the tree. */
    userIdentityPublicKey: Uint8Array;
}
export interface CreationResponseNode {
    nodeId: string;
    nodeTxSigningResult: SigningResult | undefined;
    refundTxSigningResult: SigningResult | undefined;
    children: CreationResponseNode[];
}
export interface CreateTreeResponse {
    node: CreationResponseNode | undefined;
}
export interface SigningOperatorInfo {
    index: number;
    identifier: string;
    publicKey: Uint8Array;
    address: string;
}
export interface GetSigningOperatorListResponse {
    signingOperators: {
        [key: string]: SigningOperatorInfo;
    };
}
export interface GetSigningOperatorListResponse_SigningOperatorsEntry {
    key: string;
    value: SigningOperatorInfo | undefined;
}
export interface QueryUserSignedRefundsRequest {
    paymentHash: Uint8Array;
    identityPublicKey: Uint8Array;
}
export interface QueryUserSignedRefundsResponse {
    userSignedRefunds: UserSignedRefund[];
}
export interface ProvidePreimageRequest {
    paymentHash: Uint8Array;
    preimage: Uint8Array;
    identityPublicKey: Uint8Array;
}
export interface ProvidePreimageResponse {
    transfer: Transfer | undefined;
}
export interface ReturnLightningPaymentRequest {
    paymentHash: Uint8Array;
    userIdentityPublicKey: Uint8Array;
}
export interface TreeNodeIds {
    nodeIds: string[];
}
export interface QueryNodesRequest {
    source?: {
        $case: "ownerIdentityPubkey";
        ownerIdentityPubkey: Uint8Array;
    } | {
        $case: "nodeIds";
        nodeIds: TreeNodeIds;
    } | undefined;
    includeParents: boolean;
}
export interface QueryNodesResponse {
    nodes: {
        [key: string]: TreeNode;
    };
}
export interface QueryNodesResponse_NodesEntry {
    key: string;
    value: TreeNode | undefined;
}
export interface CancelSendTransferRequest {
    transferId: string;
    senderIdentityPublicKey: Uint8Array;
}
export interface CancelSendTransferResponse {
    transfer: Transfer | undefined;
}
export interface QueryAllTransfersRequest {
    identityPublicKey: Uint8Array;
    limit: number;
    offset: number;
}
export interface QueryAllTransfersResponse {
    transfers: Transfer[];
    offset: number;
}
export declare const DepositAddressProof: MessageFns<DepositAddressProof>;
export declare const DepositAddressProof_AddressSignaturesEntry: MessageFns<DepositAddressProof_AddressSignaturesEntry>;
export declare const GenerateDepositAddressRequest: MessageFns<GenerateDepositAddressRequest>;
export declare const Address: MessageFns<Address>;
export declare const GenerateDepositAddressResponse: MessageFns<GenerateDepositAddressResponse>;
export declare const UTXO: MessageFns<UTXO>;
export declare const NodeOutput: MessageFns<NodeOutput>;
export declare const SigningJob: MessageFns<SigningJob>;
export declare const SigningKeyshare: MessageFns<SigningKeyshare>;
export declare const SigningResult: MessageFns<SigningResult>;
export declare const SigningResult_PublicKeysEntry: MessageFns<SigningResult_PublicKeysEntry>;
export declare const SigningResult_SigningNonceCommitmentsEntry: MessageFns<SigningResult_SigningNonceCommitmentsEntry>;
export declare const SigningResult_SignatureSharesEntry: MessageFns<SigningResult_SignatureSharesEntry>;
export declare const NodeSignatureShares: MessageFns<NodeSignatureShares>;
export declare const NodeSignatures: MessageFns<NodeSignatures>;
export declare const StartTreeCreationRequest: MessageFns<StartTreeCreationRequest>;
export declare const StartTreeCreationResponse: MessageFns<StartTreeCreationResponse>;
export declare const TokenLeafToSpend: MessageFns<TokenLeafToSpend>;
export declare const TransferInput: MessageFns<TransferInput>;
export declare const MintInput: MessageFns<MintInput>;
export declare const TokenLeafOutput: MessageFns<TokenLeafOutput>;
export declare const TokenTransaction: MessageFns<TokenTransaction>;
export declare const TokenTransactionSignatures: MessageFns<TokenTransactionSignatures>;
export declare const StartTokenTransactionRequest: MessageFns<StartTokenTransactionRequest>;
export declare const StartTokenTransactionResponse: MessageFns<StartTokenTransactionResponse>;
export declare const OperatorSpecificTokenTransactionSignablePayload: MessageFns<OperatorSpecificTokenTransactionSignablePayload>;
export declare const OperatorSpecificTokenTransactionSignature: MessageFns<OperatorSpecificTokenTransactionSignature>;
export declare const SignTokenTransactionRequest: MessageFns<SignTokenTransactionRequest>;
export declare const SignTokenTransactionResponse: MessageFns<SignTokenTransactionResponse>;
export declare const FinalizeTokenTransactionRequest: MessageFns<FinalizeTokenTransactionRequest>;
export declare const FreezeTokensPayload: MessageFns<FreezeTokensPayload>;
export declare const FreezeTokensRequest: MessageFns<FreezeTokensRequest>;
export declare const FreezeTokensResponse: MessageFns<FreezeTokensResponse>;
export declare const GetOwnedTokenLeavesRequest: MessageFns<GetOwnedTokenLeavesRequest>;
export declare const LeafWithPreviousTransactionData: MessageFns<LeafWithPreviousTransactionData>;
export declare const GetOwnedTokenLeavesResponse: MessageFns<GetOwnedTokenLeavesResponse>;
export declare const TreeNode: MessageFns<TreeNode>;
export declare const FinalizeNodeSignaturesRequest: MessageFns<FinalizeNodeSignaturesRequest>;
export declare const FinalizeNodeSignaturesResponse: MessageFns<FinalizeNodeSignaturesResponse>;
export declare const SecretShare: MessageFns<SecretShare>;
export declare const LeafRefundTxSigningJob: MessageFns<LeafRefundTxSigningJob>;
export declare const LeafRefundTxSigningResult: MessageFns<LeafRefundTxSigningResult>;
export declare const StartSendTransferRequest: MessageFns<StartSendTransferRequest>;
export declare const StartSendTransferResponse: MessageFns<StartSendTransferResponse>;
export declare const SendLeafKeyTweak: MessageFns<SendLeafKeyTweak>;
export declare const SendLeafKeyTweak_PubkeySharesTweakEntry: MessageFns<SendLeafKeyTweak_PubkeySharesTweakEntry>;
export declare const CompleteSendTransferRequest: MessageFns<CompleteSendTransferRequest>;
export declare const Transfer: MessageFns<Transfer>;
export declare const TransferLeaf: MessageFns<TransferLeaf>;
export declare const CompleteSendTransferResponse: MessageFns<CompleteSendTransferResponse>;
export declare const QueryPendingTransfersRequest: MessageFns<QueryPendingTransfersRequest>;
export declare const QueryPendingTransfersResponse: MessageFns<QueryPendingTransfersResponse>;
export declare const ClaimLeafKeyTweak: MessageFns<ClaimLeafKeyTweak>;
export declare const ClaimLeafKeyTweak_PubkeySharesTweakEntry: MessageFns<ClaimLeafKeyTweak_PubkeySharesTweakEntry>;
export declare const ClaimTransferTweakKeysRequest: MessageFns<ClaimTransferTweakKeysRequest>;
export declare const ClaimTransferSignRefundsRequest: MessageFns<ClaimTransferSignRefundsRequest>;
export declare const ClaimTransferSignRefundsResponse: MessageFns<ClaimTransferSignRefundsResponse>;
export declare const AggregateNodesRequest: MessageFns<AggregateNodesRequest>;
export declare const AggregateNodesResponse: MessageFns<AggregateNodesResponse>;
export declare const StorePreimageShareRequest: MessageFns<StorePreimageShareRequest>;
export declare const RequestedSigningCommitments: MessageFns<RequestedSigningCommitments>;
export declare const RequestedSigningCommitments_SigningNonceCommitmentsEntry: MessageFns<RequestedSigningCommitments_SigningNonceCommitmentsEntry>;
export declare const GetSigningCommitmentsRequest: MessageFns<GetSigningCommitmentsRequest>;
export declare const GetSigningCommitmentsResponse: MessageFns<GetSigningCommitmentsResponse>;
export declare const SigningCommitments: MessageFns<SigningCommitments>;
export declare const SigningCommitments_SigningCommitmentsEntry: MessageFns<SigningCommitments_SigningCommitmentsEntry>;
export declare const UserSignedRefund: MessageFns<UserSignedRefund>;
export declare const InvoiceAmountProof: MessageFns<InvoiceAmountProof>;
export declare const InvoiceAmount: MessageFns<InvoiceAmount>;
export declare const InitiatePreimageSwapRequest: MessageFns<InitiatePreimageSwapRequest>;
export declare const InitiatePreimageSwapResponse: MessageFns<InitiatePreimageSwapResponse>;
export declare const OutPoint: MessageFns<OutPoint>;
export declare const CooperativeExitRequest: MessageFns<CooperativeExitRequest>;
export declare const CooperativeExitResponse: MessageFns<CooperativeExitResponse>;
export declare const LeafSwapRequest: MessageFns<LeafSwapRequest>;
export declare const LeafSwapResponse: MessageFns<LeafSwapResponse>;
export declare const RefreshTimelockRequest: MessageFns<RefreshTimelockRequest>;
export declare const RefreshTimelockSigningResult: MessageFns<RefreshTimelockSigningResult>;
export declare const RefreshTimelockResponse: MessageFns<RefreshTimelockResponse>;
export declare const ExtendLeafRequest: MessageFns<ExtendLeafRequest>;
export declare const ExtendLeafSigningResult: MessageFns<ExtendLeafSigningResult>;
export declare const ExtendLeafResponse: MessageFns<ExtendLeafResponse>;
export declare const AddressRequestNode: MessageFns<AddressRequestNode>;
export declare const PrepareTreeAddressRequest: MessageFns<PrepareTreeAddressRequest>;
export declare const AddressNode: MessageFns<AddressNode>;
export declare const PrepareTreeAddressResponse: MessageFns<PrepareTreeAddressResponse>;
export declare const CreationNode: MessageFns<CreationNode>;
export declare const CreateTreeRequest: MessageFns<CreateTreeRequest>;
export declare const CreationResponseNode: MessageFns<CreationResponseNode>;
export declare const CreateTreeResponse: MessageFns<CreateTreeResponse>;
export declare const SigningOperatorInfo: MessageFns<SigningOperatorInfo>;
export declare const GetSigningOperatorListResponse: MessageFns<GetSigningOperatorListResponse>;
export declare const GetSigningOperatorListResponse_SigningOperatorsEntry: MessageFns<GetSigningOperatorListResponse_SigningOperatorsEntry>;
export declare const QueryUserSignedRefundsRequest: MessageFns<QueryUserSignedRefundsRequest>;
export declare const QueryUserSignedRefundsResponse: MessageFns<QueryUserSignedRefundsResponse>;
export declare const ProvidePreimageRequest: MessageFns<ProvidePreimageRequest>;
export declare const ProvidePreimageResponse: MessageFns<ProvidePreimageResponse>;
export declare const ReturnLightningPaymentRequest: MessageFns<ReturnLightningPaymentRequest>;
export declare const TreeNodeIds: MessageFns<TreeNodeIds>;
export declare const QueryNodesRequest: MessageFns<QueryNodesRequest>;
export declare const QueryNodesResponse: MessageFns<QueryNodesResponse>;
export declare const QueryNodesResponse_NodesEntry: MessageFns<QueryNodesResponse_NodesEntry>;
export declare const CancelSendTransferRequest: MessageFns<CancelSendTransferRequest>;
export declare const CancelSendTransferResponse: MessageFns<CancelSendTransferResponse>;
export declare const QueryAllTransfersRequest: MessageFns<QueryAllTransfersRequest>;
export declare const QueryAllTransfersResponse: MessageFns<QueryAllTransfersResponse>;
export type SparkServiceDefinition = typeof SparkServiceDefinition;
export declare const SparkServiceDefinition: {
    readonly name: "SparkService";
    readonly fullName: "spark.SparkService";
    readonly methods: {
        readonly generate_deposit_address: {
            readonly name: "generate_deposit_address";
            readonly requestType: MessageFns<GenerateDepositAddressRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<GenerateDepositAddressResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly start_tree_creation: {
            readonly name: "start_tree_creation";
            readonly requestType: MessageFns<StartTreeCreationRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<StartTreeCreationResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly finalize_node_signatures: {
            readonly name: "finalize_node_signatures";
            readonly requestType: MessageFns<FinalizeNodeSignaturesRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<FinalizeNodeSignaturesResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly start_send_transfer: {
            readonly name: "start_send_transfer";
            readonly requestType: MessageFns<StartSendTransferRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<StartSendTransferResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly complete_send_transfer: {
            readonly name: "complete_send_transfer";
            readonly requestType: MessageFns<CompleteSendTransferRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<CompleteSendTransferResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly query_pending_transfers: {
            readonly name: "query_pending_transfers";
            readonly requestType: MessageFns<QueryPendingTransfersRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<QueryPendingTransfersResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly query_all_transfers: {
            readonly name: "query_all_transfers";
            readonly requestType: MessageFns<QueryAllTransfersRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<QueryAllTransfersResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly claim_transfer_tweak_keys: {
            readonly name: "claim_transfer_tweak_keys";
            readonly requestType: MessageFns<ClaimTransferTweakKeysRequest>;
            readonly requestStream: false;
            readonly responseType: import("./google/protobuf/empty.js").MessageFns<Empty>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly claim_transfer_sign_refunds: {
            readonly name: "claim_transfer_sign_refunds";
            readonly requestType: MessageFns<ClaimTransferSignRefundsRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<ClaimTransferSignRefundsResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly aggregate_nodes: {
            readonly name: "aggregate_nodes";
            readonly requestType: MessageFns<AggregateNodesRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<AggregateNodesResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly store_preimage_share: {
            readonly name: "store_preimage_share";
            readonly requestType: MessageFns<StorePreimageShareRequest>;
            readonly requestStream: false;
            readonly responseType: import("./google/protobuf/empty.js").MessageFns<Empty>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly get_signing_commitments: {
            readonly name: "get_signing_commitments";
            readonly requestType: MessageFns<GetSigningCommitmentsRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<GetSigningCommitmentsResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly cooperative_exit: {
            readonly name: "cooperative_exit";
            readonly requestType: MessageFns<CooperativeExitRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<CooperativeExitResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly initiate_preimage_swap: {
            readonly name: "initiate_preimage_swap";
            readonly requestType: MessageFns<InitiatePreimageSwapRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<InitiatePreimageSwapResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly provide_preimage: {
            readonly name: "provide_preimage";
            readonly requestType: MessageFns<ProvidePreimageRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<ProvidePreimageResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly leaf_swap: {
            readonly name: "leaf_swap";
            readonly requestType: MessageFns<LeafSwapRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<LeafSwapResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly refresh_timelock: {
            readonly name: "refresh_timelock";
            readonly requestType: MessageFns<RefreshTimelockRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<RefreshTimelockResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly extend_leaf: {
            readonly name: "extend_leaf";
            readonly requestType: MessageFns<ExtendLeafRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<ExtendLeafResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly prepare_tree_address: {
            readonly name: "prepare_tree_address";
            readonly requestType: MessageFns<PrepareTreeAddressRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<PrepareTreeAddressResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly create_tree: {
            readonly name: "create_tree";
            readonly requestType: MessageFns<CreateTreeRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<CreateTreeResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly get_signing_operator_list: {
            readonly name: "get_signing_operator_list";
            readonly requestType: import("./google/protobuf/empty.js").MessageFns<Empty>;
            readonly requestStream: false;
            readonly responseType: MessageFns<GetSigningOperatorListResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly query_nodes: {
            readonly name: "query_nodes";
            readonly requestType: MessageFns<QueryNodesRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<QueryNodesResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly query_user_signed_refunds: {
            readonly name: "query_user_signed_refunds";
            readonly requestType: MessageFns<QueryUserSignedRefundsRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<QueryUserSignedRefundsResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        /** Token RPCs */
        readonly start_token_transaction: {
            readonly name: "start_token_transaction";
            readonly requestType: MessageFns<StartTokenTransactionRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<StartTokenTransactionResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly sign_token_transaction: {
            readonly name: "sign_token_transaction";
            readonly requestType: MessageFns<SignTokenTransactionRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<SignTokenTransactionResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly finalize_token_transaction: {
            readonly name: "finalize_token_transaction";
            readonly requestType: MessageFns<FinalizeTokenTransactionRequest>;
            readonly requestStream: false;
            readonly responseType: import("./google/protobuf/empty.js").MessageFns<Empty>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly freeze_tokens: {
            readonly name: "freeze_tokens";
            readonly requestType: MessageFns<FreezeTokensRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<FreezeTokensResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly get_owned_token_leaves: {
            readonly name: "get_owned_token_leaves";
            readonly requestType: MessageFns<GetOwnedTokenLeavesRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<GetOwnedTokenLeavesResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly return_lightning_payment: {
            readonly name: "return_lightning_payment";
            readonly requestType: MessageFns<ReturnLightningPaymentRequest>;
            readonly requestStream: false;
            readonly responseType: import("./google/protobuf/empty.js").MessageFns<Empty>;
            readonly responseStream: false;
            readonly options: {};
        };
        readonly cancel_send_transfer: {
            readonly name: "cancel_send_transfer";
            readonly requestType: MessageFns<CancelSendTransferRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<CancelSendTransferResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
    };
};
export interface SparkServiceImplementation<CallContextExt = {}> {
    generate_deposit_address(request: GenerateDepositAddressRequest, context: CallContext & CallContextExt): Promise<DeepPartial<GenerateDepositAddressResponse>>;
    start_tree_creation(request: StartTreeCreationRequest, context: CallContext & CallContextExt): Promise<DeepPartial<StartTreeCreationResponse>>;
    finalize_node_signatures(request: FinalizeNodeSignaturesRequest, context: CallContext & CallContextExt): Promise<DeepPartial<FinalizeNodeSignaturesResponse>>;
    start_send_transfer(request: StartSendTransferRequest, context: CallContext & CallContextExt): Promise<DeepPartial<StartSendTransferResponse>>;
    complete_send_transfer(request: CompleteSendTransferRequest, context: CallContext & CallContextExt): Promise<DeepPartial<CompleteSendTransferResponse>>;
    query_pending_transfers(request: QueryPendingTransfersRequest, context: CallContext & CallContextExt): Promise<DeepPartial<QueryPendingTransfersResponse>>;
    query_all_transfers(request: QueryAllTransfersRequest, context: CallContext & CallContextExt): Promise<DeepPartial<QueryAllTransfersResponse>>;
    claim_transfer_tweak_keys(request: ClaimTransferTweakKeysRequest, context: CallContext & CallContextExt): Promise<DeepPartial<Empty>>;
    claim_transfer_sign_refunds(request: ClaimTransferSignRefundsRequest, context: CallContext & CallContextExt): Promise<DeepPartial<ClaimTransferSignRefundsResponse>>;
    aggregate_nodes(request: AggregateNodesRequest, context: CallContext & CallContextExt): Promise<DeepPartial<AggregateNodesResponse>>;
    store_preimage_share(request: StorePreimageShareRequest, context: CallContext & CallContextExt): Promise<DeepPartial<Empty>>;
    get_signing_commitments(request: GetSigningCommitmentsRequest, context: CallContext & CallContextExt): Promise<DeepPartial<GetSigningCommitmentsResponse>>;
    cooperative_exit(request: CooperativeExitRequest, context: CallContext & CallContextExt): Promise<DeepPartial<CooperativeExitResponse>>;
    initiate_preimage_swap(request: InitiatePreimageSwapRequest, context: CallContext & CallContextExt): Promise<DeepPartial<InitiatePreimageSwapResponse>>;
    provide_preimage(request: ProvidePreimageRequest, context: CallContext & CallContextExt): Promise<DeepPartial<ProvidePreimageResponse>>;
    leaf_swap(request: LeafSwapRequest, context: CallContext & CallContextExt): Promise<DeepPartial<LeafSwapResponse>>;
    refresh_timelock(request: RefreshTimelockRequest, context: CallContext & CallContextExt): Promise<DeepPartial<RefreshTimelockResponse>>;
    extend_leaf(request: ExtendLeafRequest, context: CallContext & CallContextExt): Promise<DeepPartial<ExtendLeafResponse>>;
    prepare_tree_address(request: PrepareTreeAddressRequest, context: CallContext & CallContextExt): Promise<DeepPartial<PrepareTreeAddressResponse>>;
    create_tree(request: CreateTreeRequest, context: CallContext & CallContextExt): Promise<DeepPartial<CreateTreeResponse>>;
    get_signing_operator_list(request: Empty, context: CallContext & CallContextExt): Promise<DeepPartial<GetSigningOperatorListResponse>>;
    query_nodes(request: QueryNodesRequest, context: CallContext & CallContextExt): Promise<DeepPartial<QueryNodesResponse>>;
    query_user_signed_refunds(request: QueryUserSignedRefundsRequest, context: CallContext & CallContextExt): Promise<DeepPartial<QueryUserSignedRefundsResponse>>;
    /** Token RPCs */
    start_token_transaction(request: StartTokenTransactionRequest, context: CallContext & CallContextExt): Promise<DeepPartial<StartTokenTransactionResponse>>;
    sign_token_transaction(request: SignTokenTransactionRequest, context: CallContext & CallContextExt): Promise<DeepPartial<SignTokenTransactionResponse>>;
    finalize_token_transaction(request: FinalizeTokenTransactionRequest, context: CallContext & CallContextExt): Promise<DeepPartial<Empty>>;
    freeze_tokens(request: FreezeTokensRequest, context: CallContext & CallContextExt): Promise<DeepPartial<FreezeTokensResponse>>;
    get_owned_token_leaves(request: GetOwnedTokenLeavesRequest, context: CallContext & CallContextExt): Promise<DeepPartial<GetOwnedTokenLeavesResponse>>;
    return_lightning_payment(request: ReturnLightningPaymentRequest, context: CallContext & CallContextExt): Promise<DeepPartial<Empty>>;
    cancel_send_transfer(request: CancelSendTransferRequest, context: CallContext & CallContextExt): Promise<DeepPartial<CancelSendTransferResponse>>;
}
export interface SparkServiceClient<CallOptionsExt = {}> {
    generate_deposit_address(request: DeepPartial<GenerateDepositAddressRequest>, options?: CallOptions & CallOptionsExt): Promise<GenerateDepositAddressResponse>;
    start_tree_creation(request: DeepPartial<StartTreeCreationRequest>, options?: CallOptions & CallOptionsExt): Promise<StartTreeCreationResponse>;
    finalize_node_signatures(request: DeepPartial<FinalizeNodeSignaturesRequest>, options?: CallOptions & CallOptionsExt): Promise<FinalizeNodeSignaturesResponse>;
    start_send_transfer(request: DeepPartial<StartSendTransferRequest>, options?: CallOptions & CallOptionsExt): Promise<StartSendTransferResponse>;
    complete_send_transfer(request: DeepPartial<CompleteSendTransferRequest>, options?: CallOptions & CallOptionsExt): Promise<CompleteSendTransferResponse>;
    query_pending_transfers(request: DeepPartial<QueryPendingTransfersRequest>, options?: CallOptions & CallOptionsExt): Promise<QueryPendingTransfersResponse>;
    query_all_transfers(request: DeepPartial<QueryAllTransfersRequest>, options?: CallOptions & CallOptionsExt): Promise<QueryAllTransfersResponse>;
    claim_transfer_tweak_keys(request: DeepPartial<ClaimTransferTweakKeysRequest>, options?: CallOptions & CallOptionsExt): Promise<Empty>;
    claim_transfer_sign_refunds(request: DeepPartial<ClaimTransferSignRefundsRequest>, options?: CallOptions & CallOptionsExt): Promise<ClaimTransferSignRefundsResponse>;
    aggregate_nodes(request: DeepPartial<AggregateNodesRequest>, options?: CallOptions & CallOptionsExt): Promise<AggregateNodesResponse>;
    store_preimage_share(request: DeepPartial<StorePreimageShareRequest>, options?: CallOptions & CallOptionsExt): Promise<Empty>;
    get_signing_commitments(request: DeepPartial<GetSigningCommitmentsRequest>, options?: CallOptions & CallOptionsExt): Promise<GetSigningCommitmentsResponse>;
    cooperative_exit(request: DeepPartial<CooperativeExitRequest>, options?: CallOptions & CallOptionsExt): Promise<CooperativeExitResponse>;
    initiate_preimage_swap(request: DeepPartial<InitiatePreimageSwapRequest>, options?: CallOptions & CallOptionsExt): Promise<InitiatePreimageSwapResponse>;
    provide_preimage(request: DeepPartial<ProvidePreimageRequest>, options?: CallOptions & CallOptionsExt): Promise<ProvidePreimageResponse>;
    leaf_swap(request: DeepPartial<LeafSwapRequest>, options?: CallOptions & CallOptionsExt): Promise<LeafSwapResponse>;
    refresh_timelock(request: DeepPartial<RefreshTimelockRequest>, options?: CallOptions & CallOptionsExt): Promise<RefreshTimelockResponse>;
    extend_leaf(request: DeepPartial<ExtendLeafRequest>, options?: CallOptions & CallOptionsExt): Promise<ExtendLeafResponse>;
    prepare_tree_address(request: DeepPartial<PrepareTreeAddressRequest>, options?: CallOptions & CallOptionsExt): Promise<PrepareTreeAddressResponse>;
    create_tree(request: DeepPartial<CreateTreeRequest>, options?: CallOptions & CallOptionsExt): Promise<CreateTreeResponse>;
    get_signing_operator_list(request: DeepPartial<Empty>, options?: CallOptions & CallOptionsExt): Promise<GetSigningOperatorListResponse>;
    query_nodes(request: DeepPartial<QueryNodesRequest>, options?: CallOptions & CallOptionsExt): Promise<QueryNodesResponse>;
    query_user_signed_refunds(request: DeepPartial<QueryUserSignedRefundsRequest>, options?: CallOptions & CallOptionsExt): Promise<QueryUserSignedRefundsResponse>;
    /** Token RPCs */
    start_token_transaction(request: DeepPartial<StartTokenTransactionRequest>, options?: CallOptions & CallOptionsExt): Promise<StartTokenTransactionResponse>;
    sign_token_transaction(request: DeepPartial<SignTokenTransactionRequest>, options?: CallOptions & CallOptionsExt): Promise<SignTokenTransactionResponse>;
    finalize_token_transaction(request: DeepPartial<FinalizeTokenTransactionRequest>, options?: CallOptions & CallOptionsExt): Promise<Empty>;
    freeze_tokens(request: DeepPartial<FreezeTokensRequest>, options?: CallOptions & CallOptionsExt): Promise<FreezeTokensResponse>;
    get_owned_token_leaves(request: DeepPartial<GetOwnedTokenLeavesRequest>, options?: CallOptions & CallOptionsExt): Promise<GetOwnedTokenLeavesResponse>;
    return_lightning_payment(request: DeepPartial<ReturnLightningPaymentRequest>, options?: CallOptions & CallOptionsExt): Promise<Empty>;
    cancel_send_transfer(request: DeepPartial<CancelSendTransferRequest>, options?: CallOptions & CallOptionsExt): Promise<CancelSendTransferResponse>;
}
type Builtin = Date | Function | Uint8Array | string | number | boolean | undefined;
export type DeepPartial<T> = T extends Builtin ? T : T extends globalThis.Array<infer U> ? globalThis.Array<DeepPartial<U>> : T extends ReadonlyArray<infer U> ? ReadonlyArray<DeepPartial<U>> : T extends {
    $case: string;
} ? {
    [K in keyof Omit<T, "$case">]?: DeepPartial<T[K]>;
} & {
    $case: T["$case"];
} : T extends {} ? {
    [K in keyof T]?: DeepPartial<T[K]>;
} : Partial<T>;
export interface MessageFns<T> {
    encode(message: T, writer?: BinaryWriter): BinaryWriter;
    decode(input: BinaryReader | Uint8Array, length?: number): T;
    fromJSON(object: any): T;
    toJSON(message: T): unknown;
    create(base?: DeepPartial<T>): T;
    fromPartial(object: DeepPartial<T>): T;
}
export {};
