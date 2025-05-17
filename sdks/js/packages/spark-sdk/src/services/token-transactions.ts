import {
  bytesToHex,
  bytesToNumberBE,
  numberToBytesBE,
} from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import {
  OutputWithPreviousTransactionData,
  OperatorSpecificTokenTransactionSignablePayload,
  OperatorSpecificOwnerSignature,
  TokenTransaction,
  StartTokenTransactionResponse,
  SignTokenTransactionResponse,
  SignatureWithIndex,
  RevocationSecretWithIndex,
} from "../proto/spark.js";
import { SparkCallOptions } from "../types/grpc.js";
import { collectResponses } from "../utils/response-validation.js";
import {
  hashOperatorSpecificTokenTransactionSignablePayload,
  hashTokenTransaction,
} from "../utils/token-hashing.js";
import {
  KeyshareWithOperatorIndex,
  recoverRevocationSecretFromKeyshares,
} from "../utils/token-keyshares.js";
import { calculateAvailableTokenAmount } from "../utils/token-transactions.js";
import { validateTokenTransaction } from "../utils/token-transaction-validation.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection.js";
import {
  ValidationError,
  NetworkError,
  InternalValidationError,
} from "../errors/types.js";
import { SigningOperator } from "./wallet-config.js";
import { hexToBytes } from "@noble/hashes/utils";

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
      operatorKeys.push(hexToBytes(operator.identityPublicKey));
    }

    return operatorKeys;
  }

  public async broadcastTokenTransaction(
    tokenTransaction: TokenTransaction,
    outputsToSpendSigningPublicKeys?: Uint8Array[],
    outputsToSpendCommitments?: Uint8Array[],
  ): Promise<string> {
    const signingOperators = this.config.getSigningOperators();

    const { finalTokenTransaction, finalTokenTransactionHash, threshold } =
      await this.startTokenTransaction(
        tokenTransaction,
        signingOperators,
        outputsToSpendSigningPublicKeys,
        outputsToSpendCommitments,
      );

    const { successfulSignatures } = await this.signTokenTransaction(
      finalTokenTransaction,
      finalTokenTransactionHash,
      signingOperators,
    );

    if (finalTokenTransaction.tokenInputs!.$case === "transferInput") {
      const outputsToSpend =
        finalTokenTransaction.tokenInputs!.transferInput.outputsToSpend;

      const errors: ValidationError[] = [];
      const revocationSecrets: RevocationSecretWithIndex[] = [];

      for (
        let outputIndex = 0;
        outputIndex < outputsToSpend.length;
        outputIndex++
      ) {
        // For each output, collect keyshares from all SOs that responded successfully
        const outputKeyshares: KeyshareWithOperatorIndex[] =
          successfulSignatures.map(({ identifier, response }) => ({
            operatorIndex: parseInt(identifier, 16),
            keyshare: response.revocationKeyshares[outputIndex]!,
          }));

        if (outputKeyshares.length < threshold) {
          errors.push(
            new ValidationError("Insufficient keyshares", {
              field: "outputKeyshares",
              value: outputKeyshares.length,
              expected: threshold,
              index: outputIndex,
            }),
          );
        }

        // Check for duplicate operator indices
        const seenIndices = new Set<number>();
        for (const { operatorIndex } of outputKeyshares) {
          if (seenIndices.has(operatorIndex)) {
            errors.push(
              new ValidationError("Duplicate operator index", {
                field: "outputKeyshares",
                value: operatorIndex,
                expected: "Unique operator index",
                index: outputIndex,
              }),
            );
          }
          seenIndices.add(operatorIndex);
        }

        const revocationSecret = recoverRevocationSecretFromKeyshares(
          outputKeyshares as KeyshareWithOperatorIndex[],
          threshold,
        );
        const derivedRevocationCommitment = secp256k1.getPublicKey(
          revocationSecret,
          true,
        );

        if (
          !outputsToSpendCommitments ||
          !outputsToSpendCommitments[outputIndex] ||
          !derivedRevocationCommitment.every(
            (byte, i) => byte === outputsToSpendCommitments[outputIndex]![i],
          )
        ) {
          errors.push(
            new InternalValidationError(
              "Revocation commitment verification failed",
              {
                field: "revocationCommitment",
                value: derivedRevocationCommitment,
                expected: bytesToHex(outputsToSpendCommitments![outputIndex]!),
                outputIndex: outputIndex,
              },
            ),
          );
        }

        revocationSecrets.push({
          inputIndex: outputIndex,
          revocationSecret,
        });
      }

      if (errors.length > 0) {
        throw new ValidationError(
          "Multiple validation errors occurred across outputs",
          {
            field: "outputValidation",
            value: errors,
          },
        );
      }

      // Finalize the token transaction with the keyshares
      await this.finalizeTokenTransaction(
        finalTokenTransaction,
        revocationSecrets,
        threshold,
      );
    }

    return bytesToHex(finalTokenTransactionHash);
  }

  private async startTokenTransaction(
    tokenTransaction: TokenTransaction,
    signingOperators: Record<string, SigningOperator>,
    outputsToSpendSigningPublicKeys?: Uint8Array[],
    outputsToSpendCommitments?: Uint8Array[],
  ): Promise<{
    finalTokenTransaction: TokenTransaction;
    finalTokenTransactionHash: Uint8Array;
    threshold: number;
  }> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    const partialTokenTransactionHash = hashTokenTransaction(
      tokenTransaction,
      true,
    );

    const ownerSignaturesWithIndex: SignatureWithIndex[] = [];
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

      ownerSignaturesWithIndex.push({
        signature: ownerSignature,
        inputIndex: 0,
      });
    } else if (tokenTransaction.tokenInputs!.$case === "transferInput") {
      if (!outputsToSpendSigningPublicKeys || !outputsToSpendCommitments) {
        throw new ValidationError("Invalid transfer input", {
          field: "outputsToSpend",
          value: {
            signingPublicKeys: outputsToSpendSigningPublicKeys,
            revocationPublicKeys: outputsToSpendCommitments,
          },
          expected: "Non-null signing and revocation public keys",
        });
      }

      for (const [i, key] of outputsToSpendSigningPublicKeys.entries()) {
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

        ownerSignaturesWithIndex.push({
          signature: ownerSignature,
          inputIndex: i,
        });
      }
    }
    // Start the token transaction
    const startResponse = await sparkClient.start_token_transaction(
      {
        identityPublicKey: await this.config.signer.getIdentityPublicKey(),
        partialTokenTransaction: tokenTransaction,
        tokenTransactionSignatures: {
          ownerSignatures: ownerSignaturesWithIndex,
        },
      },
      {
        retry: true,
        retryableStatuses: ["UNKNOWN", "UNAVAILABLE", "CANCELLED"],
        retryMaxAttempts: 3,
      } as SparkCallOptions,
    );

    if (!startResponse.finalTokenTransaction) {
      throw new Error("Final token transaction missing in start response");
    }
    if (!startResponse.keyshareInfo) {
      throw new Error("Keyshare info missing in start response");
    }

    validateTokenTransaction(
      startResponse.finalTokenTransaction,
      tokenTransaction,
      signingOperators,
      startResponse.keyshareInfo,
      this.config.getExpectedWithdrawBondSats(),
      this.config.getExpectedWithdrawRelativeBlockLocktime(),
      this.config.getThreshold(),
    );

    const finalTokenTransaction = startResponse.finalTokenTransaction;
    const finalTokenTransactionHash = hashTokenTransaction(
      finalTokenTransaction,
      false,
    );

    return {
      finalTokenTransaction,
      finalTokenTransactionHash,
      threshold: startResponse.keyshareInfo!.threshold,
    };
  }

  private async signTokenTransaction(
    finalTokenTransaction: TokenTransaction,
    finalTokenTransactionHash: Uint8Array,
    signingOperators: Record<string, SigningOperator>,
  ): Promise<{
    successfulSignatures: {
      index: number;
      identifier: string;
      response: SignTokenTransactionResponse;
    }[];
  }> {
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
            operatorIdentityPublicKey: hexToBytes(operator.identityPublicKey),
          };

          const payloadHash =
            await hashOperatorSpecificTokenTransactionSignablePayload(payload);

          let operatorSpecificSignatures: OperatorSpecificOwnerSignature[] = [];
          if (finalTokenTransaction.tokenInputs!.$case === "mintInput") {
            const issuerPublicKey =
              finalTokenTransaction.tokenInputs!.mintInput.issuerPublicKey;
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
              ownerSignature: {
                signature: ownerSignature,
                inputIndex: 0,
              },
              payload: payload,
            });
          }

          if (finalTokenTransaction.tokenInputs!.$case === "transferInput") {
            const transferInput =
              finalTokenTransaction.tokenInputs!.transferInput;
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
                ownerSignature: {
                  signature: ownerSignature,
                  inputIndex: i,
                },
                payload,
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
                retryableStatuses: ["UNKNOWN", "UNAVAILABLE", "CANCELLED"],
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
              error as Error,
            );
          }
        },
      ),
    );

    const successfulSignatures = collectResponses(soSignatures);

    return {
      successfulSignatures,
    };
  }

  public async finalizeTokenTransaction(
    finalTokenTransaction: TokenTransaction,
    revocationSecrets: RevocationSecretWithIndex[],
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
              revocationSecrets,
              identityPublicKey,
            },
            {
              retry: true,
              retryableStatuses: ["UNKNOWN", "UNAVAILABLE", "CANCELLED"],
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
            error as Error,
          );
        }
      }),
    );

    collectResponses(soResponses);

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
        error as Error,
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
