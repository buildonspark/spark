import { bytesToHex } from "@noble/curves/utils";
import ClaimStaticDeposit from "../graphql/objects/ClaimStaticDeposit.js";
import CoopExitRequest from "../graphql/objects/CoopExitRequest.js";
import LeavesSwapRequest from "../graphql/objects/LeavesSwapRequest.js";
import LightningReceiveRequest from "../graphql/objects/LightningReceiveRequest.js";
import LightningSendRequest from "../graphql/objects/LightningSendRequest.js";
import {
  Network,
  SigningKeyshare,
  Transfer,
  TransferLeaf,
  TransferStatus,
  TransferType,
  TreeNode,
} from "../proto/spark.js";

export interface WalletLeaf {
  id: string;
  treeId: string;
  value: number;
  parentNodeId?: string | undefined;
  nodeTx: string;
  refundTx: string;
  vout: number;
  verifyingPublicKey: string;
  ownerIdentityPublicKey: string;
  signingKeyshare: SigningKeyshare | undefined;
  status: string;
  network: keyof typeof Network;
}

export function mapTreeNodeToWalletLeaf(proto: TreeNode): WalletLeaf {
  return {
    id: proto.id,
    treeId: proto.treeId,
    value: proto.value,
    parentNodeId: proto.parentNodeId,
    nodeTx: bytesToHex(proto.nodeTx),
    refundTx: bytesToHex(proto.refundTx),
    vout: proto.vout,
    verifyingPublicKey: bytesToHex(proto.verifyingPublicKey),
    ownerIdentityPublicKey: bytesToHex(proto.ownerIdentityPublicKey),
    signingKeyshare: proto.signingKeyshare,
    status: proto.status,
    network: Network[proto.network] as keyof typeof Network,
  };
}

export enum TransferDirection {
  INCOMING = "INCOMING",
  OUTGOING = "OUTGOING",
}

export type UserRequestType =
  | LightningSendRequest
  | LightningReceiveRequest
  | LeavesSwapRequest
  | CoopExitRequest
  | ClaimStaticDeposit;

export interface WalletTransfer {
  id: string;
  senderIdentityPublicKey: string;
  receiverIdentityPublicKey: string;
  status: keyof typeof TransferStatus;
  totalValue: number;
  expiryTime: Date | undefined;
  leaves: WalletTransferLeaf[];
  createdTime: Date | undefined;
  updatedTime: Date | undefined;
  type: keyof typeof TransferType;
  transferDirection: keyof typeof TransferDirection;
  userRequest: Omit<UserRequestType, "transfer"> | undefined;
}

export interface WalletTransferLeaf {
  leaf: WalletLeaf | undefined;
  secretCipher: string;
  signature: string;
  intermediateRefundTx: string;
}

export function mapTransferLeafToWalletTransferLeaf(
  proto: TransferLeaf,
): WalletTransferLeaf {
  return {
    leaf: proto.leaf ? mapTreeNodeToWalletLeaf(proto.leaf) : undefined,
    secretCipher: bytesToHex(proto.secretCipher),
    signature: bytesToHex(proto.signature),
    intermediateRefundTx: bytesToHex(proto.intermediateRefundTx),
  };
}

export function mapTransferToWalletTransfer(
  proto: Transfer,
  identityPublicKey: string,
  userRequest?: Omit<UserRequestType, "transfer">,
): WalletTransfer {
  const receiverIdentityPublicKey = bytesToHex(proto.receiverIdentityPublicKey);
  const senderIdentityPublicKey = bytesToHex(proto.senderIdentityPublicKey);
  return {
    id: proto.id,
    senderIdentityPublicKey: senderIdentityPublicKey,
    receiverIdentityPublicKey: receiverIdentityPublicKey,
    status: TransferStatus[proto.status] as keyof typeof TransferStatus,
    totalValue: proto.totalValue,
    expiryTime: proto.expiryTime ? new Date(proto.expiryTime) : undefined,
    leaves: proto.leaves.map(mapTransferLeafToWalletTransferLeaf),
    createdTime: proto.createdTime ? new Date(proto.createdTime) : undefined,
    updatedTime: proto.updatedTime ? new Date(proto.updatedTime) : undefined,
    type: TransferType[proto.type] as keyof typeof TransferType,
    transferDirection:
      receiverIdentityPublicKey === identityPublicKey
        ? TransferDirection.INCOMING
        : TransferDirection.OUTGOING,
    userRequest: userRequest,
  };
}
