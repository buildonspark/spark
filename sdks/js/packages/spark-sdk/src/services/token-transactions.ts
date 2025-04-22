import {
  bytesToHex,
  bytesToNumberBE,
  numberToBytesBE,
} from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import {
  OutputWithPreviousTransactionData,
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
import {
  ValidationError,
  NetworkError,
  AuthenticationError,
} from "../errors/types.js";

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
    selectedOutputs: OutputWithPreviousTransactionData[],
    receiverSparkAddress: Uint8Array,
    tokenPublicKey: Uint8Array,
    tokenAmount: bigint,
  ): Promise<TokenTransaction> {
    let availableTokenAmount = calculateAvailableTokenAmount(selectedOutputs);

    if (availableTokenAmount === tokenAmount) {
      return {
        network: this.config.getNetworkProto(),
        tokenInputs: {
          $case: "transferInput",
          transferInput: {
            outputsToSpend: selectedOutputs.map((output) => ({
              prevTokenTransactionHash: output.previousTransactionHash,
              prevTokenTransactionVout: output.previousTransactionVout,
            })),
          },
        },
        tokenOutputs: [
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
        tokenInputs: {
          $case: "transferInput",
          transferInput: {
            outputsToSpend: selectedOutputs.map((output) => ({
              prevTokenTransactionHash: output.previousTransactionHash,
              prevTokenTransactionVout: output.previousTransactionVout,
            })),
          },
        },
        tokenOutputs: [
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
    outputsToSpendSigningPublicKeys?: Uint8Array[],
    outputsToSpendRevocationPublicKeys?: Uint8Array[],
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
    if (tokenTransaction.tokenInputs!.$case === "mintInput") {
      const issuerPublicKey =
        tokenTransaction.tokenInputs!.mintInput.issuerPublicKey;
      if (!issuerPublicKey) {
        throw new ValidationError("Invalid mint input", {
          field: "issuerPublicKey",
          value: null,
          expected: "Non-null issuer public key",
        });
      }

      const ownerSignature = await this.signMessageWithKey(
        partialTokenTransactionHash,
        issuerPublicKey,
      );

      ownerSignatures.push(ownerSignature);
    } else if (tokenTransaction.tokenInputs!.$case === "transferInput") {
      const transferInput = tokenTransaction.tokenInputs!.transferInput;

      if (
        !outputsToSpendSigningPublicKeys ||
        !outputsToSpendRevocationPublicKeys
      ) {
        throw new ValidationError("Invalid transfer input", {
          field: "outputsToSpend",
          value: {
            signingPublicKeys: outputsToSpendSigningPublicKeys,
            revocationPublicKeys: outputsToSpendRevocationPublicKeys,
          },
          expected: "Non-null signing and revocation public keys",
        });
      }

      for (let i = 0; i < transferInput.outputsToSpend.length; i++) {
        const key = outputsToSpendSigningPublicKeys![i];
        if (!key) {
          throw new ValidationError("Invalid signing key", {
            field: "outputsToSpendSigningPublicKeys",
            value: i,
            expected: "Non-null signing key",
          });
        }
        const ownerSignature = await this.signMessageWithKey(
          partialTokenTransactionHash,
          key,
        );

        ownerSignatures.push(ownerSignature);
      }
    }

    // Start the token transaction
    const startResponse = await sparkClient.start_token_transaction(
      {
        identityPublicKey: await this.config.signer.getIdentityPublicKey(),
        partialTokenTransaction: tokenTransaction,
        tokenTransactionSignatures: {
          ownerSignatures: ownerSignatures,
        },
      },
      {
        retry: true,
        retryMaxAttempts: 3,
      } as SparkCallOptions,
    );

    // Validate keyshare configuration
    if (
      startResponse.keyshareInfo?.ownerIdentifiers.length !==
      Object.keys(signingOperators).length
    ) {
      throw new ValidationError("Invalid keyshare configuration", {
        field: "ownerIdentifiers",
        value: startResponse.keyshareInfo?.ownerIdentifiers.length,
        expected: Object.keys(signingOperators).length,
      });
    }

    for (const identifier of startResponse.keyshareInfo?.ownerIdentifiers ||
      []) {
      if (!signingOperators[identifier]) {
        throw new ValidationError("Invalid keyshare operator", {
          field: "ownerIdentifiers",
          value: identifier,
          expected: "Valid operator identifier",
        });
      }
    }

    const finalTokenTransaction = startResponse.finalTokenTransaction!;
    const finalTokenTransactionHash = hashTokenTransaction(
      finalTokenTransaction,
      false,
    );

    // Submit sign_token_transaction to all SOs in parallel and track their indices
    const soSignatures = await Promise.allSettled(
      Object.entries(signingOperators).map(
        async ([identifier, operator], index) => {
          const internalSparkClient =
            await this.connectionManager.createSparkClient(operator.address);
          const identityPublicKey =
            await this.config.signer.getIdentityPublicKey();

          // Create operator-specific payload with operator's identity public key
          const payload: OperatorSpecificTokenTransactionSignablePayload = {
            finalTokenTransactionHash: finalTokenTransactionHash,
            operatorIdentityPublicKey: operator.identityPublicKey,
          };

          const payloadHash =
            await hashOperatorSpecificTokenTransactionSignablePayload(payload);

          const operatorSpecificSignatures: OperatorSpecificTokenTransactionSignature[] =
            [];

          if (tokenTransaction.tokenInputs!.$case === "mintInput") {
            const issuerPublicKey =
              tokenTransaction.tokenInputs!.mintInput.issuerPublicKey;
            if (!issuerPublicKey) {
              throw new ValidationError("Invalid mint input", {
                field: "issuerPublicKey",
                value: null,
                expected: "Non-null issuer public key",
              });
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

          if (tokenTransaction.tokenInputs!.$case === "transferInput") {
            const transferInput = tokenTransaction.tokenInputs!.transferInput;
            for (let i = 0; i < transferInput.outputsToSpend.length; i++) {
              let ownerSignature: Uint8Array;
              if (this.config.shouldSignTokenTransactionsWithSchnorr()) {
                ownerSignature =
                  await this.config.signer.signSchnorrWithIdentityKey(
                    payloadHash,
                  );
              } else {
                ownerSignature =
                  await this.config.signer.signMessageWithIdentityKey(
                    payloadHash,
                  );
              }

              operatorSpecificSignatures.push({
                ownerPublicKey: await this.config.signer.getIdentityPublicKey(),
                ownerSignature: ownerSignature,
                payload: payload,
              });
            }
          }

          try {
            const response = await internalSparkClient.sign_token_transaction(
              {
                finalTokenTransaction,
                operatorSpecificSignatures,
                identityPublicKey,
              },
              {
                retry: true,
                retryMaxAttempts: 3,
              } as SparkCallOptions,
            );

            return {
              index,
              identifier,
              response,
            };
          } catch (error) {
            throw new NetworkError(
              "Failed to sign token transaction",
              {
                operation: "sign_token_transaction",
                errorCount: 1,
                errors: error instanceof Error ? error.message : String(error),
              },
              error instanceof Error ? error : undefined,
            );
          }
        },
      ),
    );

    const threshold = startResponse.keyshareInfo.threshold;
    const successfulSignatures = validateResponses(soSignatures);

    if (tokenTransaction.tokenInputs!.$case === "transferInput") {
      const outputsToSpend =
        tokenTransaction.tokenInputs!.transferInput.outputsToSpend;

      let revocationKeys: Uint8Array[] = [];

      for (
        let outputIndex = 0;
        outputIndex < outputsToSpend.length;
        outputIndex++
      ) {
        // For each output, collect keyshares from all SOs that responded successfully
        const outputKeyshares = successfulSignatures.map(
          ({ identifier, response }) => ({
            index: parseInt(identifier, 16),
            keyshare: response.tokenTransactionRevocationKeyshares[outputIndex],
          }),
        );

        if (outputKeyshares.length < threshold) {
          throw new ValidationError("Insufficient keyshares", {
            field: "outputKeyshares",
            value: outputKeyshares.length,
            expected: threshold,
            index: outputIndex,
          });
        }

        // Check for duplicate operator indices
        const seenIndices = new Set<number>();
        for (const { index } of outputKeyshares) {
          if (seenIndices.has(index)) {
            throw new ValidationError("Duplicate operator index", {
              field: "outputKeyshares",
              value: index,
              expected: "Unique operator index",
              index: outputIndex,
            });
          }
          seenIndices.add(index);
        }

        const recoveredPrivateKey = recoverPrivateKeyFromKeyshares(
          outputKeyshares as KeyshareWithOperatorIndex[],
          threshold,
        );
        const recoveredPublicKey = secp256k1.getPublicKey(
          recoveredPrivateKey,
          true,
        );

        if (
          !outputsToSpendRevocationPublicKeys ||
          !outputsToSpendRevocationPublicKeys[outputIndex] ||
          !recoveredPublicKey.every(
            (byte, i) =>
              byte === outputsToSpendRevocationPublicKeys[outputIndex]![i],
          )
        ) {
          throw new ValidationError("Invalid revocation key", {
            field: "recoveredPublicKey",
            value: bytesToHex(recoveredPublicKey),
            index: outputIndex,
          });
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
    outputToSpendRevocationSecrets: Uint8Array[],
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

        try {
          const response = await internalSparkClient.finalize_token_transaction(
            {
              finalTokenTransaction,
              outputToSpendRevocationSecrets,
              identityPublicKey,
            },
            {
              retry: true,
              retryMaxAttempts: 3,
            } as SparkCallOptions,
          );

          return {
            identifier,
            response,
          };
        } catch (error) {
          throw new NetworkError(
            "Failed to finalize token transaction",
            {
              operation: "finalize_token_transaction",
              errorCount: 1,
              errors: error instanceof Error ? error.message : String(error),
            },
            error instanceof Error ? error : undefined,
          );
        }
      }),
    );

    validateResponses(soResponses);

    return finalTokenTransaction;
  }

  public async fetchOwnedTokenOutputs(
    ownerPublicKeys: Uint8Array[],
    tokenPublicKeys: Uint8Array[],
  ): Promise<OutputWithPreviousTransactionData[]> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    try {
      const result = await sparkClient.query_token_outputs({
        ownerPublicKeys,
        tokenPublicKeys,
      });

      return result.outputsWithPreviousTransactionData;
    } catch (error) {
      throw new NetworkError(
        "Failed to fetch owned token outputs",
        {
          operation: "query_token_outputs",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
        error instanceof Error ? error : undefined,
      );
    }
  }

  public async syncTokenOutputs(
    tokenOutputs: Map<string, OutputWithPreviousTransactionData[]>,
  ) {
    const unsortedTokenOutputs = await this.fetchOwnedTokenOutputs(
      await this.config.signer.getTrackedPublicKeys(),
      [],
    );

    unsortedTokenOutputs.forEach((output) => {
      const tokenKey = bytesToHex(output.output!.tokenPublicKey!);
      const index = output.previousTransactionVout!;

      tokenOutputs.set(tokenKey, [
        { ...output, previousTransactionVout: index },
      ]);
    });
  }

  public selectTokenOutputs(
    tokenOutputs: OutputWithPreviousTransactionData[],
    tokenAmount: bigint,
  ): OutputWithPreviousTransactionData[] {
    if (calculateAvailableTokenAmount(tokenOutputs) < tokenAmount) {
      throw new ValidationError("Insufficient token amount", {
        field: "tokenAmount",
        value: calculateAvailableTokenAmount(tokenOutputs),
        expected: tokenAmount,
      });
    }

    // First try to find an exact match
    const exactMatch: OutputWithPreviousTransactionData | undefined =
      tokenOutputs.find(
        (item) => bytesToNumberBE(item.output!.tokenAmount!) === tokenAmount,
      );

    if (exactMatch) {
      return [exactMatch];
    }

    // Sort by amount ascending for optimal selection.
    // It's in user's interest to hold as little token outputs as possible,
    // so that in the event of a unilateral exit the fees are as low as possible
    tokenOutputs.sort((a, b) =>
      Number(
        bytesToNumberBE(a.output!.tokenAmount!) -
          bytesToNumberBE(b.output!.tokenAmount!),
      ),
    );

    let remainingAmount = tokenAmount;
    const selectedOutputs: typeof tokenOutputs = [];

    // Select outputs using a greedy approach
    for (const outputWithPreviousTransactionData of tokenOutputs) {
      if (remainingAmount <= 0n) break;

      selectedOutputs.push(outputWithPreviousTransactionData);
      remainingAmount -= bytesToNumberBE(
        outputWithPreviousTransactionData.output!.tokenAmount!,
      );
    }

    if (remainingAmount > 0n) {
      throw new ValidationError("Insufficient funds", {
        field: "remainingAmount",
        value: remainingAmount,
      });
    }

    return selectedOutputs;
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
