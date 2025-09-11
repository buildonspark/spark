import {
  bytesToHex,
  bytesToNumberBE,
  numberToBytesBE,
} from "@noble/curves/utils";
import { hexToBytes } from "@noble/hashes/utils";
import { NetworkError, ValidationError } from "../errors/types.js";
import {
  Direction,
  OperatorSpecificTokenTransactionSignablePayload,
  OutputWithPreviousTransactionData,
  RevocationSecretWithIndex,
} from "../proto/spark.js";
import {
  InputTtxoSignaturesPerOperator,
  QueryTokenTransactionsRequest as QueryTokenTransactionsRequestV1,
  QueryTokenTransactionsResponse,
  SignatureWithIndex,
  TokenOutput,
  TokenTransaction,
} from "../proto/spark_token.js";
import { TokenOutputsMap } from "../spark-wallet/types.js";
import { SparkCallOptions } from "../types/grpc.js";
import {
  decodeSparkAddress,
  SparkAddressFormat,
  isValidPublicKey,
} from "../utils/address.js";
import { collectResponses } from "../utils/response-validation.js";
import {
  hashOperatorSpecificTokenTransactionSignablePayload,
  hashTokenTransaction,
} from "../utils/token-hashing.js";
import {
  Bech32mTokenIdentifier,
  decodeBech32mTokenIdentifier,
} from "../utils/token-identifier.js";
import { validateTokenTransaction } from "../utils/token-transaction-validation.js";
import {
  checkIfSelectedOutputsAreAvailable,
  sumAvailableTokens,
} from "../utils/token-transactions.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection.js";
import { SigningOperator } from "./wallet-config.js";

const MAX_TOKEN_OUTPUTS = 500;
const QUERY_TOKEN_OUTPUTS_PAGE_SIZE = 100;

export interface FetchOwnedTokenOutputsParams {
  ownerPublicKeys: Uint8Array[];
  issuerPublicKeys?: Uint8Array[];
  tokenIdentifiers?: Uint8Array[];
}

export interface QueryTokenTransactionsParams {
  ownerPublicKeys?: string[];
  issuerPublicKeys?: string[];
  tokenTransactionHashes?: string[];
  tokenIdentifiers?: string[];
  outputIds?: string[];
  pageSize?: number;
  offset?: number;
}

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

  public async tokenTransfer({
    tokenOutputs,
    receiverOutputs,
    outputSelectionStrategy = "SMALL_FIRST",
    selectedOutputs,
  }: {
    tokenOutputs: TokenOutputsMap;
    receiverOutputs: {
      tokenIdentifier: Bech32mTokenIdentifier;
      tokenAmount: bigint;
      receiverSparkAddress: string;
    }[];
    outputSelectionStrategy?: "SMALL_FIRST" | "LARGE_FIRST";
    selectedOutputs?: OutputWithPreviousTransactionData[];
  }): Promise<string> {
    if (!Array.isArray(receiverOutputs) || receiverOutputs.length === 0) {
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

    const tokenIdentifier: Bech32mTokenIdentifier =
      receiverOutputs[0]!!.tokenIdentifier;

    if (selectedOutputs) {
      outputsToUse = selectedOutputs;

      if (
        !checkIfSelectedOutputsAreAvailable(
          outputsToUse,
          tokenOutputs,
          tokenIdentifier,
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
        tokenOutputs.get(tokenIdentifier)!!,
        totalTokenAmount,
        outputSelectionStrategy,
      );
    }

    if (outputsToUse.length > MAX_TOKEN_OUTPUTS) {
      const availableOutputs = tokenOutputs.get(tokenIdentifier)!!;

      // Sort outputs by the same strategy as in selectTokenOutputs
      const sortedOutputs = [...availableOutputs];
      this.sortTokenOutputsByStrategy(sortedOutputs, outputSelectionStrategy);

      // Take only the first MAX_TOKEN_OUTPUTS and calculate their total
      const maxOutputsToUse = sortedOutputs.slice(0, MAX_TOKEN_OUTPUTS);
      const maxAmount = sumAvailableTokens(maxOutputsToUse);

      throw new ValidationError(
        `Cannot transfer more than ${MAX_TOKEN_OUTPUTS} TTXOs in a single transaction (${outputsToUse.length} selected). Maximum transferable amount is: ${maxAmount}`,
        {
          field: "outputsToUse",
          value: outputsToUse.length,
          expected: `Less than or equal to ${MAX_TOKEN_OUTPUTS}, with maximum transferable amount of ${maxAmount}`,
        },
      );
    }

    const rawTokenIdentifier: Uint8Array = decodeBech32mTokenIdentifier(
      tokenIdentifier,
      this.config.getNetworkType(),
    ).tokenIdentifier;

    let sparkInvoices: SparkAddressFormat[] = [];

    const tokenOutputData = receiverOutputs.map((transfer) => {
      const receiverAddress = decodeSparkAddress(
        transfer.receiverSparkAddress,
        this.config.getNetworkType(),
      );

      if (receiverAddress.sparkInvoiceFields) {
        sparkInvoices.push(transfer.receiverSparkAddress as SparkAddressFormat);
      }

      if (receiverAddress.sparkInvoiceFields) {
        return {
          receiverPublicKey: hexToBytes(receiverAddress.identityPublicKey),
          rawTokenIdentifier,
          tokenAmount: transfer.tokenAmount,
          sparkInvoice: transfer.receiverSparkAddress,
        };
      }

      return {
        receiverPublicKey: hexToBytes(receiverAddress.identityPublicKey),
        rawTokenIdentifier,
        tokenAmount: transfer.tokenAmount,
      };
    });

    const tokenTransaction = await this.constructTransferTokenTransaction(
      outputsToUse,
      tokenOutputData,
      sparkInvoices,
    );

    const txId = await this.broadcastTokenTransaction(
      tokenTransaction,
      outputsToUse.map((output) => output.output!.ownerPublicKey),
      outputsToUse.map((output) => output.output!.revocationCommitment!),
    );

    return txId;
  }

  public async constructTransferTokenTransaction(
    selectedOutputs: OutputWithPreviousTransactionData[],
    tokenOutputData: Array<{
      receiverPublicKey: Uint8Array;
      rawTokenIdentifier: Uint8Array;
      tokenAmount: bigint;
    }>,
    sparkInvoices?: SparkAddressFormat[],
  ): Promise<TokenTransaction> {
    selectedOutputs.sort(
      (a, b) => a.previousTransactionVout - b.previousTransactionVout,
    );

    const availableTokenAmount = sumAvailableTokens(selectedOutputs);
    const totalRequestedAmount = tokenOutputData.reduce(
      (sum, output) => sum + output.tokenAmount,
      0n,
    );

    const tokenOutputs: TokenOutput[] = tokenOutputData.map(
      (output): TokenOutput => ({
        ownerPublicKey: output.receiverPublicKey,
        tokenIdentifier: output.rawTokenIdentifier,
        tokenAmount: numberToBytesBE(output.tokenAmount, 16),
      }),
    );

    if (availableTokenAmount > totalRequestedAmount) {
      const changeAmount = availableTokenAmount - totalRequestedAmount;
      const firstTokenIdentifierBytes = tokenOutputData[0]!!.rawTokenIdentifier;

      tokenOutputs.push({
        ownerPublicKey: await this.config.signer.getIdentityPublicKey(),
        tokenIdentifier: firstTokenIdentifierBytes,
        tokenAmount: numberToBytesBE(changeAmount, 16),
      });
    }

    return {
      version: 2,
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
      invoiceAttachments: sparkInvoices
        ? sparkInvoices.map((invoice) => ({ sparkInvoice: invoice }))
        : [],
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

    await this.signTokenTransaction(
      finalTokenTransaction,
      finalTokenTransactionHash,
      signingOperators,
    );

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
    const sparkClient = await this.connectionManager.createSparkTokenClient(
      this.config.getCoordinatorAddress(),
    );

    const partialTokenTransactionHash = hashTokenTransaction(
      tokenTransaction,
      true,
    );

    const ownerSignaturesWithIndex: SignatureWithIndex[] = [];
    if (tokenTransaction.tokenInputs!.$case === "mintInput") {
      const tokenIdentifier =
        tokenTransaction.tokenInputs!.mintInput.tokenIdentifier;
      if (!tokenIdentifier) {
        throw new ValidationError("Invalid mint input", {
          field: "tokenIdentifier",
          value: null,
          expected: "Non-null tokenIdentifier",
        });
      }
      const ownerPubkey = tokenTransaction.tokenOutputs[0]!.ownerPublicKey;
      if (!ownerPubkey) {
        throw new ValidationError("Invalid mint input", {
          field: "ownerPubkey",
          value: null,
          expected: "Non-null ownerPubkey",
        });
      }

      const ownerSignature = await this.signMessageWithKey(
        partialTokenTransactionHash,
        ownerPubkey,
      );

      ownerSignaturesWithIndex.push({
        signature: ownerSignature,
        inputIndex: 0,
      });
    } else if (tokenTransaction.tokenInputs!.$case === "createInput") {
      const issuerPublicKey =
        tokenTransaction.tokenInputs!.createInput.issuerPublicKey;
      if (!issuerPublicKey) {
        throw new ValidationError("Invalid create input", {
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
    params: FetchOwnedTokenOutputsParams,
  ): Promise<OutputWithPreviousTransactionData[]> {
    const {
      ownerPublicKeys,
      issuerPublicKeys = [],
      tokenIdentifiers = [],
    } = params;

    if (ownerPublicKeys.length === 0) {
      throw new ValidationError("Owner public keys cannot be empty", {
        field: "ownerPublicKeys",
        value: ownerPublicKeys,
        expected: "Non-empty array",
      });
    }
    for (const ownerPublicKey of ownerPublicKeys) {
      isValidPublicKey(bytesToHex(ownerPublicKey));
    }
    for (const issuerPublicKey of issuerPublicKeys) {
      isValidPublicKey(bytesToHex(issuerPublicKey));
    }
    for (const tokenIdentifier of tokenIdentifiers) {
      if (tokenIdentifier.length !== 32) {
        throw new ValidationError(
          "Token identifier must be 32 bytes (64 hex characters) long.",
          {
            field: "tokenIdentifier",
            value: tokenIdentifier,
            expected: "32 bytes",
          },
        );
      }
    }

    const tokenClient = await this.connectionManager.createSparkTokenClient(
      this.config.getCoordinatorAddress(),
    );

    try {
      const allOutputs: OutputWithPreviousTransactionData[] = [];
      let after: string | undefined = undefined;

      do {
        const result = await tokenClient.query_token_outputs({
          ownerPublicKeys,
          issuerPublicKeys,
          tokenIdentifiers,
          network: this.config.getNetworkProto(),
          pageRequest: {
            pageSize: QUERY_TOKEN_OUTPUTS_PAGE_SIZE,
            cursor: after,
            direction: Direction.NEXT,
          },
        });

        if (Array.isArray(result.outputsWithPreviousTransactionData)) {
          allOutputs.push(...result.outputsWithPreviousTransactionData);
        }

        if (result.pageResponse?.hasNextPage) {
          after = result.pageResponse.nextCursor;
        } else {
          break;
        }
      } while (after);

      return allOutputs;
    } catch (error) {
      throw new NetworkError(
        "Failed to fetch owned token outputs",
        {
          operation: "spark_token.query_token_outputs",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
        error as Error,
      );
    }
  }

  public async queryTokenTransactions(
    params: QueryTokenTransactionsParams,
  ): Promise<QueryTokenTransactionsResponse> {
    const {
      ownerPublicKeys,
      issuerPublicKeys,
      tokenTransactionHashes,
      tokenIdentifiers,
      outputIds,
      pageSize,
      offset,
    } = params;

    const tokenClient = await this.connectionManager.createSparkTokenClient(
      this.config.getCoordinatorAddress(),
    );

    let queryParams: QueryTokenTransactionsRequestV1 = {
      issuerPublicKeys: issuerPublicKeys?.map(hexToBytes)!,
      ownerPublicKeys: ownerPublicKeys?.map(hexToBytes)!,
      tokenIdentifiers: tokenIdentifiers?.map((identifier) => {
        const { tokenIdentifier } = decodeBech32mTokenIdentifier(
          identifier as Bech32mTokenIdentifier,
          this.config.getNetworkType(),
        );
        return tokenIdentifier;
      })!,
      tokenTransactionHashes: tokenTransactionHashes?.map(hexToBytes)!,
      outputIds: outputIds || [],
      limit: pageSize!,
      offset: offset!,
    };

    try {
      return await tokenClient.query_token_transactions(queryParams);
    } catch (error) {
      throw new NetworkError(
        "Failed to query token transactions",
        {
          operation: "spark_token.query_token_transactions",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
        error as Error,
      );
    }
  }

  public selectTokenOutputs(
    tokenOutputs: OutputWithPreviousTransactionData[],
    tokenAmount: bigint,
    strategy: "SMALL_FIRST" | "LARGE_FIRST",
  ): OutputWithPreviousTransactionData[] {
    if (tokenAmount <= 0n) {
      throw new ValidationError("Token amount must be greater than 0", {
        field: "tokenAmount",
        value: tokenAmount,
        expected: "Greater than 0",
      });
    }

    if (sumAvailableTokens(tokenOutputs) < tokenAmount) {
      throw new ValidationError("Insufficient token amount", {
        field: "tokenAmount",
        value: sumAvailableTokens(tokenOutputs),
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
      throw new ValidationError("Invalid public key", {
        field: "publicKey",
        value: bytesToHex(publicKey),
        expected: bytesToHex(await this.config.signer.getIdentityPublicKey()),
      });
    }
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
      } else if (finalTokenTransaction.tokenInputs!.$case === "createInput") {
        const issuerPublicKey =
          finalTokenTransaction.tokenInputs!.createInput.issuerPublicKey;
        if (!issuerPublicKey) {
          throw new ValidationError("Invalid create input", {
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
