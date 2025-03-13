import { LRCWallet } from "@buildonspark/lrc20-sdk";
import {
  CreateLightningInvoiceParams,
  PayLightningInvoiceParams,
} from "@buildonspark/spark-sdk";
import {
  LeafWithPreviousTransactionData,
  QueryAllTransfersResponse,
  Transfer,
} from "@buildonspark/spark-sdk/proto/spark";

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
  refreshTimelockNodes(nodeId?: string): Promise<void>;
  createLightningInvoice(params: CreateLightningInvoiceParams): Promise<
    | string
    | {
        invoice: string;
        paymentHash: string;
      }
  >;
  payLightningInvoice(params: PayLightningInvoiceParams): Promise<any>;
  withdraw(params: {
    onchainAddress: string;
    targetAmountSats?: number;
  }): Promise<any>;
  transferTokens(params: {
    tokenPublicKey: string;
    tokenAmount: bigint;
    receiverSparkAddress: string;
    selectedLeaves?: LeafWithPreviousTransactionData[];
  }): Promise<string>;

  // IssuerSparkWallet methods
  getIssuerTokenBalance(): Promise<{
    balance: bigint;
  }>;
  mintTokens(tokenAmount: bigint): Promise<string>;
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
