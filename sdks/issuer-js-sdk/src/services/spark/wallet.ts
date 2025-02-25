import {
  bytesToNumberBE,
  hexToBytes,
} from "@noble/curves/abstract/utils";
import { SparkWallet } from "@buildonspark/spark-js-sdk";
import { SparkSigner } from "@buildonspark/spark-js-sdk/signer";
import { LeafWithPreviousTransactionData } from "../../proto/spark.js";
import { Network } from "@buildonspark/spark-js-sdk/utils";
import { checkIfSelectedLeavesAreAvailable } from "@buildonspark/spark-js-sdk/utils";
import { IssuerTokenTransactionService } from "./token-transactions.js";
import { TokenFreezeService } from "../freeze.js";

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
    await super.transferTokens(
      tokenPublicKey,
      tokenAmount,
      recipientPublicKey
    );
  }

  async consolidateIssuerTokenLeaves() {
    const tokenPublicKey = await super.getIdentityPublicKey();
    await super.consolidateTokenLeaves(tokenPublicKey);
  }

  // TODO: Simplify so less logic is in the Issuer JS SDK in favor of the Spark
  // SDK logic.
  async burnIssuerTokens(
    tokenAmount: bigint,
    selectedLeaves?: LeafWithPreviousTransactionData[]
  ) {
    await this.syncTokenLeaves();
    const tokenPublicKey = await super.getIdentityPublicKey();

    if (!this.tokenLeaves.has(tokenPublicKey)) {
      throw new Error("No token leaves available to burn");
    }

    if (selectedLeaves) {
      if (
        !checkIfSelectedLeavesAreAvailable(
          selectedLeaves,
          this.tokenLeaves,
          hexToBytes(tokenPublicKey)
        )
      ) {
        throw new Error("One or more selected leaves are not available");
      }
    } else {
      selectedLeaves = this.selectTokenLeaves(
        tokenPublicKey,
        tokenAmount
      );
    }

    const partialTokenTransaction =
      await this.issuerTokenTransactionService.constructBurnTokenTransaction(
        selectedLeaves,
        hexToBytes(tokenPublicKey),
        tokenAmount
      );

    const finalizedTokenTransaction =
      await this.issuerTokenTransactionService.broadcastTokenTransaction(
        partialTokenTransaction,
        selectedLeaves.map((leaf) => leaf.leaf!.ownerPublicKey),
        selectedLeaves.map((leaf) => leaf.leaf!.revocationPublicKey!)
      );

    const tokenPubKeyHex = tokenPublicKey;
    if (!this.tokenLeaves.has(tokenPubKeyHex)) {
      this.tokenLeaves.set(tokenPubKeyHex, []);
    }
    this.issuerTokenTransactionService.updateTokenLeavesFromFinalizedTransaction(
      this.tokenLeaves.get(tokenPubKeyHex)!,
      finalizedTokenTransaction
    );
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
