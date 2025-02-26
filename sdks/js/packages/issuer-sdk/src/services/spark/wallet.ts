import { bytesToNumberBE, hexToBytes } from "@noble/curves/abstract/utils";
import { SparkWallet } from "@buildonspark/spark-sdk";
import { SparkSigner } from "@buildonspark/spark-sdk/signer";
import { LeafWithPreviousTransactionData } from "@buildonspark/spark-sdk/proto/spark";
import { Network } from "@buildonspark/spark-sdk/utils";
import { IssuerTokenTransactionService } from "./token-transactions.js";
import { TokenFreezeService } from "../freeze.js";

const BURN_ADDRESS =
  "020202020202020202020202020202020202020202020202020202020202020202";

export class IssuerSparkWallet extends SparkWallet {
  private issuerTokenTransactionService: IssuerTokenTransactionService;
  private tokenFreezeService: TokenFreezeService;

  constructor(network: Network, signer?: SparkSigner) {
    super(network, signer);

    this.issuerTokenTransactionService = new IssuerTokenTransactionService(
      this.config,
      this.connectionManager,
    );
    this.tokenFreezeService = new TokenFreezeService(
      this.config,
      this.connectionManager,
    );
  }

  async getIssuerTokenBalance(): Promise<{
    balance: bigint;
    leafCount: number;
  }> {
    const publicKey = await super.getIdentityPublicKey();
    const balanceObj = await this.getBalance();
    if (!balanceObj.tokenBalances || !balanceObj.tokenBalances.has(publicKey)) {
      return {
        balance: 0n,
        leafCount: 0,
      };
    }
    return {
      balance: balanceObj.tokenBalances.get(publicKey)!.balance,
      leafCount: balanceObj.tokenBalances.get(publicKey)!.leafCount,
    };
  }

  async mintIssuerTokens(tokenAmount: bigint): Promise<string> {
    var tokenPublicKey = await super.getIdentityPublicKey();

    const tokenTransaction =
      await this.issuerTokenTransactionService.constructMintTokenTransaction(
        hexToBytes(tokenPublicKey),
        tokenAmount,
      );

    return await this.issuerTokenTransactionService.broadcastTokenTransaction(
      tokenTransaction,
    );
  }

  async transferIssuerTokens(
    tokenAmount: bigint,
    recipientPublicKey: string,
  ): Promise<string> {
    const tokenPublicKey = await super.getIdentityPublicKey();
    return await super.transferTokens(
      tokenPublicKey,
      tokenAmount,
      recipientPublicKey,
    );
  }

  async burnIssuerTokens(
    tokenAmount: bigint,
    selectedLeaves?: LeafWithPreviousTransactionData[],
  ): Promise<string> {
    return await this.transferTokens(
      await super.getIdentityPublicKey(),
      tokenAmount,
      BURN_ADDRESS,
      selectedLeaves,
    );
  }

  async freezeIssuerTokens(
    ownerPublicKey: string,
  ): Promise<{ impactedLeafIds: string[]; impactedTokenAmount: bigint }> {
    await this.syncTokenLeaves();
    const tokenPublicKey = await super.getIdentityPublicKey();

    const response = await this.tokenFreezeService!.freezeTokens(
      hexToBytes(ownerPublicKey),
      hexToBytes(tokenPublicKey),
    );

    // Convert the Uint8Array to a bigint
    const tokenAmount = bytesToNumberBE(response.impactedTokenAmount);

    return {
      impactedLeafIds: response.impactedLeafIds,
      impactedTokenAmount: tokenAmount,
    };
  }

  async unfreezeIssuerTokens(
    ownerPublicKey: string,
  ): Promise<{ impactedLeafIds: string[]; impactedTokenAmount: bigint }> {
    await this.syncTokenLeaves();
    const tokenPublicKey = await super.getIdentityPublicKey();

    const response = await this.tokenFreezeService!.unfreezeTokens(
      hexToBytes(ownerPublicKey),
      hexToBytes(tokenPublicKey),
    );
    const tokenAmount = bytesToNumberBE(response.impactedTokenAmount);

    return {
      impactedLeafIds: response.impactedLeafIds,
      impactedTokenAmount: tokenAmount,
    };
  }
}
