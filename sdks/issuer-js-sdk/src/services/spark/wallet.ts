import { bytesToHex } from "@noble/curves/abstract/utils";
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
      this.connectionManager,
    );
    this.tokenFreezeService = new TokenFreezeService(
      this.config,
      this.connectionManager,
    );
  }

  async mintTokens(tokenPublicKey: Uint8Array, tokenAmount: bigint) {
    const tokenTransaction =
      await this.issuerTokenTransactionService.constructMintTokenTransaction(
        tokenPublicKey,
        tokenAmount,
      );

    const finalizedTokenTransaction =
      await this.issuerTokenTransactionService.broadcastTokenTransaction(
        tokenTransaction,
      );

    const tokenPubKeyHex = bytesToHex(tokenPublicKey);
    if (!this.tokenLeaves.has(tokenPubKeyHex)) {
      this.tokenLeaves.set(tokenPubKeyHex, []);
    }
    this.issuerTokenTransactionService.updateTokenLeavesFromFinalizedTransaction(
      this.tokenLeaves.get(tokenPubKeyHex)!,
      finalizedTokenTransaction,
    );
  }

  async burnTokens(
    tokenPublicKey: Uint8Array,
    tokenAmount: bigint,
    selectedLeaves?: LeafWithPreviousTransactionData[],
  ) {
    if (!this.tokenLeaves.has(bytesToHex(tokenPublicKey))) {
      throw new Error("No token leaves with the given tokenPublicKey");
    }

    if (selectedLeaves) {
      if (
        !checkIfSelectedLeavesAreAvailable(
          selectedLeaves,
          this.tokenLeaves,
          tokenPublicKey,
        )
      ) {
        throw new Error("One or more selected leaves are not available");
      }
    } else {
      selectedLeaves = this.selectTokenLeaves(tokenPublicKey, tokenAmount);
    }

    const partialTokenTransaction =
      await this.issuerTokenTransactionService.constructBurnTokenTransaction(
        selectedLeaves,
        tokenPublicKey,
        tokenAmount,
      );

    const finalizedTokenTransaction =
      await this.issuerTokenTransactionService.broadcastTokenTransaction(
        partialTokenTransaction,
        selectedLeaves.map((leaf) => leaf.leaf!.ownerPublicKey),
        selectedLeaves.map((leaf) => leaf.leaf!.revocationPublicKey!),
      );

    const tokenPubKeyHex = bytesToHex(tokenPublicKey);
    if (!this.tokenLeaves.has(tokenPubKeyHex)) {
      this.tokenLeaves.set(tokenPubKeyHex, []);
    }
    this.issuerTokenTransactionService.updateTokenLeavesFromFinalizedTransaction(
      this.tokenLeaves.get(tokenPubKeyHex)!,
      finalizedTokenTransaction,
    );
  }

  async freezeTokens(ownerPublicKey: Uint8Array, tokenPublicKey: Uint8Array) {
    await this.tokenFreezeService!.freezeTokens(ownerPublicKey, tokenPublicKey);
  }

  async unfreezeTokens(ownerPublicKey: Uint8Array, tokenPublicKey: Uint8Array) {
    await this.tokenFreezeService!.unfreezeTokens(
      ownerPublicKey,
      tokenPublicKey,
    );
  }
}
