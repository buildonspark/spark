import { LRCWallet, Lrc20Protos } from "@buildonspark/lrc20-sdk";
import {
  CreateLightningInvoiceParams,
  PayLightningInvoiceParams,
  TokenInfo,
} from "@buildonspark/spark-sdk";
import {
  LeafWithPreviousTransactionData,
  QueryAllTransfersResponse,
  TokenTransactionWithStatus,
  Transfer,
} from "@buildonspark/spark-sdk/proto/spark";
import { LightningReceiveRequest } from "@buildonspark/spark-sdk/types";
import { GetTokenActivityResponse, TokenPubKeyInfoResponse } from "../types.js";

/**
 * Interface for the IssuerSparkWallet that includes all functions from both SparkWallet and IssuerSparkWallet
 */
export interface IssuerWalletInterface {
  // SparkWallet methods
  getIdentityPublicKey(): Promise<string>;
  getSparkAddress(): Promise<string>;
  getTransfers(
    limit?: number,
    offset?: number,
  ): Promise<QueryAllTransfersResponse>;
  getBalance(forceRefetch?: boolean): Promise<{
    balance: bigint;
    tokenBalances: Map<string, { balance: bigint }>;
  }>;
  getDepositAddress(): Promise<string>;
  transfer(params: {
    receiverSparkAddress: string;
    amountSats: number;
  }): Promise<Transfer | string>;
  createLightningInvoice(
    params: CreateLightningInvoiceParams,
  ): Promise<LightningReceiveRequest>;
  payLightningInvoice(params: PayLightningInvoiceParams): Promise<any>;
  withdraw(params: {
    onchainAddress: string;
    amountSats?: number;
  }): Promise<any>;
  transferTokens(params: {
    tokenPublicKey: string;
    tokenAmount: bigint;
    receiverSparkAddress: string;
    selectedLeaves?: LeafWithPreviousTransactionData[];
  }): Promise<string>;
  // IssuerSparkWallet methods
  getIssuerTokenPublicKey(): Promise<string>;
  getIssuerTokenBalance(): Promise<{
    balance: bigint;
  }>;
  mintTokens(tokenAmount: bigint): Promise<string>;
  getTokenActivity(
    pageSize: number,
    cursor?: Lrc20Protos.ListAllTokenTransactionsCursor,
    operationTypes?: Lrc20Protos.OperationType[],
    beforeTimestamp?: Date,
  ): Promise<GetTokenActivityResponse>;
  getIssuerTokenActivity(
    pageSize: number,
    cursor?: Lrc20Protos.ListAllTokenTransactionsCursor,
    operationTypes?: Lrc20Protos.OperationType[],
    beforeTimestamp?: Date,
    afterTimestamp?: Date,
  ): Promise<GetTokenActivityResponse>;
  getTokenTransactions(
    tokenPublicKeys: string[],
    tokenTransactionHashes?: string[],
  ): Promise<TokenTransactionWithStatus[]>;

  getTokenInfo(): Promise<TokenInfo[]>;
  getIssuerTokenInfo(): Promise<TokenPubKeyInfoResponse | null>;
  burnTokens(
    tokenAmount: bigint,
    selectedLeaves?: LeafWithPreviousTransactionData[],
  ): Promise<string>;
  freezeTokens(
    ownerPublicKey: string,
  ): Promise<{ impactedLeafIds: string[]; impactedTokenAmount: bigint }>;
  unfreezeTokens(
    ownerPublicKey: string,
  ): Promise<{ impactedLeafIds: string[]; impactedTokenAmount: bigint }>;
  getTokenL1Address(): string;
  announceTokenL1(params: {
    lrc20Wallet: LRCWallet;
    tokenName: string;
    tokenTicker: string;
    decimals: number;
    maxSupply: bigint;
    isFreezable: boolean;
    feeRateSatsPerVb?: number;
  }): Promise<string>;
  mintTokensL1(tokenAmount: bigint): Promise<string>;
  transferTokensL1(tokenAmount: bigint, p2trAddress: string): Promise<string>;
}
