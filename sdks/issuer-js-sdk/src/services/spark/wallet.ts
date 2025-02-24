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
      this.connectionManager
    );
    this.tokenFreezeService = new TokenFreezeService(
      this.config,
      this.connectionManager
    );
  }

  async mintIssuerTokens(tokenAmount: bigint) {
    var tokenPublicKey = await super.getSigner().getIdentityPublicKey();

    const tokenTransaction =
      await this.issuerTokenTransactionService.constructMintTokenTransaction(
        tokenPublicKey,
        tokenAmount
      );

    const finalizedTokenTransaction =
      await this.issuerTokenTransactionService.broadcastTokenTransaction(
        tokenTransaction
      );

    const tokenPubKeyHex = bytesToHex(tokenPublicKey);
    if (!this.tokenLeaves.has(tokenPubKeyHex)) {
      this.tokenLeaves.set(tokenPubKeyHex, []);
    }
    this.issuerTokenTransactionService.updateTokenLeavesFromFinalizedTransaction(
      this.tokenLeaves.get(tokenPubKeyHex)!,
      finalizedTokenTransaction
    );
  }

  async transferIssuerTokens(tokenAmount: bigint, recipientPublicKey: string) {
    var tokenPublicKey = await super.getSigner().getIdentityPublicKey();
    await super.transferTokens(
      bytesToHex(tokenPublicKey),
      tokenAmount,
      recipientPublicKey
    );
  }

  async consolidateIssuerTokenLeaves() {
    var tokenPublicKey = await super.getSigner().getIdentityPublicKey();
    await super.consolidateTokenLeaves(bytesToHex(tokenPublicKey));
  }

  // TODO: Simplify so less logic is in the Issuer JS SDK in favor of the Spark
  // SDK logic.
  async burnIssuerTokens(
    tokenAmount: bigint,
    selectedLeaves?: LeafWithPreviousTransactionData[]
  ) {
    var tokenPublicKey = await super.getSigner().getIdentityPublicKey();

    if (!this.tokenLeaves.has(bytesToHex(tokenPublicKey))) {
      throw new Error("No token leaves available to burn");
    }

    if (selectedLeaves) {
      if (
        !checkIfSelectedLeavesAreAvailable(
          selectedLeaves,
          this.tokenLeaves,
          tokenPublicKey
        )
      ) {
        throw new Error("One or more selected leaves are not available");
      }
    } else {
      selectedLeaves = this.selectTokenLeaves(bytesToHex(tokenPublicKey), tokenAmount);
    }

    const partialTokenTransaction =
      await this.issuerTokenTransactionService.constructBurnTokenTransaction(
        selectedLeaves,
        tokenPublicKey,
        tokenAmount
      );

    const finalizedTokenTransaction =
      await this.issuerTokenTransactionService.broadcastTokenTransaction(
        partialTokenTransaction,
        selectedLeaves.map((leaf) => leaf.leaf!.ownerPublicKey),
        selectedLeaves.map((leaf) => leaf.leaf!.revocationPublicKey!)
      );

    const tokenPubKeyHex = bytesToHex(tokenPublicKey);
    if (!this.tokenLeaves.has(tokenPubKeyHex)) {
      this.tokenLeaves.set(tokenPubKeyHex, []);
    }
    this.issuerTokenTransactionService.updateTokenLeavesFromFinalizedTransaction(
      this.tokenLeaves.get(tokenPubKeyHex)!,
      finalizedTokenTransaction
    );
  }

  async freezeIssuerTokens(ownerPublicKey: Uint8Array) {
    var tokenPublicKey = await super.getSigner().getIdentityPublicKey();

    await this.tokenFreezeService!.freezeTokens(ownerPublicKey, tokenPublicKey);
  }

  async unfreezeIssuerTokens(ownerPublicKey: Uint8Array) {
    var tokenPublicKey = await super.getSigner().getIdentityPublicKey();

    await this.tokenFreezeService!.unfreezeTokens(
      ownerPublicKey,
      tokenPublicKey
    );
  }
}
