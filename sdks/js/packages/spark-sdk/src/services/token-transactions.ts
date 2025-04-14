import {
  bytesToHex,
  bytesToNumberBE,
  numberToBytesBE,
} from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import {
  LeafWithPreviousTransactionData,
  OperatorSpecificTokenTransactionSignablePayload,
  OperatorSpecificTokenTransactionSignature,
  TokenTransaction,
} from "../proto/spark.js";
import { SparkCallOptions } from "../types/grpc.js";
import { validateResponses } from "../utils/response-validation.js";
import {
  hashOperatorSpecificTokenTransactionSignablePayload,
  hashTokenTransaction,
} from "../utils/token-hashing.js";
import {
  KeyshareWithOperatorIndex,
  recoverPrivateKeyFromKeyshares,
} from "../utils/token-keyshares.js";
import { calculateAvailableTokenAmount } from "../utils/token-transactions.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection.js";

export class TokenTransactionService {
  protected readonly config: WalletConfigService;
  protected readonly connectionManager: ConnectionManager;

  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager,
  ) {
    this.config = config;
    this.connectionManager = connectionManager;
  }

  public async constructTransferTokenTransaction(
    selectedLeaves: LeafWithPreviousTransactionData[],
    receiverSparkAddress: Uint8Array,
    tokenPublicKey: Uint8Array,
    tokenAmount: bigint,
  ): Promise<TokenTransaction> {
    let availableTokenAmount = calculateAvailableTokenAmount(selectedLeaves);

    if (availableTokenAmount === tokenAmount) {
      return {
        network: this.config.getNetworkProto(),
        tokenInput: {
          $case: "transferInput",
          transferInput: {
            leavesToSpend: selectedLeaves.map((leaf) => ({
              prevTokenTransactionHash: leaf.previousTransactionHash,
              prevTokenTransactionLeafVout: leaf.previousTransactionVout,
            })),
          },
        },
        outputLeaves: [
          {
            ownerPublicKey: receiverSparkAddress,
            tokenPublicKey: tokenPublicKey,
            tokenAmount: numberToBytesBE(tokenAmount, 16),
          },
        ],
        sparkOperatorIdentityPublicKeys:
          this.collectOperatorIdentityPublicKeys(),
      };
    } else {
      const tokenAmountDifference = availableTokenAmount - tokenAmount;

      return {
        network: this.config.getNetworkProto(),
        tokenInput: {
          $case: "transferInput",
          transferInput: {
            leavesToSpend: selectedLeaves.map((leaf) => ({
              prevTokenTransactionHash: leaf.previousTransactionHash,
              prevTokenTransactionLeafVout: leaf.previousTransactionVout,
            })),
          },
        },
        outputLeaves: [
          {
            ownerPublicKey: receiverSparkAddress,
            tokenPublicKey: tokenPublicKey,
            tokenAmount: numberToBytesBE(tokenAmount, 16),
          },
          {
            ownerPublicKey: await this.config.signer.getIdentityPublicKey(),
            tokenPublicKey: tokenPublicKey,
            tokenAmount: numberToBytesBE(tokenAmountDifference, 16),
          },
        ],
        sparkOperatorIdentityPublicKeys:
          this.collectOperatorIdentityPublicKeys(),
      };
    }
  }

  public collectOperatorIdentityPublicKeys(): Uint8Array[] {
    const operatorKeys: Uint8Array[] = [];
    for (const [_, operator] of Object.entries(
      this.config.getSigningOperators(),
    )) {
      operatorKeys.push(operator.identityPublicKey);
    }

    return operatorKeys;
  }

  public async broadcastTokenTransaction(
    tokenTransaction: TokenTransaction,
    leafToSpendSigningPublicKeys?: Uint8Array[],
    leafToSpendRevocationPublicKeys?: Uint8Array[],
  ): Promise<string> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    const signingOperators = this.config.getSigningOperators();

    const partialTokenTransactionHash = hashTokenTransaction(
      tokenTransaction,
      true,
    );

    const ownerSignatures: Uint8Array[] = [];
    if (tokenTransaction.tokenInput!.$case === "mintInput") {
      const issuerPublicKey =
        tokenTransaction.tokenInput!.mintInput.issuerPublicKey;
      if (!issuerPublicKey) {
        throw new Error("issuer public key cannot be nil");
      }

      const ownerSignature = await this.signMessageWithKey(
        partialTokenTransactionHash,
        issuerPublicKey,
      );

      ownerSignatures.push(ownerSignature);
    } else if (tokenTransaction.tokenInput!.$case === "transferInput") {
      const transferInput = tokenTransaction.tokenInput!.transferInput;

      if (!leafToSpendSigningPublicKeys || !leafToSpendRevocationPublicKeys) {
        throw new Error(
          "leafToSpendSigningPublicKeys and leafToSpendRevocationPublicKeys are required",
        );
      }

      for (let i = 0; i < transferInput.leavesToSpend.length; i++) {
        const key = leafToSpendSigningPublicKeys![i];
        if (!key) {
          throw new Error("key not found");
        }
        const ownerSignature = await this.signMessageWithKey(
          partialTokenTransactionHash,
          key,
        );

        ownerSignatures.push(ownerSignature);
      }
    }

    // Start the token transaction
    const startResponse = await sparkClient.start_token_transaction({
      identityPublicKey: await this.config.signer.getIdentityPublicKey(),
      partialTokenTransaction: tokenTransaction,
      tokenTransactionSignatures: {
        ownerSignatures: ownerSignatures,
      },
    });

    // Validate keyshare configuration
    if (
      startResponse.keyshareInfo?.ownerIdentifiers.length !==
      Object.keys(signingOperators).length
    ) {
      throw new Error(
        `Keyshare operator count (${
          startResponse.keyshareInfo?.ownerIdentifiers.length
        }) does not match signing operator count (${
          Object.keys(signingOperators).length
        })`,
      );
    }

    for (const identifier of startResponse.keyshareInfo?.ownerIdentifiers ||
      []) {
      if (!signingOperators[identifier]) {
        throw new Error(
          `Keyshare operator ${identifier} not found in signing operator list`,
        );
      }
    }

    const finalTokenTransaction = startResponse.finalTokenTransaction!;
    const finalTokenTransactionHash = hashTokenTransaction(
      finalTokenTransaction,
      false,
    );

    const payload: OperatorSpecificTokenTransactionSignablePayload = {
      finalTokenTransactionHash: finalTokenTransactionHash,
      operatorIdentityPublicKey:
        await this.config.signer.getIdentityPublicKey(),
    };

    const payloadHash =
      await hashOperatorSpecificTokenTransactionSignablePayload(payload);

    const operatorSpecificSignatures: OperatorSpecificTokenTransactionSignature[] =
      [];
    if (tokenTransaction.tokenInput!.$case === "mintInput") {
      const issuerPublicKey =
        tokenTransaction.tokenInput!.mintInput.issuerPublicKey;
      if (!issuerPublicKey) {
        throw new Error("issuer public key cannot be nil");
      }

      const ownerSignature = await this.signMessageWithKey(
        payloadHash,
        issuerPublicKey,
      );

      operatorSpecificSignatures.push({
        ownerPublicKey: issuerPublicKey,
        ownerSignature: ownerSignature,
        payload: payload,
      });
    }

    if (tokenTransaction.tokenInput!.$case === "transferInput") {
      const transferInput = tokenTransaction.tokenInput!.transferInput;
      for (let i = 0; i < transferInput.leavesToSpend.length; i++) {
        let ownerSignature: Uint8Array;
        if (this.config.shouldSignTokenTransactionsWithSchnorr()) {
          ownerSignature =
            await this.config.signer.signSchnorrWithIdentityKey(payloadHash);
        } else {
          ownerSignature =
            await this.config.signer.signMessageWithIdentityKey(payloadHash);
        }

        operatorSpecificSignatures.push({
          ownerPublicKey: await this.config.signer.getIdentityPublicKey(),
          ownerSignature: ownerSignature,
          payload: payload,
        });
      }
    }

    // Submit sign_token_transaction to all SOs in parallel and track their indices
    const soSignatures = await Promise.allSettled(
      Object.entries(signingOperators).map(
        async ([identifier, operator], index) => {
          const internalSparkClient =
            await this.connectionManager.createSparkClient(operator.address);
          const identityPublicKey =
            await this.config.signer.getIdentityPublicKey();

          const response = await internalSparkClient.sign_token_transaction(
            {
              finalTokenTransaction,
              operatorSpecificSignatures,
              identityPublicKey,
            },
            {
              retry: true,
              retryMaxAttempts: 5,
            } as SparkCallOptions,
          );

          return {
            index,
            identifier,
            response,
          };
        },
      ),
    );

    const threshold = startResponse.keyshareInfo.threshold;
    const successfulSignatures = validateResponses(soSignatures);

    if (tokenTransaction.tokenInput!.$case === "transferInput") {
      const leavesToSpend =
        tokenTransaction.tokenInput!.transferInput.leavesToSpend;

      let revocationKeys: Uint8Array[] = [];

      for (let leafIndex = 0; leafIndex < leavesToSpend.length; leafIndex++) {
        // For each leaf, collect keyshares from all SOs that responded successfully
        const leafKeyshares = successfulSignatures.map(
          ({ identifier, response }) => ({
            index: parseInt(identifier, 16),
            keyshare: response.tokenTransactionRevocationKeyshares[leafIndex],
          }),
        );

        if (leafKeyshares.length < threshold) {
          throw new Error(
            `Insufficient keyshares for leaf ${leafIndex}: got ${leafKeyshares.length}, need ${threshold}`,
          );
        }

        // Check for duplicate operator indices
        const seenIndices = new Set<number>();
        for (const { index } of leafKeyshares) {
          if (seenIndices.has(index)) {
            throw new Error(
              `Duplicate operator index ${index} for leaf ${leafIndex}`,
            );
          }
          seenIndices.add(index);
        }

        const recoveredPrivateKey = recoverPrivateKeyFromKeyshares(
          leafKeyshares as KeyshareWithOperatorIndex[],
          threshold,
        );
        const recoveredPublicKey = secp256k1.getPublicKey(
          recoveredPrivateKey,
          true,
        );

        if (
          !leafToSpendRevocationPublicKeys ||
          !leafToSpendRevocationPublicKeys[leafIndex] ||
          !recoveredPublicKey.every(
            (byte, i) =>
              byte === leafToSpendRevocationPublicKeys[leafIndex]![i],
          )
        ) {
          throw new Error(
            `Recovered public key does not match expected revocation public key for leaf ${leafIndex}`,
          );
        }

        revocationKeys.push(recoveredPrivateKey);
      }

      // Finalize the token transaction with the keyshares
      await this.finalizeTokenTransaction(
        finalTokenTransaction,
        revocationKeys,
        threshold,
      );
    }

    return bytesToHex(
      hashTokenTransaction(startResponse.finalTokenTransaction!),
    );
  }

  public async finalizeTokenTransaction(
    finalTokenTransaction: TokenTransaction,
    leafToSpendRevocationKeys: Uint8Array[],
    threshold: number,
  ): Promise<TokenTransaction> {
    const signingOperators = this.config.getSigningOperators();
    // Submit finalize_token_transaction to all SOs in parallel
    const soResponses = await Promise.allSettled(
      Object.entries(signingOperators).map(async ([identifier, operator]) => {
        const internalSparkClient =
          await this.connectionManager.createSparkClient(operator.address);
        const identityPublicKey =
          await this.config.signer.getIdentityPublicKey();

        const response = await internalSparkClient.finalize_token_transaction(
          {
            finalTokenTransaction,
            leafToSpendRevocationKeys,
            identityPublicKey,
          },
          {
            retry: true,
            retryMaxAttempts: 5,
          } as SparkCallOptions,
        );

        return {
          identifier,
          response,
        };
      }),
    );

    validateResponses(soResponses);

    return finalTokenTransaction;
  }

  public async fetchOwnedTokenLeaves(
    ownerPublicKeys: Uint8Array[],
    tokenPublicKeys: Uint8Array[],
  ): Promise<LeafWithPreviousTransactionData[]> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    const result = await sparkClient.query_token_outputs({
      ownerPublicKeys,
      tokenPublicKeys,
    });

    return result.leavesWithPreviousTransactionData;
  }

  public async syncTokenLeaves(
    tokenLeaves: Map<string, LeafWithPreviousTransactionData[]>,
  ) {
    const unsortedTokenLeaves = await this.fetchOwnedTokenLeaves(
      await this.config.signer.getTrackedPublicKeys(),
      [],
    );

    unsortedTokenLeaves.forEach((leaf) => {
      const tokenKey = bytesToHex(leaf.leaf!.tokenPublicKey!);
      const index = leaf.previousTransactionVout!;

      tokenLeaves.set(tokenKey, [{ ...leaf, previousTransactionVout: index }]);
    });
  }

  public selectTokenLeaves(
    tokenLeaves: LeafWithPreviousTransactionData[],
    tokenAmount: bigint,
  ): LeafWithPreviousTransactionData[] {
    if (calculateAvailableTokenAmount(tokenLeaves) < tokenAmount) {
      throw new Error("Insufficient available token amount");
    }

    // First try to find an exact match
    const exactMatch: LeafWithPreviousTransactionData | undefined =
      tokenLeaves.find(
        (item) => bytesToNumberBE(item.leaf!.tokenAmount!) === tokenAmount,
      );

    if (exactMatch) {
      return [exactMatch];
    }

    // Sort by amount ascending for optimal selection.
    // It's in user's interest to hold as little leaves as possible,
    // so that in the event of a unilateral exit the fees are as low as possible
    tokenLeaves.sort((a, b) =>
      Number(
        bytesToNumberBE(a.leaf!.tokenAmount!) -
          bytesToNumberBE(b.leaf!.tokenAmount!),
      ),
    );

    let remainingAmount = tokenAmount;
    const selectedLeaves: typeof tokenLeaves = [];

    // Select leaves using a greedy approach
    for (const leafInfo of tokenLeaves) {
      if (remainingAmount <= 0n) break;

      selectedLeaves.push(leafInfo);
      remainingAmount -= bytesToNumberBE(leafInfo.leaf!.tokenAmount!);
    }

    if (remainingAmount > 0n) {
      throw new Error(
        "You do not have enough funds to complete the specified operation",
      );
    }

    return selectedLeaves;
  }

  // Helper function for deciding if the signer public key is the identity public key
  private async signMessageWithKey(
    message: Uint8Array,
    publicKey: Uint8Array,
  ): Promise<Uint8Array> {
    const signWithSchnorr =
      this.config.shouldSignTokenTransactionsWithSchnorr();
    if (
      bytesToHex(publicKey) ===
      bytesToHex(await this.config.signer.getIdentityPublicKey())
    ) {
      if (signWithSchnorr) {
        return await this.config.signer.signSchnorrWithIdentityKey(message);
      } else {
        return await this.config.signer.signMessageWithIdentityKey(message);
      }
    } else {
      if (signWithSchnorr) {
        return await this.config.signer.signSchnorr(message, publicKey);
      } else {
        return await this.config.signer.signMessageWithPublicKey(
          message,
          publicKey,
        );
      }
    }
  }
}
