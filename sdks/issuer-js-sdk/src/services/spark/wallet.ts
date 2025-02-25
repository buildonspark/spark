import { bytesToNumberBE, hexToBytes } from "@noble/curves/abstract/utils";
import { SparkWallet } from "@buildonspark/spark-js-sdk";
import { SparkSigner } from "@buildonspark/spark-js-sdk/signer";
import { LeafWithPreviousTransactionData } from "../../proto/spark.js";
import { Network } from "@buildonspark/spark-js-sdk/utils";
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
      this.connectionManager
    );
    this.tokenFreezeService = new TokenFreezeService(
      this.config,
      this.connectionManager
    );
  }

  async mintIssuerTokens(tokenAmount: bigint) {
    var tokenPublicKey = await super.getIdentityPublicKey();

    const tokenTransaction =
      await this.issuerTokenTransactionService.constructMintTokenTransaction(
        hexToBytes(tokenPublicKey),
        tokenAmount
      );

    const finalizedTokenTransaction =
      await this.issuerTokenTransactionService.broadcastTokenTransaction(
        tokenTransaction
      );

    if (!this.tokenLeaves.has(tokenPublicKey)) {
      this.tokenLeaves.set(tokenPublicKey, []);
    }
    this.issuerTokenTransactionService.updateTokenLeavesFromFinalizedTransaction(
      this.tokenLeaves.get(tokenPublicKey)!,
      finalizedTokenTransaction
    );
  }

  async transferIssuerTokens(tokenAmount: bigint, recipientPublicKey: string) {
    const tokenPublicKey = await super.getIdentityPublicKey();
    await super.transferTokens(tokenPublicKey, tokenAmount, recipientPublicKey);
  }

  async consolidateIssuerTokenLeaves() {
    const tokenPublicKey = await super.getIdentityPublicKey();
    await super.consolidateTokenLeaves(tokenPublicKey);
  }

  async burnIssuerTokens(
    tokenAmount: bigint,
    selectedLeaves?: LeafWithPreviousTransactionData[]
  ) {
    await this.transferTokens(await this.getIdentityPublicKey(), tokenAmount, BURN_ADDRESS, selectedLeaves);
  }

  async freezeIssuerTokens(ownerPublicKey: string) {
    await this.syncTokenLeaves();
    const tokenPublicKey = await super.getIdentityPublicKey();

    const response = await this.tokenFreezeService!.freezeTokens(
      hexToBytes(ownerPublicKey),
      hexToBytes(tokenPublicKey)
    );

    // Convert the Uint8Array to a bigint
    const tokenAmount = bytesToNumberBE(response.impactedTokenAmount);

    return {
      impactedLeafIds: response.impactedLeafIds,
      impactedTokenAmount: tokenAmount,
    };
  }

  async unfreezeIssuerTokens(ownerPublicKey: string) {
    await this.syncTokenLeaves();
    const tokenPublicKey = await super.getIdentityPublicKey();

    const response = await this.tokenFreezeService!.unfreezeTokens(
      hexToBytes(ownerPublicKey),
      hexToBytes(tokenPublicKey)
    );
    const tokenAmount = bytesToNumberBE(response.impactedTokenAmount);

    return {
      impactedLeafIds: response.impactedLeafIds,
      impactedTokenAmount: tokenAmount,
    };
  }
}
