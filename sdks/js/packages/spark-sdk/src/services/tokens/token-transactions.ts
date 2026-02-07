import {
  bytesToHex,
  bytesToNumberBE,
  numberToBytesBE,
} from "@noble/curves/utils";
import { hexToBytes } from "@noble/hashes/utils";
import { SparkRequestError, SparkValidationError } from "../../errors/types.js";
import {
  Direction,
  OperatorSpecificTokenTransactionSignablePayload,
  Order,
} from "../../proto/spark.js";
import {
  BroadcastTransactionResponse,
  CommitProgress,
  CommitStatus,
  CommitTransactionResponse,
  InputTtxoSignaturesPerOperator,
  OutputWithPreviousTransactionData,
  PartialTokenOutput,
  PartialTokenTransaction,
  QueryTokenTransactionsRequest,
  QueryTokenTransactionsResponse,
  SignatureWithIndex,
  TokenOutput,
  TokenTransaction,
} from "../../proto/spark_token.js";
import { TokenOutputsMap } from "../../spark-wallet/types.js";
import { SparkCallOptions } from "../../types/grpc.js";
import {
  decodeSparkAddress,
  isValidPublicKey,
  SparkAddressFormat,
} from "../../utils/address.js";
import {
  hashFinalTokenTransaction,
  hashOperatorSpecificTokenTransactionSignablePayload,
  hashPartialTokenTransaction,
  hashTokenTransaction,
  sortInvoiceAttachments,
} from "../../utils/token-hashing.js";
import {
  Bech32mTokenIdentifier,
  decodeBech32mTokenIdentifier,
} from "../../utils/token-identifier.js";
import { validateTokenTransaction } from "../../utils/token-transaction-validation.js";
import { sumTokenOutputs } from "../../utils/token-transactions.js";
import { WalletConfigService } from "../config.js";
import { ConnectionManager } from "../connection/connection.js";
import { SigningOperator } from "../wallet-config.js";

const QUERY_TOKEN_OUTPUTS_PAGE_SIZE = 100;
export const MAX_TOKEN_OUTPUTS_TX = 500;

export interface FetchOwnedTokenOutputsParams {
  ownerPublicKeys: Uint8Array[];
  issuerPublicKeys?: Uint8Array[];
  tokenIdentifiers?: Uint8Array[];
}

export interface QueryTokenTransactionsParams {
  sparkAddresses?: string[];
  ownerPublicKeys?: string[];
  issuerPublicKeys?: string[];
  tokenTransactionHashes?: string[];
  tokenIdentifiers?: string[];
  outputIds?: string[];
  order?: "asc" | "desc";
  pageSize?: number;
  offset?: number;
}

export interface QueryTokenTransactionsWithFiltersParams {
  sparkAddresses?: string[];
  issuerPublicKeys?: string[];
  tokenIdentifiers?: string[];
  outputIds?: string[];
  pageSize?: number;
  cursor?: string;
  direction?: "NEXT" | "PREVIOUS";
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
      throw new SparkValidationError("No receiver outputs provided", {
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
    } else {
      outputsToUse = this.selectTokenOutputs(
        tokenOutputs.get(tokenIdentifier)!!,
        totalTokenAmount,
        outputSelectionStrategy,
      );
    }

    if (outputsToUse.length > MAX_TOKEN_OUTPUTS_TX) {
      const availableOutputs = tokenOutputs.get(tokenIdentifier)!!;

      // Sort outputs by the same strategy as in selectTokenOutputs
      const sortedOutputs = [...availableOutputs];
      this.sortTokenOutputsByStrategy(sortedOutputs, outputSelectionStrategy);

      // Take only the first MAX_TOKEN_OUTPUTS and calculate their total
      const maxOutputsToUse = sortedOutputs.slice(0, MAX_TOKEN_OUTPUTS_TX);
      const maxAmount = sumTokenOutputs(maxOutputsToUse);

      throw new SparkValidationError(
        `Cannot transfer more than ${MAX_TOKEN_OUTPUTS_TX} TTXOs in a single transaction (${outputsToUse.length} selected). Maximum transferable amount is: ${maxAmount}`,
        {
          field: "outputsToUse",
          value: outputsToUse.length,
          expected: `Less than or equal to ${MAX_TOKEN_OUTPUTS_TX}, with maximum transferable amount of ${maxAmount}`,
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

    if (this.config.getTokenTransactionVersion() === "V2") {
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
    } else {
      const partialTokenTransaction =
        await this.constructPartialTransferTokenTransaction(
          outputsToUse,
          tokenOutputData,
          sparkInvoices,
        );

      const txId = await this.broadcastTokenTransactionV3(
        partialTokenTransaction,
        outputsToUse.map((output) => output.output!.ownerPublicKey),
      );

      return txId;
    }
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

    const availableTokenAmount = sumTokenOutputs(selectedOutputs);
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

    const sortedInvoiceAttachments = sparkInvoices
      ? sortInvoiceAttachments(
          sparkInvoices.map((invoice) => ({ sparkInvoice: invoice })),
        )
      : [];

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
      clientCreatedTimestamp: this.connectionManager.getCurrentServerTime(),
      invoiceAttachments: sortedInvoiceAttachments!,
    };
  }

  public async constructPartialTransferTokenTransaction(
    selectedOutputs: OutputWithPreviousTransactionData[],
    tokenOutputData: Array<{
      receiverPublicKey: Uint8Array;
      rawTokenIdentifier: Uint8Array;
      tokenAmount: bigint;
    }>,
    sparkInvoices?: SparkAddressFormat[],
  ): Promise<PartialTokenTransaction> {
    selectedOutputs.sort(
      (a, b) => a.previousTransactionVout - b.previousTransactionVout,
    );

    const availableTokenAmount = sumTokenOutputs(selectedOutputs);
    const totalRequestedAmount = tokenOutputData.reduce(
      (sum, output) => sum + output.tokenAmount,
      0n,
    );

    const partialTokenOutputs: PartialTokenOutput[] = tokenOutputData.map(
      (output): PartialTokenOutput => ({
        ownerPublicKey: output.receiverPublicKey,
        tokenIdentifier: output.rawTokenIdentifier,
        withdrawBondSats: this.config.getExpectedWithdrawBondSats(),
        withdrawRelativeBlockLocktime:
          this.config.getExpectedWithdrawRelativeBlockLocktime(),
        tokenAmount: numberToBytesBE(output.tokenAmount, 16),
      }),
    );

    if (availableTokenAmount > totalRequestedAmount) {
      const changeAmount = availableTokenAmount - totalRequestedAmount;
      const firstTokenIdentifierBytes = tokenOutputData[0]!!.rawTokenIdentifier;

      partialTokenOutputs.push({
        ownerPublicKey: await this.config.signer.getIdentityPublicKey(),
        tokenIdentifier: firstTokenIdentifierBytes,
        withdrawBondSats: this.config.getExpectedWithdrawBondSats(),
        withdrawRelativeBlockLocktime:
          this.config.getExpectedWithdrawRelativeBlockLocktime(),
        tokenAmount: numberToBytesBE(changeAmount, 16),
      });
    }

    const sortedInvoiceAttachments = sparkInvoices
      ? sortInvoiceAttachments(
          sparkInvoices.map((invoice) => ({ sparkInvoice: invoice })),
        )
      : [];

    return {
      version: 3,
      tokenTransactionMetadata: {
        network: this.config.getNetworkProto(),
        sparkOperatorIdentityPublicKeys:
          this.collectOperatorIdentityPublicKeys(),
        validityDurationSeconds:
          await this.config.getTokenValidityDurationSeconds(),
        clientCreatedTimestamp: this.connectionManager.getCurrentServerTime(),
        invoiceAttachments: sortedInvoiceAttachments!,
      },
      tokenInputs: {
        $case: "transferInput",
        transferInput: {
          outputsToSpend: selectedOutputs.map((output) => ({
            prevTokenTransactionHash: output.previousTransactionHash,
            prevTokenTransactionVout: output.previousTransactionVout,
          })),
        },
      },
      partialTokenOutputs,
    };
  }

  public collectOperatorIdentityPublicKeys(): Uint8Array[] {
    const operatorKeys: Uint8Array[] = [];
    for (const [_, operator] of Object.entries(
      this.config.getSigningOperators(),
    )) {
      operatorKeys.push(hexToBytes(operator.identityPublicKey));
    }

    operatorKeys.sort((a, b) => {
      const minLength = Math.min(a.length, b.length);
      for (let i = 0; i < minLength; i++) {
        if (a[i] !== b[i]) {
          return a[i]! - b[i]!;
        }
      }
      return a.length - b.length;
    });

    return operatorKeys;
  }

  public async broadcastTokenTransaction(
    tokenTransaction: TokenTransaction,
    outputsToSpendSigningPublicKeys?: Uint8Array[],
    outputsToSpendCommitments?: Uint8Array[],
  ): Promise<string> {
    const { finalTokenTransactionHash } =
      await this.broadcastTokenTransactionDetailed(
        tokenTransaction,
        outputsToSpendSigningPublicKeys,
        outputsToSpendCommitments,
      );

    return bytesToHex(finalTokenTransactionHash);
  }

  public async broadcastTokenTransactionDetailed(
    tokenTransaction: TokenTransaction,
    outputsToSpendSigningPublicKeys?: Uint8Array[],
    outputsToSpendCommitments?: Uint8Array[],
  ): Promise<{
    commitStatus: CommitStatus;
    commitProgress: CommitProgress | undefined;
    tokenIdentifier: Uint8Array | undefined;
    finalTokenTransaction: TokenTransaction;
    finalTokenTransactionHash: Uint8Array;
  }> {
    const signingOperators = this.config.getSigningOperators();

    const { finalTokenTransaction, finalTokenTransactionHash, threshold } =
      await this.startTokenTransaction(
        tokenTransaction,
        signingOperators,
        outputsToSpendSigningPublicKeys,
        outputsToSpendCommitments,
      );

    const { commitStatus, commitProgress, tokenIdentifier } =
      await this.signTokenTransaction(
        finalTokenTransaction,
        finalTokenTransactionHash,
        signingOperators,
      );
    return {
      commitStatus,
      commitProgress,
      tokenIdentifier,
      finalTokenTransaction,
      finalTokenTransactionHash,
    };
  }

  public async broadcastTokenTransactionV3(
    partialTokenTransaction: PartialTokenTransaction,
    outputsToSpendSigningPublicKeys?: Uint8Array[],
  ): Promise<string> {
    const broadcastResponse = await this.broadcastTokenTransactionV3Detailed(
      partialTokenTransaction,
      outputsToSpendSigningPublicKeys,
    );

    const finalHash = await hashFinalTokenTransaction(
      broadcastResponse.finalTokenTransaction!,
    );

    return bytesToHex(finalHash);
  }

  public async broadcastTokenTransactionV3Detailed(
    partialTokenTransaction: PartialTokenTransaction,
    outputsToSpendSigningPublicKeys?: Uint8Array[],
  ): Promise<BroadcastTransactionResponse> {
    const sparkClient = await this.connectionManager.createSparkTokenClient(
      this.config.getCoordinatorAddress(),
    );

    const partialTokenTransactionHash = await hashPartialTokenTransaction(
      partialTokenTransaction,
    );

    const ownerSignaturesWithIndex: SignatureWithIndex[] = [];

    if (partialTokenTransaction.tokenInputs?.$case === "mintInput") {
      const ownerPubkey =
        partialTokenTransaction.partialTokenOutputs[0]?.ownerPublicKey;
      if (!ownerPubkey) {
        throw new SparkValidationError("Invalid mint input", {
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
    } else if (partialTokenTransaction.tokenInputs?.$case === "createInput") {
      const issuerPublicKey =
        partialTokenTransaction.tokenInputs.createInput.issuerPublicKey;
      if (!issuerPublicKey) {
        throw new SparkValidationError("Invalid create input", {
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
    } else if (partialTokenTransaction.tokenInputs?.$case === "transferInput") {
      if (!outputsToSpendSigningPublicKeys) {
        throw new SparkValidationError("Invalid transfer input", {
          field: "outputsToSpend",
          value: {
            signingPublicKeys: outputsToSpendSigningPublicKeys,
          },
          expected: "Non-null signing public keys",
        });
      }

      for (const [i, key] of outputsToSpendSigningPublicKeys.entries()) {
        if (!key) {
          throw new SparkValidationError("Invalid signing key", {
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

    return await sparkClient.broadcast_transaction(
      {
        identityPublicKey: await this.config.signer.getIdentityPublicKey(),
        partialTokenTransaction,
        tokenTransactionOwnerSignatures: ownerSignaturesWithIndex,
      },
      {
        retry: true,
        retryableStatuses: ["UNKNOWN", "UNAVAILABLE", "CANCELLED", "INTERNAL"],
        retryMaxAttempts: 3,
      } as SparkCallOptions,
    );
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
        throw new SparkValidationError("Invalid mint input", {
          field: "tokenIdentifier",
          value: null,
          expected: "Non-null tokenIdentifier",
        });
      }
      const ownerPubkey = tokenTransaction.tokenOutputs[0]!.ownerPublicKey;
      if (!ownerPubkey) {
        throw new SparkValidationError("Invalid mint input", {
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
        throw new SparkValidationError("Invalid create input", {
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
        throw new SparkValidationError("Invalid transfer input", {
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
          throw new SparkValidationError("Invalid signing key", {
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
  ): Promise<CommitTransactionResponse> {
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
      return await coordinatorClient.commit_transaction(
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
      throw new SparkRequestError("Failed to sign token transaction", {
        operation: "commit_transaction",
        error,
      });
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
      throw new SparkValidationError("Owner public keys cannot be empty", {
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
        throw new SparkValidationError(
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
      throw new SparkRequestError("Failed to fetch owned token outputs", {
        operation: "query_token_outputs",
        error,
      });
    }
  }

  public async queryTokenTransactions(
    params: QueryTokenTransactionsParams,
  ): Promise<QueryTokenTransactionsResponse> {
    const {
      sparkAddresses,
      ownerPublicKeys,
      issuerPublicKeys,
      tokenTransactionHashes,
      tokenIdentifiers,
      outputIds,
      order,
      pageSize,
      offset,
    } = params;

    const decodedOwnerPublicKeys = sparkAddresses?.map((address) => {
      const decoded = decodeSparkAddress(address, this.config.getNetworkType());
      return decoded.identityPublicKey;
    });

    const allOwnerPublicKeys = [
      ...(decodedOwnerPublicKeys || []),
      ...(ownerPublicKeys || []),
    ];

    const tokenClient = await this.connectionManager.createSparkTokenClient(
      this.config.getCoordinatorAddress(),
    );

    let queryParams: QueryTokenTransactionsRequest = {
      queryType: undefined,
      issuerPublicKeys: issuerPublicKeys?.map(hexToBytes)!,
      ownerPublicKeys:
        allOwnerPublicKeys.length > 0
          ? allOwnerPublicKeys.map(hexToBytes)
          : undefined!,
      tokenIdentifiers: tokenIdentifiers?.map((identifier) => {
        const { tokenIdentifier } = decodeBech32mTokenIdentifier(
          identifier as Bech32mTokenIdentifier,
          this.config.getNetworkType(),
        );
        return tokenIdentifier;
      })!,
      tokenTransactionHashes: tokenTransactionHashes?.map(hexToBytes)!,
      outputIds: outputIds || [],
      order: order === "asc" ? Order.ASCENDING : Order.DESCENDING,
      limit: pageSize!,
      offset: offset!,
    };

    try {
      return await tokenClient.query_token_transactions(queryParams);
    } catch (error) {
      throw new SparkRequestError("Failed to query token transactions", {
        operation: "query_token_transactions",
        error,
      });
    }
  }

  public async queryTokenTransactionsByTxHashes(
    tokenTransactionHashes: string[],
  ): Promise<QueryTokenTransactionsResponse> {
    const tokenClient = await this.connectionManager.createSparkTokenClient(
      this.config.getCoordinatorAddress(),
    );

    let queryParams: QueryTokenTransactionsRequest = {
      queryType: {
        $case: "byTxHash",
        byTxHash: {
          tokenTransactionHashes: tokenTransactionHashes.map(hexToBytes)!,
        },
      },
      outputIds: [],
      ownerPublicKeys: [],
      issuerPublicKeys: [],
      tokenIdentifiers: [],
      tokenTransactionHashes: [],
      order: Order.UNRECOGNIZED,
      limit: 0,
      offset: 0,
    };

    try {
      return await tokenClient.query_token_transactions(queryParams);
    } catch (error) {
      throw new SparkRequestError("Failed to query token transactions", {
        operation: "query_token_transactions",
        error,
      });
    }
  }

  public async queryTokenTransactionsWithFilters(
    params: QueryTokenTransactionsWithFiltersParams,
  ): Promise<QueryTokenTransactionsResponse> {
    const {
      sparkAddresses,
      issuerPublicKeys,
      tokenIdentifiers,
      outputIds,
      pageSize,
      cursor,
      direction,
    } = params;

    const decodedOwnerPublicKeys: string[] | undefined = sparkAddresses?.map(
      (address) => {
        const decoded = decodeSparkAddress(
          address,
          this.config.getNetworkType(),
        );
        return decoded.identityPublicKey;
      },
    );

    const tokenClient = await this.connectionManager.createSparkTokenClient(
      this.config.getCoordinatorAddress(),
    );

    let queryParams: QueryTokenTransactionsRequest = {
      queryType: {
        $case: "byFilters",
        byFilters: {
          outputIds: outputIds || [],
          ownerPublicKeys: decodedOwnerPublicKeys?.map(hexToBytes)!,
          issuerPublicKeys: issuerPublicKeys?.map(hexToBytes)!,
          tokenIdentifiers: tokenIdentifiers?.map((identifier) => {
            const { tokenIdentifier } = decodeBech32mTokenIdentifier(
              identifier as Bech32mTokenIdentifier,
              this.config.getNetworkType(),
            );
            return tokenIdentifier;
          })!,
          pageRequest: {
            unsafePageSize: 0,
            pageSize: pageSize ?? 50,
            cursor: cursor ?? "",
            direction:
              direction === "PREVIOUS" ? Direction.PREVIOUS : Direction.NEXT,
          },
        },
      },
      outputIds: [],
      ownerPublicKeys: [],
      issuerPublicKeys: [],
      tokenIdentifiers: [],
      tokenTransactionHashes: [],
      order: Order.UNRECOGNIZED,
      limit: 0,
      offset: 0,
    };

    try {
      return await tokenClient.query_token_transactions(queryParams);
    } catch (error) {
      throw new SparkRequestError("Failed to query token transactions", {
        operation: "query_token_transactions",
        error,
      });
    }
  }

  public selectTokenOutputs(
    tokenOutputs: OutputWithPreviousTransactionData[],
    tokenAmount: bigint,
    strategy: "SMALL_FIRST" | "LARGE_FIRST",
  ): OutputWithPreviousTransactionData[] {
    if (tokenAmount <= 0n) {
      throw new SparkValidationError("Token amount must be greater than 0", {
        field: "tokenAmount",
        value: tokenAmount,
        expected: "Greater than 0",
      });
    }

    if (sumTokenOutputs(tokenOutputs) < tokenAmount) {
      throw new SparkValidationError("Insufficient token amount", {
        field: "tokenAmount",
        value: sumTokenOutputs(tokenOutputs),
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

    // Sort outputs: smallest first for SMALL_FIRST, largest first for LARGE_FIRST
    const sortedOutputs = [...tokenOutputs].sort((a, b) => {
      const amountA = bytesToNumberBE(a.output!.tokenAmount!);
      const amountB = bytesToNumberBE(b.output!.tokenAmount!);
      return strategy === "SMALL_FIRST"
        ? Number(amountA - amountB)
        : Number(amountB - amountA);
    });

    // SMALL_FIRST strategy: Maximize use of small outputs while staying within MAX_OUTPUTS limit
    if (strategy === "SMALL_FIRST") {
      // First, try to use only the smallest outputs
      let sum = 0n;
      let count = 0;
      for (const output of sortedOutputs) {
        sum += bytesToNumberBE(output.output!.tokenAmount!);
        count++;
        if (sum >= tokenAmount) {
          // We can reach the target with outputs
          return sortedOutputs.slice(0, count);
        }
        if (count >= MAX_TOKEN_OUTPUTS_TX) break;
      }

      // If we've gone through all outputs and still don't have enough, check if we have more outputs available
      if (count >= sortedOutputs.length) {
        // No more outputs available - this should have been caught by the earlier check
        throw new SparkValidationError("Insufficient funds", {
          field: "tokenAmount",
          value: sum,
          expected: tokenAmount,
        });
      }

      // If we reached MAX_OUTPUTS but don't have enough, we need to swap some small
      // outputs for larger ones
      const smallOutputs = sortedOutputs.slice(0, MAX_TOKEN_OUTPUTS_TX);
      const largeOutputs = sortedOutputs.slice(MAX_TOKEN_OUTPUTS_TX).reverse(); // Largest first

      let smallSum = smallOutputs.reduce(
        (acc, output) => acc + bytesToNumberBE(output.output!.tokenAmount!),
        0n,
      );

      const selectedOutputs = [...smallOutputs];

      // While we haven't reached the target, swap the smallest output for a larger one
      let largeIdx = 0;
      while (smallSum < tokenAmount && largeIdx < largeOutputs.length) {
        const largeOutput = largeOutputs[largeIdx]!;
        const largeAmount = bytesToNumberBE(largeOutput.output!.tokenAmount!);

        // Remove the smallest output from selection
        const smallestOutput = selectedOutputs.shift()!;
        const smallestAmount = bytesToNumberBE(
          smallestOutput.output!.tokenAmount!,
        );

        selectedOutputs.push(largeOutput);

        smallSum = smallSum - smallestAmount + largeAmount;
        largeIdx++;
      }

      if (smallSum < tokenAmount) {
        throw new SparkValidationError("Insufficient funds", {
          field: "tokenAmount",
          value: smallSum,
          expected: tokenAmount,
        });
      }

      return selectedOutputs;
    } else {
      // LARGE_FIRST strategy: simple greedy approach
      const selectedOutputs: typeof sortedOutputs = [];
      let remainingAmount = tokenAmount;

      for (const output of sortedOutputs) {
        if (remainingAmount <= 0n) break;
        if (selectedOutputs.length >= MAX_TOKEN_OUTPUTS_TX) break;

        selectedOutputs.push(output);
        remainingAmount -= bytesToNumberBE(output.output!.tokenAmount!);
      }

      if (remainingAmount > 0n) {
        throw new SparkValidationError("Insufficient funds", {
          field: "remainingAmount",
          value: remainingAmount,
        });
      }

      return selectedOutputs;
    }
  }

  private sortTokenOutputsByStrategy(
    tokenOutputs: OutputWithPreviousTransactionData[],
    strategy: "SMALL_FIRST" | "LARGE_FIRST",
  ): void {
    if (strategy === "SMALL_FIRST") {
      tokenOutputs.sort((a, b) => {
        const amountA = bytesToNumberBE(a.output!.tokenAmount!);
        const amountB = bytesToNumberBE(b.output!.tokenAmount!);

        return amountA < amountB ? -1 : amountA > amountB ? 1 : 0;
      });
    } else {
      tokenOutputs.sort((a, b) => {
        const amountA = bytesToNumberBE(a.output!.tokenAmount!);
        const amountB = bytesToNumberBE(b.output!.tokenAmount!);

        return amountB < amountA ? -1 : amountB > amountA ? 1 : 0;
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
      throw new SparkValidationError("Invalid public key", {
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
          throw new SparkValidationError("Invalid mint input", {
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
          throw new SparkValidationError("Invalid create input", {
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
