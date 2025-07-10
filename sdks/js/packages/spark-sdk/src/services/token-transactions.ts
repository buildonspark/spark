import {
  bytesToHex,
  bytesToNumberBE,
  numberToBytesBE,
} from "@noble/curves/abstract/utils";
import {
  OutputWithPreviousTransactionData,
  OperatorSpecificTokenTransactionSignablePayload,
  SignTokenTransactionResponse,
  OperatorSpecificOwnerSignature,
  RevocationSecretWithIndex,
} from "../proto/spark.js";
import { secp256k1 } from "@noble/curves/secp256k1";
import { SparkCallOptions } from "../types/grpc.js";
import {
  hashOperatorSpecificTokenTransactionSignablePayload,
  hashTokenTransactionV0,
  hashTokenTransaction,
} from "../utils/token-hashing.js";
import {
  calculateAvailableTokenAmount,
  checkIfSelectedOutputsAreAvailable,
} from "../utils/token-transactions.js";
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
import { decodeSparkAddress } from "../address/index.js";
import {
  TokenTransaction,
  SignatureWithIndex,
  InputTtxoSignaturesPerOperator,
} from "../proto/spark_token.js";
import { TokenTransaction as TokenTransactionV0 } from "../proto/spark.js";
import { collectResponses } from "../utils/response-validation.js";
import {
  KeyshareWithOperatorIndex,
  recoverRevocationSecretFromKeyshares,
} from "../utils/token-keyshares.js";

const MAX_TOKEN_OUTPUTS = 500;

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

  public async tokenTransfer(
    tokenOutputs: Map<string, OutputWithPreviousTransactionData[]>,
    receiverOutputs: {
      tokenPublicKey: string;
      tokenAmount: bigint;
      receiverSparkAddress: string;
    }[],
    outputSelectionStrategy: "SMALL_FIRST" | "LARGE_FIRST" = "SMALL_FIRST",
    selectedOutputs?: OutputWithPreviousTransactionData[],
  ): Promise<string> {
    if (receiverOutputs.length === 0) {
      throw new ValidationError("No receiver outputs provided", {
        field: "receiverOutputs",
        value: receiverOutputs,
        expected: "Non-empty array",
      });
    }

    const totalTokenAmount = receiverOutputs.reduce(
      (sum, transfer) => sum + transfer.tokenAmount,
      0n,
    );

    let outputsToUse: OutputWithPreviousTransactionData[];

    if (selectedOutputs) {
      outputsToUse = selectedOutputs;

      if (
        !checkIfSelectedOutputsAreAvailable(
          outputsToUse,
          tokenOutputs,
          hexToBytes(receiverOutputs[0]!!.tokenPublicKey),
        )
      ) {
        throw new ValidationError(
          "One or more selected TTXOs are not available",
          {
            field: "selectedOutputs",
            value: selectedOutputs,
            expected: "Available TTXOs",
          },
        );
      }
    } else {
      outputsToUse = this.selectTokenOutputs(
        tokenOutputs.get(receiverOutputs[0]!!.tokenPublicKey)!!,
        totalTokenAmount,
        outputSelectionStrategy,
      );
    }

    if (outputsToUse.length > MAX_TOKEN_OUTPUTS) {
      const availableOutputs = tokenOutputs.get(
        receiverOutputs[0]!!.tokenPublicKey,
      )!!;

      // Sort outputs by the same strategy as in selectTokenOutputs
      const sortedOutputs = [...availableOutputs];
      this.sortTokenOutputsByStrategy(sortedOutputs, outputSelectionStrategy);

      // Take only the first MAX_TOKEN_OUTPUTS and calculate their total
      const maxOutputsToUse = sortedOutputs.slice(0, MAX_TOKEN_OUTPUTS);
      const maxAmount = calculateAvailableTokenAmount(maxOutputsToUse);

      throw new ValidationError(
        `Cannot transfer more than ${MAX_TOKEN_OUTPUTS} TTXOs in a single transaction (${outputsToUse.length} selected). Maximum transferable amount is: ${maxAmount}`,
        {
          field: "outputsToUse",
          value: outputsToUse.length,
          expected: `Less than or equal to ${MAX_TOKEN_OUTPUTS}, with maximum transferable amount of ${maxAmount}`,
        },
      );
    }

    const tokenOutputData = receiverOutputs.map((transfer) => {
      const receiverAddress = decodeSparkAddress(
        transfer.receiverSparkAddress,
        this.config.getNetworkType(),
      );
      return {
        receiverSparkAddress: hexToBytes(receiverAddress.identityPublicKey),
        tokenPublicKey: hexToBytes(transfer.tokenPublicKey),
        tokenAmount: transfer.tokenAmount,
      };
    });

    let tokenTransaction: TokenTransactionV0 | TokenTransaction;

    if (this.config.getTokenTransactionVersion() === "V0") {
      tokenTransaction = await this.constructTransferTokenTransactionV0(
        outputsToUse,
        tokenOutputData,
      );
    } else {
      tokenTransaction = await this.constructTransferTokenTransaction(
        outputsToUse,
        tokenOutputData,
      );
    }

    const txId = await this.broadcastTokenTransaction(
      tokenTransaction,
      outputsToUse.map((output) => output.output!.ownerPublicKey),
      outputsToUse.map((output) => output.output!.revocationCommitment!),
    );

    return txId;
  }

  public async constructTransferTokenTransactionV0(
    selectedOutputs: OutputWithPreviousTransactionData[],
    tokenOutputData: Array<{
      receiverSparkAddress: Uint8Array;
      tokenPublicKey: Uint8Array;
      tokenAmount: bigint;
    }>,
  ): Promise<TokenTransactionV0> {
    // Ensure outputsToSpend are ordered by vout ascending so that the input indices
    // used for owner signatures match the order expected by the SO, which sorts
    // inputs by "prevTokenTransactionVout" before validating signatures.
    selectedOutputs.sort(
      (a, b) => a.previousTransactionVout - b.previousTransactionVout,
    );

    const availableTokenAmount = calculateAvailableTokenAmount(selectedOutputs);
    const totalRequestedAmount = tokenOutputData.reduce(
      (sum, output) => sum + output.tokenAmount,
      0n,
    );

    const tokenOutputs = tokenOutputData.map((output) => ({
      ownerPublicKey: output.receiverSparkAddress,
      tokenPublicKey: output.tokenPublicKey,
      tokenAmount: numberToBytesBE(output.tokenAmount, 16),
    }));

    if (availableTokenAmount > totalRequestedAmount) {
      const changeAmount = availableTokenAmount - totalRequestedAmount;
      const firstTokenPublicKey = tokenOutputData[0]!!.tokenPublicKey;

      tokenOutputs.push({
        ownerPublicKey: await this.config.signer.getIdentityPublicKey(),
        tokenPublicKey: firstTokenPublicKey,
        tokenAmount: numberToBytesBE(changeAmount, 16),
      });
    }

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
      tokenOutputs,
      sparkOperatorIdentityPublicKeys: this.collectOperatorIdentityPublicKeys(),
    };
  }

  public async constructTransferTokenTransaction(
    selectedOutputs: OutputWithPreviousTransactionData[],
    tokenOutputData: Array<{
      receiverSparkAddress: Uint8Array;
      tokenPublicKey: Uint8Array;
      tokenAmount: bigint;
    }>,
  ): Promise<TokenTransaction> {
    selectedOutputs.sort(
      (a, b) => a.previousTransactionVout - b.previousTransactionVout,
    );

    const availableTokenAmount = calculateAvailableTokenAmount(selectedOutputs);
    const totalRequestedAmount = tokenOutputData.reduce(
      (sum, output) => sum + output.tokenAmount,
      0n,
    );

    const tokenOutputs = tokenOutputData.map((output) => ({
      ownerPublicKey: output.receiverSparkAddress,
      tokenPublicKey: output.tokenPublicKey,
      tokenAmount: numberToBytesBE(output.tokenAmount, 16),
    }));

    if (availableTokenAmount > totalRequestedAmount) {
      const changeAmount = availableTokenAmount - totalRequestedAmount;
      const firstTokenPublicKey = tokenOutputData[0]!!.tokenPublicKey;

      tokenOutputs.push({
        ownerPublicKey: await this.config.signer.getIdentityPublicKey(),
        tokenPublicKey: firstTokenPublicKey,
        tokenAmount: numberToBytesBE(changeAmount, 16),
      });
    }

    return {
      version: 1,
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
      tokenOutputs,
      sparkOperatorIdentityPublicKeys: this.collectOperatorIdentityPublicKeys(),
      expiryTime: undefined,
      clientCreatedTimestamp: new Date(),
    };
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
    tokenTransaction: TokenTransactionV0 | TokenTransaction,
    outputsToSpendSigningPublicKeys?: Uint8Array[],
    outputsToSpendCommitments?: Uint8Array[],
  ): Promise<string> {
    const signingOperators = this.config.getSigningOperators();
    if (!isTokenTransaction(tokenTransaction)) {
      return this.broadcastTokenTransactionV0(
        tokenTransaction,
        signingOperators,
        outputsToSpendSigningPublicKeys,
        outputsToSpendCommitments,
      );
    } else {
      return this.broadcastTokenTransactionV1(
        tokenTransaction as TokenTransaction,
        signingOperators,
        outputsToSpendSigningPublicKeys,
        outputsToSpendCommitments,
      );
    }
  }

  private async broadcastTokenTransactionV0(
    tokenTransaction: TokenTransactionV0,
    signingOperators: Record<string, SigningOperator>,
    outputsToSpendSigningPublicKeys?: Uint8Array[],
    outputsToSpendCommitments?: Uint8Array[],
  ): Promise<string> {
    const { finalTokenTransaction, finalTokenTransactionHash, threshold } =
      await this.startTokenTransactionV0(
        tokenTransaction,
        signingOperators,
        outputsToSpendSigningPublicKeys,
        outputsToSpendCommitments,
      );

    const { successfulSignatures } = await this.signTokenTransactionV0(
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

  private async broadcastTokenTransactionV1(
    tokenTransaction: TokenTransaction,
    signingOperators: Record<string, SigningOperator>,
    outputsToSpendSigningPublicKeys?: Uint8Array[],
    outputsToSpendCommitments?: Uint8Array[],
  ): Promise<string> {
    const { finalTokenTransaction, finalTokenTransactionHash, threshold } =
      await this.startTokenTransaction(
        tokenTransaction,
        signingOperators,
        outputsToSpendSigningPublicKeys,
        outputsToSpendCommitments,
      );

    await this.signTokenTransaction(
      finalTokenTransaction,
      finalTokenTransactionHash,
      signingOperators,
    );

    return bytesToHex(finalTokenTransactionHash);
  }

  private async startTokenTransactionV0(
    tokenTransaction: TokenTransactionV0,
    signingOperators: Record<string, SigningOperator>,
    outputsToSpendSigningPublicKeys?: Uint8Array[],
    outputsToSpendCommitments?: Uint8Array[],
  ): Promise<{
    finalTokenTransaction: TokenTransactionV0;
    finalTokenTransactionHash: Uint8Array;
    threshold: number;
  }> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    const partialTokenTransactionHash = hashTokenTransactionV0(
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
        retryableStatuses: ["UNKNOWN", "UNAVAILABLE", "CANCELLED", "INTERNAL"],
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
    const finalTokenTransactionHash = hashTokenTransactionV0(
      finalTokenTransaction,
      false,
    );

    return {
      finalTokenTransaction,
      finalTokenTransactionHash,
      threshold: startResponse.keyshareInfo!.threshold,
    };
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
    const sparkClient = await this.connectionManager.createSparkTokenClient(
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

    const startResponse = await sparkClient.start_transaction(
      {
        identityPublicKey: await this.config.signer.getIdentityPublicKey(),
        partialTokenTransaction: tokenTransaction,
        validityDurationSeconds:
          await this.config.getTokenValidityDurationSeconds(),
        partialTokenTransactionOwnerSignatures: ownerSignaturesWithIndex,
      },
      {
        retry: true,
        retryableStatuses: ["UNKNOWN", "UNAVAILABLE", "CANCELLED", "INTERNAL"],
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

  private async signTokenTransactionV0(
    finalTokenTransaction: TokenTransactionV0,
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

          const operatorSpecificSignatures = await this.createOperatorSpecificSignaturesV0(
            finalTokenTransaction,
            finalTokenTransactionHash,
            operator,
          );

          try {
            const response = await internalSparkClient.sign_token_transaction(
              {
                finalTokenTransaction,
                operatorSpecificSignatures,
                identityPublicKey,
              },
              {
                retry: true,
                retryableStatuses: [
                  "UNKNOWN",
                  "UNAVAILABLE",
                  "CANCELLED",
                  "INTERNAL",
                ],
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

  private async signTokenTransaction(
    finalTokenTransaction: TokenTransaction,
    finalTokenTransactionHash: Uint8Array,
    signingOperators: Record<string, SigningOperator>,
  ) {
    const coordinatorClient =
      await this.connectionManager.createSparkTokenClient(
        this.config.getCoordinatorAddress(),
      );

    const inputTtxoSignaturesPerOperator =
      await this.createSignaturesForOperators(
        finalTokenTransaction,
        finalTokenTransactionHash,
        signingOperators,
      );

    try {
      await coordinatorClient.commit_transaction(
        {
          finalTokenTransaction,
          finalTokenTransactionHash,
          inputTtxoSignaturesPerOperator,
          ownerIdentityPublicKey:
            await this.config.signer.getIdentityPublicKey(),
        },
        {
          retry: true,
          retryableStatuses: [
            "UNKNOWN",
            "UNAVAILABLE",
            "CANCELLED",
            "INTERNAL",
          ],
          retryMaxAttempts: 3,
        } as SparkCallOptions,
      );
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
        network: this.config.getNetworkProto(),
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
    strategy: "SMALL_FIRST" | "LARGE_FIRST",
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

    // Sort based on configured strategy
    this.sortTokenOutputsByStrategy(tokenOutputs, strategy);

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

  private sortTokenOutputsByStrategy(
    tokenOutputs: OutputWithPreviousTransactionData[],
    strategy: "SMALL_FIRST" | "LARGE_FIRST",
  ): void {
    if (strategy === "SMALL_FIRST") {
      tokenOutputs.sort((a, b) => {
        return Number(
          bytesToNumberBE(a.output!.tokenAmount!) -
            bytesToNumberBE(b.output!.tokenAmount!),
        );
      });
    } else {
      tokenOutputs.sort((a, b) => {
        return Number(
          bytesToNumberBE(b.output!.tokenAmount!) -
            bytesToNumberBE(a.output!.tokenAmount!),
        );
      });
    }
  }

  // Helper function for deciding if the signer public key is the identity public key
  private async signMessageWithKey(
    message: Uint8Array,
    publicKey: Uint8Array,
  ): Promise<Uint8Array> {
    const tokenSignatures = this.config.getTokenSignatures();
    if (
      bytesToHex(publicKey) ===
      bytesToHex(await this.config.signer.getIdentityPublicKey())
    ) {
      if (tokenSignatures === "SCHNORR") {
        return await this.config.signer.signSchnorrWithIdentityKey(message);
      } else {
        return await this.config.signer.signMessageWithIdentityKey(message);
      }
    } else {
      if (tokenSignatures === "SCHNORR") {
        return await this.config.signer.signSchnorr(message, publicKey);
      } else {
        return await this.config.signer.signMessageWithPublicKey(
          message,
          publicKey,
        );
      }
    }
  }

  // Helper method to create operator-specific signatures for V0 transactions
  private async createOperatorSpecificSignaturesV0(
    finalTokenTransaction: TokenTransactionV0,
    finalTokenTransactionHash: Uint8Array,
    operator: SigningOperator,
  ): Promise<OperatorSpecificOwnerSignature[]> {
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
        if (this.config.getTokenSignatures() === "SCHNORR") {
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

    return operatorSpecificSignatures;
  }

  private async finalizeTokenTransaction(
    finalTokenTransaction: TokenTransactionV0,
    revocationSecrets: RevocationSecretWithIndex[],
    threshold: number,
  ): Promise<TokenTransactionV0> {
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
              retryableStatuses: [
                "UNKNOWN",
                "UNAVAILABLE",
                "CANCELLED",
                "INTERNAL",
              ],
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

  private async createSignaturesForOperators(
    finalTokenTransaction: TokenTransaction,
    finalTokenTransactionHash: Uint8Array,
    signingOperators: Record<string, SigningOperator>,
  ) {
    const inputTtxoSignaturesPerOperator: InputTtxoSignaturesPerOperator[] = [];

    for (const [_, operator] of Object.entries(signingOperators)) {
      let ttxoSignatures: SignatureWithIndex[] = [];

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

        const payload: OperatorSpecificTokenTransactionSignablePayload = {
          finalTokenTransactionHash: finalTokenTransactionHash,
          operatorIdentityPublicKey: hexToBytes(operator.identityPublicKey),
        };

        const payloadHash =
          await hashOperatorSpecificTokenTransactionSignablePayload(payload);

        const ownerSignature = await this.signMessageWithKey(
          payloadHash,
          issuerPublicKey,
        );

        ttxoSignatures.push({
          signature: ownerSignature,
          inputIndex: 0,
        });
      } else if (finalTokenTransaction.tokenInputs!.$case === "transferInput") {
        const transferInput = finalTokenTransaction.tokenInputs!.transferInput;

        // Create signatures for each input
        for (let i = 0; i < transferInput.outputsToSpend.length; i++) {
          const payload: OperatorSpecificTokenTransactionSignablePayload = {
            finalTokenTransactionHash: finalTokenTransactionHash,
            operatorIdentityPublicKey: hexToBytes(operator.identityPublicKey),
          };

          const payloadHash =
            await hashOperatorSpecificTokenTransactionSignablePayload(payload);

          let ownerSignature: Uint8Array;
          if (this.config.getTokenSignatures() === "SCHNORR") {
            ownerSignature =
              await this.config.signer.signSchnorrWithIdentityKey(payloadHash);
          } else {
            ownerSignature =
              await this.config.signer.signMessageWithIdentityKey(payloadHash);
          }

          ttxoSignatures.push({
            signature: ownerSignature,
            inputIndex: i,
          });
        }
      }

      inputTtxoSignaturesPerOperator.push({
        ttxoSignatures: ttxoSignatures,
        operatorIdentityPublicKey: hexToBytes(operator.identityPublicKey),
      });
    }

    return inputTtxoSignaturesPerOperator;
  }
}

function isTokenTransaction(
  tokenTransaction: TokenTransactionV0 | TokenTransaction,
): tokenTransaction is TokenTransaction {
  return "version" in tokenTransaction && "expiryTime" in tokenTransaction;
}
