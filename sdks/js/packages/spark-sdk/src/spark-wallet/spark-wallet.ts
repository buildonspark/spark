import { CurrencyUnit } from "@lightsparkdev/core";
import {
  bytesToHex,
  bytesToNumberBE,
  equalBytes,
  hexToBytes,
  numberToVarBytesBE,
} from "@noble/curves/utils";
import { sha256 } from "@noble/hashes/sha2";
import { type Tracer } from "@opentelemetry/api";
import {
  ConsoleSpanExporter,
  SimpleSpanProcessor,
  SpanProcessor,
} from "@opentelemetry/sdk-trace-base";
import { validateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { Mutex } from "async-mutex";
import { EventEmitter } from "eventemitter3";
import { ClientError, Status } from "nice-grpc-common";
import { uuidv7, uuidv7obj } from "uuidv7";
import { isReactNative } from "../constants.js";
import {
  SparkError,
  SparkRequestError,
  SparkValidationError,
} from "../errors/index.js";
import SspClient, {
  GetUserRequestsParams,
  TransferWithUserRequest,
} from "../graphql/client.js";
import {
  BitcoinNetwork,
  ClaimStaticDepositOutput,
  CoopExitFeeQuote,
  CoopExitRequest,
  ExitSpeed,
  LeavesSwapFeeEstimateOutput,
  LightningReceiveRequest,
  LightningSendFeeEstimateInput,
  LightningSendRequest,
  RequestCoopExitInput,
  SparkLeavesSwapRequestStatus,
  SparkWalletUserToUserRequestsConnection,
  StaticDepositQuoteOutput,
  UserLeafInput,
} from "../graphql/objects/index.js";
import {
  ConnectedEvent,
  DepositAddressQueryResult,
  Network as NetworkProto,
  networkToJSON,
  PreimageRequestRole,
  PreimageRequestStatus,
  QueryHtlcResponse,
  QueryNodesRequest,
  QueryNodesResponse,
  QuerySparkInvoicesResponse,
  SigningJob,
  SubscribeToEventsResponse,
  Transfer,
  TransferStatus,
  TransferType,
  TreeNode,
  TreeNodeStatus,
  UtxoSwapRequestType,
} from "../proto/spark.js";
import {
  OutputWithPreviousTransactionData,
  QueryTokenTransactionsResponse,
} from "../proto/spark_token.js";
import {
  decodeInvoice,
  getNetworkFromInvoice,
  isValidSparkAddressFallback,
} from "../services/bolt11-spark.js";
import { WalletConfigService } from "../services/config.js";
import { ConnectionManager } from "../services/connection/connection.js";
import { CoopExitService } from "../services/coop-exit.js";
import { DepositService } from "../services/deposit.js";
import { LightningService } from "../services/lightning.js";
import { SigningService } from "../services/signing.js";
import { TokenOutputManager } from "../services/tokens/output-manager.js";
import {
  MAX_TOKEN_OUTPUTS_TX,
  QueryTokenTransactionsWithFiltersParams,
  TokenTransactionService,
} from "../services/tokens/token-transactions.js";
import type { LeafKeyTweak } from "../services/transfer.js";
import { TransferService } from "../services/transfer.js";
import {
  ConfigOptions,
  ELECTRS_CREDENTIALS,
} from "../services/wallet-config.js";
import { DefaultSparkSigner, SparkSigner } from "../signer/signer.js";
import { KeyDerivation, KeyDerivationType } from "../signer/types.js";
import { BitcoinFaucet } from "../tests/utils/test-faucet.js";
import { Interval } from "../types/index.js";
import {
  mapSettingsProtoToWalletSettings,
  mapTransferToWalletTransfer,
  mapTreeNodeToWalletLeaf,
  UserRequestType,
  WalletLeaf,
  WalletSettings,
  WalletTransfer,
} from "../types/sdk-types.js";
import {
  decodeSparkAddress,
  encodeSparkAddress,
  encodeSparkAddressWithSignature,
  getNetworkFromSparkAddress,
  isLegacySparkAddress,
  isSafeForNumber,
  SparkAddressFormat,
  validateSparkInvoiceFields,
} from "../utils/address.js";
import {
  getP2TRScriptFromPublicKey,
  getP2WPKHAddressFromPublicKey,
  getSigHashFromTx,
  getTxEstimatedVbytesSizeByNumberOfInputsOutputs,
  getTxFromRawTxBytes,
  getTxFromRawTxHex,
  getTxId,
} from "../utils/bitcoin.js";
import { chunkArray } from "../utils/chunkArray.js";
import { getFetch } from "../utils/fetch.js";
import {
  createReceiverSpendTx,
  createSenderSpendTx,
} from "../utils/htlc-transactions.js";
import { HashSparkInvoice } from "../utils/invoice-hashing.js";
import { addPublicKeys } from "../utils/keys.js";
import {
  getNetwork,
  Network,
  NetworkToProto,
  NetworkType,
} from "../utils/network.js";
import { optimize, shouldOptimize } from "../utils/optimize.js";
import { RetryContext, withRetry } from "../utils/retry.js";
import {
  Bech32mTokenIdentifier,
  decodeBech32mTokenIdentifier,
  encodeBech32mTokenIdentifier,
} from "../utils/token-identifier.js";
import { sumTokenOutputs } from "../utils/token-transactions.js";
import { doesTxnNeedRenewed, isZeroTimelock } from "../utils/transaction.js";
import type {
  CreateHTLCParams,
  CreateLightningHodlInvoiceParams,
  CreateLightningInvoiceParams,
  DepositParams,
  FulfillSparkInvoiceResponse,
  GroupSparkInvoicesResult,
  HandlePublicMethodErrorParams,
  InitWalletResponse,
  InvalidInvoice,
  PayLightningInvoiceParams,
  SparkWalletEvents,
  SparkWalletEventType,
  SparkWalletProps,
  TokenBalanceMap,
  TokenInvoice,
  TokenMetadataMap,
  TokenOutputsMap,
  TransferParams,
  TransferWithInvoiceOutcome,
  TransferWithInvoiceParams,
  UserTokenMetadata,
  WithdrawParams,
} from "./types.js";
import { SparkWalletEvent } from "./types.js";
import type { DecodedInvoice } from "../services/bolt11-spark.js";

/**
 * The SparkWallet class is the primary interface for interacting with the Spark network.
 * It provides methods for creating and managing wallets, handling deposits, executing transfers,
 * and interacting with the Lightning Network.
 */
export abstract class SparkWallet extends EventEmitter<SparkWalletEvents> {
  protected config: WalletConfigService;
  protected connectionManager: ConnectionManager;
  protected coopExitService: CoopExitService;
  protected depositService: DepositService;
  protected lightningService: LightningService;
  protected signingService: SigningService;
  protected sspClient: SspClient | null = null;
  protected tokenTransactionService: TokenTransactionService;
  protected transferService: TransferService;

  private claimTransferMutex = new Mutex();
  private claimTransfersInterval: Interval | null = null;
  private leavesMutex = new Mutex();
  private mutexes: Map<string, Mutex> = new Map();
  private optimizationInProgress = false;
  private sparkAddress: SparkAddressFormat | undefined;
  private streamController: AbortController | null = null;
  private tokenOptimizationInProgress = false;
  private tokenOptimizationInterval: Interval | null = null;
  private tokenOutputManager: TokenOutputManager;

  protected leaves: TreeNode[] = [];
  protected tokenMetadata: TokenMetadataMap = new Map();
  protected tracer: Tracer | null = null;
  protected tracerId = "spark-sdk";

  protected abstract buildConnectionManager(
    config: WalletConfigService,
  ): ConnectionManager;

  constructor(options?: ConfigOptions, signerArg?: SparkSigner) {
    super();
    const signer = signerArg || new DefaultSparkSigner();
    this.config = new WalletConfigService(options, signer);
    const events = this.config.getEvents();
    if (Object.keys(events).length > 0) {
      Object.entries(events).forEach(([event, handler]) => {
        this.on(
          event as SparkWalletEventType,
          handler as (...args: unknown[]) => void,
        );
      });
    }
    this.connectionManager = this.buildConnectionManager(this.config);
    this.signingService = new SigningService(this.config);
    this.depositService = new DepositService(
      this.config,
      this.connectionManager,
    );
    this.transferService = new TransferService(
      this.config,
      this.connectionManager,
      this.signingService,
    );
    this.tokenTransactionService = new TokenTransactionService(
      this.config,
      this.connectionManager,
    );
    this.tokenOutputManager = new TokenOutputManager(
      this.config.getTokenOutputLockExpiryMs(),
    );
    this.lightningService = new LightningService(
      this.config,
      this.connectionManager,
      this.signingService,
    );
    this.coopExitService = new CoopExitService(
      this.config,
      this.connectionManager,
      this.signingService,
    );

    if (this.getTracer()) {
      this.initializeTracer(this);
    }
    this.wrapPublicMethods();
  }

  public static async initialize<T extends SparkWallet>(
    this: new (options?: ConfigOptions, signer?: SparkSigner) => T,
    { mnemonicOrSeed, accountNumber, signer, options = {} }: SparkWalletProps,
  ): Promise<InitWalletResponse<T>> {
    let wallet: T;

    try {
      wallet = new this(options, signer);
    } catch (error) {
      const err = await SparkWallet.handlePublicMethodError(error);
      throw err;
    }

    const wrappedInit = SparkWallet.wrapMethod(
      "initialize",
      () => wallet.initWallet(mnemonicOrSeed, accountNumber, options),
      wallet,
    );

    const initWalletResponse = await wrappedInit();
    return initWalletResponse as InitWalletResponse<T>;
  }

  private async createClientsAndSyncWallet() {
    this.sspClient = new SspClient(this.config);
    await this.connectionManager.createClients();

    if (isReactNative) {
      this.startPeriodicClaimTransfers();
    } else {
      this.setupBackgroundStream();
    }

    await this.syncWallet();

    // Start periodic token output optimization if enabled
    const tokenOptConfig = this.config.getTokenOptimizationOptions();
    if (tokenOptConfig?.enabled) {
      this.startPeriodicTokenOptimization();
    }
  }

  private getSspClient() {
    if (!this.sspClient) {
      throw new SparkError("SSP client not initialized", {
        configKey: "sspClient",
      });
    }
    return this.sspClient;
  }

  private async handleStreamEvent({ event }: SubscribeToEventsResponse) {
    try {
      if (
        isTransferStreamEvent(event) &&
        event.transfer.transfer.type !== TransferType.COUNTER_SWAP &&
        event.transfer.transfer.type !== TransferType.COUNTER_SWAP_V3
      ) {
        const { senderIdentityPublicKey, receiverIdentityPublicKey } =
          event.transfer.transfer;

        // Don't claim if this is a self transfer, that's handled elsewhere
        if (
          event.transfer.transfer &&
          !equalBytes(senderIdentityPublicKey, receiverIdentityPublicKey)
        ) {
          await this.claimTransfer({
            transfer: event.transfer.transfer,
            emit: true,
          });
        }
      } else if (isDepositStreamEvent(event)) {
        const deposit = event.deposit.deposit;

        await this.withLeaves(async () => {
          this.leaves.push(deposit);
        });

        this.emit(
          SparkWalletEvent.DepositConfirmed,
          deposit.id,
          (await this.getBalance()).balance,
        );
      }
    } catch (error) {
      console.error("Error processing event", error);
    }
  }

  protected async setupBackgroundStream() {
    const MAX_RETRIES = 10;
    const INITIAL_DELAY = 1000;
    const MAX_DELAY = 60000;

    const delay = (ms: number, signal: AbortSignal) => {
      return new Promise<boolean>((resolve) => {
        const timer = setTimeout(() => {
          signal.removeEventListener("abort", onAbort);
          resolve(true);
        }, ms);

        function onAbort() {
          clearTimeout(timer);
          resolve(false);
          signal.removeEventListener("abort", onAbort);
        }

        signal.addEventListener("abort", onAbort);
      });
    };

    let retryCount = 0;
    const streamController = new AbortController();
    this.streamController = streamController;
    while (retryCount <= MAX_RETRIES) {
      try {
        const address = this.config.getCoordinatorAddress();
        const stream = await this.connectionManager.subscribeToEvents(
          address,
          streamController.signal,
        );
        const claimedTransfersIds = await this.claimTransfers();

        try {
          for await (const data of stream) {
            if (streamController.signal.aborted) {
              break;
            }

            if (isConnectedStreamEvent(data.event)) {
              this.emit(SparkWalletEvent.StreamConnected);
              retryCount = 0;
            }

            if (
              isTransferStreamEvent(data.event) &&
              claimedTransfersIds.includes(data.event.transfer.transfer.id)
            ) {
              continue;
            }
            await this.handleStreamEvent(data);
          }
        } catch (error) {
          throw error;
        }
      } catch (error) {
        if (streamController.signal.aborted) {
          break;
        }

        const backoffDelay = Math.min(
          INITIAL_DELAY * Math.pow(2, retryCount),
          MAX_DELAY,
        );

        if (retryCount < MAX_RETRIES) {
          retryCount++;
          this.emit(
            SparkWalletEvent.StreamReconnecting,
            retryCount,
            MAX_RETRIES,
            backoffDelay,
            error instanceof Error ? error.message : String(error),
          );
          try {
            const completed = await delay(
              backoffDelay,
              streamController.signal,
            );
            if (!completed) {
              break;
            }
          } catch (error) {
            if (streamController.signal.aborted) {
              break;
            }
          }
        } else {
          this.emit(
            SparkWalletEvent.StreamDisconnected,
            "Max reconnection attempts reached",
          );
          break;
        }
      }
    }
  }

  public async getLeaves(isBalanceCheck: boolean = false): Promise<TreeNode[]> {
    const operatorToLeaves = new Map<string, QueryNodesResponse>();
    const ownerIdentityPubkey = await this.config.signer.getIdentityPublicKey();

    let signingOperators = Object.entries(this.config.getSigningOperators());
    if (isBalanceCheck) {
      // If we're just checking the balance, we can just query the coordinator.
      signingOperators = signingOperators.filter(
        ([id, _]) => id === this.config.getCoordinatorIdentifier(),
      );
    }
    await Promise.all(
      signingOperators.map(async ([id, operator]) => {
        const leaves = await this.queryNodes(
          {
            source: {
              $case: "ownerIdentityPubkey",
              ownerIdentityPubkey,
            },
            includeParents: false,
            network: NetworkToProto[this.config.getNetwork()],
            statuses: [TreeNodeStatus.TREE_NODE_STATUS_AVAILABLE],
          },
          operator.address,
        );
        operatorToLeaves.set(id, leaves);
      }),
    );

    const leaves = operatorToLeaves.get(
      this.config.getCoordinatorIdentifier(),
    )!;
    const leavesToIgnore: Set<string> = new Set();
    if (!isBalanceCheck) {
      // Query the leaf states from other operators.
      // We'll ignore the leaves that are out of sync for now.
      // Still include the leaves that are out of sync for balance check.
      for (const [id, operatorLeaves] of operatorToLeaves) {
        if (id !== this.config.getCoordinatorIdentifier()) {
          // Loop over leaves returned by coordinator.
          // If the leaf is not present in the operator's leaves, we'll ignore it.
          // If the leaf is present, we'll check if the leaf is in sync with the operator's leaf.
          // If the leaf is not in sync, we'll ignore it.
          for (const [nodeId, leaf] of Object.entries(leaves.nodes)) {
            const operatorLeaf = operatorLeaves.nodes[nodeId];

            if (!operatorLeaf) {
              leavesToIgnore.add(nodeId);
              continue;
            }

            if (
              leaf.status !== operatorLeaf.status ||
              !leaf.signingKeyshare ||
              !operatorLeaf.signingKeyshare ||
              !equalBytes(
                leaf.signingKeyshare.publicKey,
                operatorLeaf.signingKeyshare.publicKey,
              ) ||
              !equalBytes(leaf.nodeTx, operatorLeaf.nodeTx)
            ) {
              leavesToIgnore.add(nodeId);
            }
          }
        }
      }
    }

    const availableLeaves = Object.entries(leaves.nodes).filter(
      ([_, node]) => node.status === "AVAILABLE",
    );

    for (const [id, leaf] of availableLeaves) {
      if (
        leaf.parentNodeId &&
        leaf.status === "AVAILABLE" &&
        this.verifyKey(
          await this.config.signer.getPublicKeyFromDerivation({
            type: KeyDerivationType.LEAF,
            path: leaf.parentNodeId,
          }),
          leaf.signingKeyshare?.publicKey ?? new Uint8Array(),
          leaf.verifyingPublicKey,
        )
      ) {
        this.transferLeavesToSelf([leaf], {
          type: KeyDerivationType.LEAF,
          path: leaf.parentNodeId,
        });
        leavesToIgnore.add(id);
      } else if (
        !this.verifyKey(
          await this.config.signer.getPublicKeyFromDerivation({
            type: KeyDerivationType.LEAF,
            path: leaf.id,
          }),
          leaf.signingKeyshare?.publicKey ?? new Uint8Array(),
          leaf.verifyingPublicKey,
        )
      ) {
        leavesToIgnore.add(id);
      }
    }

    return availableLeaves
      .filter(([_, node]) => !leavesToIgnore.has(node.id))
      .map(([_, node]) => node);
  }

  private verifyKey(
    pubkey1: Uint8Array,
    pubkey2: Uint8Array,
    verifyingKey: Uint8Array,
  ): boolean {
    return equalBytes(addPublicKeys(pubkey1, pubkey2), verifyingKey);
  }

  private popOrThrow<T>(arr: T[] | undefined, msg: string): T {
    if (!arr || arr.length === 0) throw new SparkValidationError(msg);
    return arr.pop() as T;
  }

  private async selectLeaves(
    targetAmounts: number[],
  ): Promise<Map<number, TreeNode[][]>> {
    if (targetAmounts.length === 0) {
      throw new SparkValidationError("Target amounts must be non-empty", {
        field: "targetAmounts",
        value: targetAmounts,
      });
    }

    if (targetAmounts.some((amount) => amount <= 0)) {
      throw new SparkValidationError("Target amount must be positive", {
        field: "targetAmounts",
        value: targetAmounts,
      });
    }

    const totalTargetAmount = targetAmounts.reduce(
      (acc, amount) => acc + amount,
      0,
    );
    const totalBalance = this.getInternalBalance();

    if (totalTargetAmount > totalBalance) {
      throw new SparkValidationError(
        "Total target amount exceeds available balance",
        {
          field: "targetAmounts",
          value: totalTargetAmount,
          expected: `less than or equal to ${totalBalance}`,
        },
      );
    }

    const leaves = await this.getLeaves();
    if (leaves.length === 0) {
      throw new SparkValidationError("No owned leaves found", {
        field: "leaves",
      });
    }

    leaves.sort((a, b) => b.value - a.value);

    const selectLeavesForTargets = (
      targetAmounts: number[],
      leaves: TreeNode[],
    ) => {
      const usedLeaves = new Set<string>();
      const results: Map<number, TreeNode[][]> = new Map();
      let totalAmount = 0;

      for (const targetAmount of targetAmounts) {
        const nodes: TreeNode[] = [];
        let amount = 0;

        for (const leaf of leaves) {
          if (usedLeaves.has(leaf.id)) {
            continue;
          }

          if (targetAmount - amount >= leaf.value) {
            amount += leaf.value;
            nodes.push(leaf);
            usedLeaves.add(leaf.id);
          }
        }

        totalAmount += amount;
        if (results.has(targetAmount)) {
          results.get(targetAmount)!.push(nodes);
        } else {
          results.set(targetAmount, [nodes]);
        }
      }

      return {
        results,
        foundSelections: totalAmount === totalTargetAmount,
      };
    };

    let { results, foundSelections } = selectLeavesForTargets(
      targetAmounts,
      leaves,
    );

    if (!foundSelections) {
      const newLeaves = await this.requestLeavesSwap({ targetAmounts });

      newLeaves.sort((a, b) => b.value - a.value);

      ({ results, foundSelections } = selectLeavesForTargets(
        targetAmounts,
        newLeaves,
      ));
    }

    if (!foundSelections) {
      throw new Error(
        `Failed to select leaves for target amount ${totalTargetAmount}`,
      );
    }

    return results;
  }

  private async selectLeavesForSwap(targetAmount: number) {
    if (targetAmount == 0) {
      throw new Error("Target amount needs to > 0");
    }
    const leaves = await this.getLeaves();
    leaves.sort((a, b) => a.value - b.value);

    let amount = 0;
    const nodes: TreeNode[] = [];
    for (const leaf of leaves) {
      if (amount < targetAmount) {
        amount += leaf.value;
        nodes.push(leaf);
      }
    }

    if (amount < targetAmount) {
      throw new Error("Not enough leaves to swap for the target amount");
    }

    return nodes;
  }

  public async *optimizeLeaves(
    multiplicity: number | undefined = undefined,
  ): AsyncGenerator<
    {
      step: number;
      total: number;
      controller: AbortController;
    },
    void,
    void
  > {
    const multiplicityValue =
      multiplicity ?? this.config.getOptimizationOptions().multiplicity ?? 0;
    if (multiplicityValue < 0) {
      throw new SparkValidationError("Multiplicity cannot be negative");
    } else if (multiplicityValue > 5) {
      throw new SparkValidationError("Multiplicity cannot be greater than 5");
    }

    if (this.optimizationInProgress) {
      return;
    }

    const controller = new AbortController();
    const release = await this.leavesMutex.acquire();
    try {
      this.optimizationInProgress = true;

      this.leaves = await this.getLeaves();
      const swaps = optimize(
        this.leaves.map((leaf) => leaf.value),
        multiplicityValue,
      );
      if (swaps.length === 0) {
        return;
      }

      yield {
        step: 0,
        total: swaps.length,
        controller,
      };

      // Build a map from the denomination to the indices
      const valueToNodes = new Map<number, TreeNode[]>();
      this.leaves.forEach((leaf) => {
        if (!valueToNodes.has(leaf.value)) {
          valueToNodes.set(leaf.value, []);
        }
        valueToNodes.get(leaf.value)!.push(leaf);
      });

      // Select the leaves to send for each swap.
      for (const swap of swaps) {
        if (controller.signal.aborted) {
          break;
        }

        const leavesToSend: TreeNode[] = [];
        for (const leafValue of swap.inLeaves) {
          const nodes = valueToNodes.get(leafValue);
          if (nodes && nodes.length > 0) {
            const node = nodes.shift()!;
            leavesToSend.push(node);
          } else {
            throw new SparkError(
              `No unused leaf with value ${leafValue} found in leaves`,
            );
          }
        }

        // TODO: Parallelize this.
        await this.requestLeavesSwap({
          leaves: leavesToSend,
          targetAmounts: swap.outLeaves,
        });

        yield {
          step: swaps.indexOf(swap) + 1,
          total: swaps.length,
          controller,
        };
      }

      this.leaves = await this.getLeaves();
    } finally {
      this.optimizationInProgress = false;
      release();
    }
  }

  /**
   * Optimizes token outputs by consolidating them when there are more than the configured threshold.
   * Processes one token at a time that has more than 50 outputs (configurable).
   * On each run, it will find the next token identifier that needs consolidation and process it.
   * Respects the maximum of 500 outputs per transaction.
   */
  public async optimizeTokenOutputs(): Promise<void> {
    if (this.tokenOptimizationInProgress) {
      return;
    }

    this.tokenOptimizationInProgress = true;

    try {
      await this.syncTokenOutputs();

      const tokenOptConfig = this.config.getTokenOptimizationOptions();
      const minOutputsThreshold = tokenOptConfig?.minOutputsThreshold ?? 50;

      const entries = await this.tokenOutputManager.entries();
      for (const [tokenIdentifier, allOutputs] of entries) {
        if (allOutputs.length <= minOutputsThreshold) {
          continue;
        }

        const { outputs: outputsToConsolidate, release } =
          await this.tokenOutputManager.acquireOutputs(
            tokenIdentifier,
            (available) => available.slice(0, MAX_TOKEN_OUTPUTS_TX),
            `optimize-${tokenIdentifier}`,
          );

        if (outputsToConsolidate.length === 0) {
          continue;
        }

        try {
          const receiverSparkAddress = await this.getSparkAddress();
          const totalAmount = sumTokenOutputs(outputsToConsolidate);

          const txId = await this.tokenTransactionService.tokenTransfer({
            tokenOutputs: new Map([[tokenIdentifier, outputsToConsolidate]]),
            receiverOutputs: [
              {
                tokenIdentifier,
                tokenAmount: totalAmount,
                receiverSparkAddress,
              },
            ],
            outputSelectionStrategy: "SMALL_FIRST",
          });

          await this.tokenOutputManager.lockOutputs(outputsToConsolidate);

          console.log(
            `Consolidated ${outputsToConsolidate.length} outputs for token ${tokenIdentifier} in transaction ${txId}`,
          );

          break;
        } catch (error) {
          console.error(
            `Failed to optimize token outputs for ${tokenIdentifier}:`,
            error,
          );
        } finally {
          await release();
        }
      }
    } finally {
      this.tokenOptimizationInProgress = false;
    }
  }

  /**
   * Starts periodic token output optimization.
   * @private
   */
  private startPeriodicTokenOptimization() {
    // Clear any existing interval first
    if (this.tokenOptimizationInterval) {
      clearInterval(this.tokenOptimizationInterval);
    }

    const tokenOptConfig = this.config.getTokenOptimizationOptions();
    const intervalMs = tokenOptConfig?.intervalMs ?? 300000; // Default 5 minutes

    // @ts-ignore
    this.tokenOptimizationInterval = setInterval(async () => {
      try {
        await this.optimizeTokenOutputs();
      } catch (error) {
        console.error("Error in periodic token output optimization:", error);
      }
    }, intervalMs);
  }

  private async syncWallet() {
    await this.syncTokenOutputs();

    let leaves = await this.getLeaves();

    leaves = await this.checkRenewLeaves(leaves);

    this.leaves = leaves;

    if (
      this.config.getOptimizationOptions().auto &&
      shouldOptimize(
        this.leaves.map((leaf) => leaf.value),
        this.config.getOptimizationOptions().multiplicity ?? 0,
      )
    ) {
      for await (const _ of this.optimizeLeaves()) {
        // run all optimizer steps, do nothing with them
      }
    }
  }

  private async withLeaves<T>(operation: () => Promise<T>): Promise<T> {
    const release = await this.leavesMutex.acquire();
    try {
      return await operation();
    } finally {
      release();
    }
  }

  /**
   * Gets the identity public key of the wallet.
   *
   * @returns {Promise<string>} The identity public key as a hex string.
   */
  public async getIdentityPublicKey(): Promise<string> {
    return bytesToHex(await this.config.signer.getIdentityPublicKey());
  }

  /**
   * Gets the Spark address of the wallet.
   *
   * @returns {Promise<string>} The Spark address as a hex string.
   */
  public async getSparkAddress(): Promise<SparkAddressFormat> {
    if (!this.sparkAddress) {
      this.sparkAddress = encodeSparkAddress({
        identityPublicKey: bytesToHex(
          await this.config.signer.getIdentityPublicKey(),
        ),
        network: this.config.getNetworkType(),
      });
    }

    return this.sparkAddress;
  }

  /**
   * Creates a Spark invoice for a sats payment on Spark.
   *
   * @param {Object} params - Parameters for the sats payment
   * @param {number} params.amount - The amount of sats to receive
   * @param {string} [params.memo] - The memo for the payment
   * @param {string} [params.senderSparkAddress] - The spark address of the expected sender
   * @param {Date} [params.expiryTime] - The expiry time of the payment
   * @param {string} [params.receiverIdentityPubkey] - Optional public key of the wallet receiving the invoice. If not present, the receiver will be the creator of this request. If provided and different from the creator's identity public key, the created invoice will be unsigned.
   * @returns {Promise<SparkAddressFormat>} The Spark address for the sats payment
   */
  public async createSatsInvoice({
    amount,
    memo,
    senderSparkAddress,
    expiryTime,
    receiverIdentityPubkey,
  }: {
    amount?: number;
    memo?: string;
    senderSparkAddress?: SparkAddressFormat;
    expiryTime?: Date;
    receiverIdentityPubkey?: string;
  }): Promise<SparkAddressFormat> {
    const MAX_SATS_AMOUNT = 2_100_000_000_000_000; // 21_000_000 BTC * 100_000_000 sats/BTC
    if (amount && (amount < 0 || amount > MAX_SATS_AMOUNT)) {
      throw new SparkValidationError(
        `Amount must be between 0 and ${MAX_SATS_AMOUNT} sats`,
        {
          field: "amount",
          value: amount,
          expected: `less than or equal to ${MAX_SATS_AMOUNT}`,
        },
      );
    }
    const protoPayment = {
      $case: "satsPayment",
      satsPayment: {
        amount: amount,
      },
    } as const;
    const senderPublicKey = senderSparkAddress
      ? hexToBytes(
          decodeSparkAddress(senderSparkAddress, this.config.getNetworkType())
            .identityPublicKey,
        )
      : undefined;
    const invoiceFields = {
      version: 1,
      id: uuidv7obj().bytes,
      paymentType: protoPayment,
      memo: memo,
      senderPublicKey,
      expiryTime: expiryTime ?? undefined,
    };
    validateSparkInvoiceFields(invoiceFields);
    const identityPublicKey = await this.config.signer.getIdentityPublicKey();
    const shouldSignInvoice =
      !receiverIdentityPubkey ||
      receiverIdentityPubkey.toLowerCase() ===
        bytesToHex(identityPublicKey).toLowerCase();
    let signature: Uint8Array | undefined = undefined;
    if (shouldSignInvoice) {
      const hash = HashSparkInvoice(
        invoiceFields,
        identityPublicKey,
        this.config.getNetworkType(),
      );
      signature = await this.config.signer.signSchnorrWithIdentityKey(hash);
    }
    return encodeSparkAddressWithSignature(
      {
        identityPublicKey:
          receiverIdentityPubkey ?? bytesToHex(identityPublicKey),
        network: this.config.getNetworkType(),
        sparkInvoiceFields: invoiceFields,
      },
      signature,
    );
  }

  /**
   * Creates a Spark invoice for a tokens payment on Spark.
   *
   * @param {Object} params - Parameters for the tokens payment
   * @param {bigint} [params.amount] - The amount of tokens to receive
   * @param {Bech32mTokenIdentifier} [params.tokenIdentifier] - The token identifier
   * @param {string} [params.memo] - The memo for the payment
   * @param {string} [params.senderSparkAddress] - The spark address of the expected sender
   * @param {Date} [params.expiryTime] - The expiry time of the payment
   * @returns {Promise<SparkAddressFormat>} The Spark address for the tokens payment
   */
  public async createTokensInvoice({
    amount,
    tokenIdentifier,
    memo,
    senderSparkAddress,
    expiryTime,
  }: {
    tokenIdentifier?: Bech32mTokenIdentifier;
    amount?: bigint;
    memo?: string;
    senderSparkAddress?: SparkAddressFormat;
    expiryTime?: Date;
  }): Promise<SparkAddressFormat> {
    const MAX_UINT128 = BigInt(2 ** 128 - 1);
    if (amount && (amount < 0 || amount > MAX_UINT128)) {
      throw new SparkValidationError(
        `Amount must be between 0 and ${MAX_UINT128}`,
        {
          field: "amount",
          value: amount,
          expected: `greater than or equal to 0 and less than or equal to ${MAX_UINT128}`,
        },
      );
    }
    let decodedTokenIdentifier: Uint8Array | undefined = undefined;
    if (tokenIdentifier) {
      decodedTokenIdentifier = decodeBech32mTokenIdentifier(
        tokenIdentifier,
        this.config.getNetworkType(),
      ).tokenIdentifier;
    }
    const protoPayment = {
      $case: "tokensPayment",
      tokensPayment: {
        tokenIdentifier: decodedTokenIdentifier ?? undefined,
        amount: amount ? numberToVarBytesBE(amount) : undefined,
      },
    } as const;
    const senderPublicKey = senderSparkAddress
      ? hexToBytes(
          decodeSparkAddress(senderSparkAddress, this.config.getNetworkType())
            .identityPublicKey,
        )
      : undefined;
    const invoiceFields = {
      version: 1,
      id: uuidv7obj().bytes,
      paymentType: protoPayment,
      memo: memo ?? undefined,
      senderPublicKey,
      expiryTime: expiryTime ?? undefined,
    };
    validateSparkInvoiceFields(invoiceFields);
    const identityPublicKey = await this.config.signer.getIdentityPublicKey();
    const hash = HashSparkInvoice(
      invoiceFields,
      identityPublicKey,
      this.config.getNetworkType(),
    );
    const signature = await this.config.signer.signSchnorrWithIdentityKey(hash);
    return encodeSparkAddressWithSignature(
      {
        identityPublicKey: bytesToHex(identityPublicKey),
        network: this.config.getNetworkType(),
        sparkInvoiceFields: invoiceFields,
      },
      signature,
    );
  }

  /**
   * Initializes the wallet using either a mnemonic phrase or a raw seed.
   * initWallet will also claim any pending incoming lightning payment, spark transfer,
   * or bitcoin deposit.
   *
   * @param {Uint8Array | string} [mnemonicOrSeed] - (Optional) Either:
   *   - A BIP-39 mnemonic phrase as string
   *   - A raw seed as Uint8Array or hex string
   *   If not provided, generates a new mnemonic and uses it to create a new wallet
   *
   * @param {number} [accountNumber] - (Optional) The account number to use for the wallet. Defaults to 1 to maintain backwards compatability for legacy mainnet wallets.
   *
   * @returns {Promise<Object>} Object containing:
   *   - mnemonic: The mnemonic if one was generated (undefined for raw seed)
   *   - wallet: The wallet instance
   * @private
   */
  protected async initWallet(
    mnemonicOrSeed?: Uint8Array | string,
    accountNumber?: number,
    options: ConfigOptions = {},
  ): Promise<InitWalletResponse<this>> {
    if (options.signerWithPreExistingKeys) {
      await this.initWalletWithoutSeed();
      return {
        wallet: this,
        mnemonic: undefined,
      };
    }

    if (accountNumber === undefined) {
      if (this.config.getNetwork() === Network.REGTEST) {
        accountNumber = 0;
      } else {
        accountNumber = 1;
      }
    }
    let mnemonic: string | undefined;
    if (!mnemonicOrSeed) {
      mnemonic = await this.config.signer.generateMnemonic();
      mnemonicOrSeed = mnemonic;
    }

    let seed: Uint8Array;
    if (typeof mnemonicOrSeed !== "string") {
      seed = mnemonicOrSeed;
    } else {
      if (validateMnemonic(mnemonicOrSeed, wordlist)) {
        mnemonic = mnemonicOrSeed;
        seed = await this.config.signer.mnemonicToSeed(mnemonicOrSeed);
      } else {
        seed = hexToBytes(mnemonicOrSeed);
      }
    }

    await this.initWalletFromSeed(seed, accountNumber);

    return {
      mnemonic,
      wallet: this,
    };
  }

  /**
   * Initializes the wallet without a seed. Meant for use with a signer with pre-existing keys.
   * @private
   */
  protected async initWalletWithoutSeed() {
    await this.createClientsAndSyncWallet();

    const identityPublicKey = await this.config.signer.getIdentityPublicKey();

    if (!identityPublicKey || identityPublicKey.length === 0) {
      throw new SparkValidationError(
        "Identity public key not found in signer",
        {
          field: "identityPublicKey",
          value: identityPublicKey,
        },
      );
    }

    this.sparkAddress = encodeSparkAddress({
      identityPublicKey: bytesToHex(identityPublicKey),
      network: this.config.getNetworkType(),
    });

    return this.sparkAddress;
  }

  /**
   * Initializes a wallet from a seed.
   *
   * @param {Uint8Array | string} seed - The seed to initialize the wallet from
   * @returns {Promise<string>} The Spark address
   * @private
   */
  private async initWalletFromSeed(
    seed: Uint8Array | string,
    accountNumber?: number,
  ) {
    const identityPublicKey =
      await this.config.signer.createSparkWalletFromSeed(seed, accountNumber);
    await this.createClientsAndSyncWallet();

    this.sparkAddress = encodeSparkAddress({
      identityPublicKey: identityPublicKey,
      network: this.config.getNetworkType(),
    });

    return this.sparkAddress;
  }

  /**
   * Gets the estimated fee for a swap of leaves.
   *
   * @param amountSats - The amount of sats to swap
   *  @returns {Promise<LeavesSwapFeeEstimateOutput>}  The estimated fee for the swap
   */
  public async getSwapFeeEstimate(
    amountSats: number,
  ): Promise<LeavesSwapFeeEstimateOutput> {
    const sspClient = this.getSspClient();

    const feeEstimate = await sspClient.getSwapFeeEstimate(amountSats);
    if (!feeEstimate) {
      throw new Error("Failed to get swap fee estimate");
    }

    return feeEstimate;
  }

  /**
   * Requests a swap of leaves to optimize wallet structure.
   *
   * @param {Object} params - Parameters for the leaves swap
   * @param {number} [params.targetAmount] - Target amount for the swap
   * @param {TreeNode[]} [params.leaves] - Specific leaves to swap
   * @returns {Promise<Object>} The completed swap response
   * @private
   */
  private async requestLeavesSwap({
    targetAmounts,
    leaves,
  }: {
    targetAmounts?: number[];
    leaves?: TreeNode[];
  }): Promise<TreeNode[]> {
    if (targetAmounts && targetAmounts.some((amount) => amount <= 0)) {
      throw new Error("specified targetAmount must be positive");
    }

    if (
      targetAmounts &&
      targetAmounts.some((amount) => !Number.isSafeInteger(amount))
    ) {
      throw new SparkValidationError("targetAmount must be less than 2^53", {
        field: "targetAmounts",
        value: targetAmounts,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    let leavesToSwap: TreeNode[];
    const totalTargetAmount = targetAmounts?.reduce(
      (acc, amount) => acc + amount,
      0,
    );

    if (totalTargetAmount) {
      const totalBalance = this.getInternalBalance();

      if (totalTargetAmount > totalBalance) {
        throw new SparkValidationError(
          "Total target amount exceeds available balance",
          {
            field: "targetAmounts",
            value: totalTargetAmount,
            expected: `less than or equal to ${totalBalance}`,
          },
        );
      }
    }

    if (totalTargetAmount && leaves && leaves.length > 0) {
      if (
        totalTargetAmount < leaves.reduce((acc, leaf) => acc + leaf.value, 0)
      ) {
        throw new Error("targetAmount is less than the sum of leaves");
      }
      leavesToSwap = leaves;
    } else if (totalTargetAmount) {
      leavesToSwap = await this.selectLeavesForSwap(totalTargetAmount);
    } else if (leaves && leaves.length > 0) {
      leavesToSwap = leaves;
    } else {
      throw new Error("targetAmount or leaves must be provided");
    }

    leavesToSwap.sort((a, b) => a.value - b.value);

    const batches = chunkArray(leavesToSwap, 64);

    const results: TreeNode[] = [];
    for (const batch of batches) {
      const result = await this.processSwapBatch(batch, targetAmounts);
      results.push(...result);
    }

    return results;
  }

  /**
   * Processes a single batch of leaves for swapping.
   */
  private async processSwapBatch(
    leavesBatch: TreeNode[],
    targetAmounts?: number[],
  ): Promise<TreeNode[]> {
    if (!targetAmounts) {
      targetAmounts = leavesBatch.map((leaf) => leaf.value);
    }

    const leafKeyTweaks: LeafKeyTweak[] = await Promise.all(
      leavesBatch.map(async (leaf) => ({
        leaf,
        keyDerivation: {
          type: KeyDerivationType.LEAF,
          path: leaf.id,
        },
        newKeyDerivation: {
          type: KeyDerivationType.RANDOM,
        },
      })),
    );

    const { swapTransfer, adaptorPubkey, adaptorAddedSignatureMap } =
      await this.transferService.sendSwapTransfer(
        leafKeyTweaks,
        hexToBytes(this.config.getSspIdentityPublicKey()),
      );

    if (!swapTransfer.transfer) {
      throw new SparkValidationError("Transfer is missing in swap response", {
        field: "transfer",
        value: swapTransfer.transfer,
        expected: "not null",
      });
    }
    if (swapTransfer.transfer.leaves.some((leaf) => !leaf.leaf)) {
      throw new SparkValidationError("Leaf is missing in swap response", {
        field: "leaves",
        value: swapTransfer.transfer.leaves,
        expected: "not null",
      });
    }

    const transfer = swapTransfer.transfer;

    try {
      const userLeaves: UserLeafInput[] = [];
      for (let i = 0; i < transfer.leaves.length; i++) {
        const leaf = transfer.leaves[i];
        if (!leaf?.leaf) {
          console.error(`[processSwapBatch] Leaf ${i + 1} is missing`);
          throw new Error("Failed to get leaf");
        }

        const adaptorAddedSignature = adaptorAddedSignatureMap.get(
          leaf.leaf.id,
        );

        if (!adaptorAddedSignature) {
          throw new Error("Adaptor added signature not found");
        }
        userLeaves.push({
          leaf_id: leaf.leaf.id,
          raw_unsigned_refund_transaction: bytesToHex(
            leaf.intermediateRefundTx,
          ),
          direct_raw_unsigned_refund_transaction: bytesToHex(
            leaf.intermediateDirectRefundTx,
          ),
          direct_from_cpfp_raw_unsigned_refund_transaction: bytesToHex(
            leaf.intermediateDirectFromCpfpRefundTx,
          ),
          adaptor_added_signature: bytesToHex(adaptorAddedSignature),
          direct_adaptor_added_signature: bytesToHex(adaptorAddedSignature),
          direct_from_cpfp_adaptor_added_signature: bytesToHex(
            adaptorAddedSignature,
          ),
        });
      }

      const sspClient = this.getSspClient();

      const request = await sspClient.requestLeavesSwap({
        userLeaves,
        adaptorPubkey: bytesToHex(adaptorPubkey),
        targetAmountSats: targetAmounts,
        totalAmountSats: leavesBatch.reduce((acc, leaf) => acc + leaf.value, 0),
        // TODO: Request fee from SSP
        feeSats: 0,
        userOutboundTransferExternalId: transfer.id,
      });

      if (
        !request ||
        !request.swapLeaves ||
        request.swapLeaves.length === 0 ||
        request.status === SparkLeavesSwapRequestStatus.FAILED ||
        !request.inboundTransfer?.sparkId
      ) {
        console.error("[processSwapBatch] Leave swap request returned null");
        throw new Error("Failed to request leaves swap. Request failed.");
      }

      await this.updateLeaves(
        leavesBatch.map((leaf) => leaf.id),
        [],
      );

      const incomingTransfer = await this.transferService.queryTransfer(
        request.inboundTransfer.sparkId,
      );

      if (!incomingTransfer) {
        console.error("[processSwapBatch] No incoming transfer found");
        throw new Error("Failed to get incoming transfer");
      }

      return await this.claimTransfer({
        transfer: incomingTransfer,
        emit: false,
      });
    } catch (e) {
      console.error("[processSwapBatch] Error details:", {
        error: e,
        message: (e as Error).message,
        stack: (e as Error).stack,
      });
      throw new Error(`Failed to request leaves swap: ${e}`);
    }
  }

  /**
   * Gets the current balance of the wallet.
   * You can use the forceRefetch option to synchronize your wallet and claim any
   * pending incoming lightning payment, spark transfer, or bitcoin deposit before returning the balance.
   *
   * @returns {Promise<Object>} Object containing:
   *   - balance: The wallet's current balance in satoshis
   *   - tokenBalances: Map of the bech32m encodedtoken identifier to token balances and token info
   */
  public async getBalance(): Promise<{
    balance: bigint;
    tokenBalances: TokenBalanceMap;
  }> {
    const leaves = await this.getLeaves(true);
    await this.syncTokenOutputs();

    let tokenBalances: TokenBalanceMap;

    const hasTokenOutputs = !(await this.tokenOutputManager.isEmpty());
    if (hasTokenOutputs) {
      tokenBalances = await this.getTokenBalance();
    } else {
      tokenBalances = new Map();
    }

    return {
      balance: BigInt(leaves.reduce((acc, leaf) => acc + leaf.value, 0)),
      tokenBalances,
    };
  }

  private async getTokenMetadata(): Promise<
    Map<Bech32mTokenIdentifier, UserTokenMetadata>
  > {
    const tokenIdentifierKeys =
      await this.tokenOutputManager.getTokenIdentifiers();
    let metadataToFetch = new Array<Bech32mTokenIdentifier>();
    for (const tokenIdentifier of tokenIdentifierKeys) {
      if (!this.tokenMetadata.has(tokenIdentifier)) {
        metadataToFetch.push(tokenIdentifier);
      }
    }

    if (metadataToFetch.length > 0) {
      const sparkTokenClient =
        await this.connectionManager.createSparkTokenClient(
          this.config.getCoordinatorAddress(),
        );

      try {
        const response = await sparkTokenClient.query_token_metadata({
          tokenIdentifiers: metadataToFetch.map(
            (tokenIdentifier) =>
              decodeBech32mTokenIdentifier(
                tokenIdentifier,
                this.config.getNetworkType(),
              ).tokenIdentifier,
          ),
        });

        for (const tokenMetadata of response.tokenMetadata) {
          const tokenIdentifier = encodeBech32mTokenIdentifier({
            tokenIdentifier: tokenMetadata.tokenIdentifier,
            network: this.config.getNetworkType(),
          });

          this.tokenMetadata.set(tokenIdentifier, tokenMetadata);
        }
      } catch (error) {
        throw new SparkRequestError("Failed to fetch token metadata", {
          error,
        });
      }
    }

    let tokenMetadataMap = new Map<Bech32mTokenIdentifier, UserTokenMetadata>();

    for (const [tokenIdentifier, metadata] of this.tokenMetadata) {
      tokenMetadataMap.set(tokenIdentifier, {
        tokenPublicKey: bytesToHex(metadata.issuerPublicKey),
        rawTokenIdentifier: metadata.tokenIdentifier,
        tokenName: metadata.tokenName,
        tokenTicker: metadata.tokenTicker,
        decimals: metadata.decimals,
        maxSupply: bytesToNumberBE(metadata.maxSupply),
        extraMetadata: metadata.extraMetadata
          ? new Uint8Array(metadata.extraMetadata)
          : undefined,
      });
    }

    return tokenMetadataMap;
  }

  private async getTokenBalance(): Promise<TokenBalanceMap> {
    const tokenMetadataMap = await this.getTokenMetadata();
    const result: TokenBalanceMap = new Map();

    for (const [tokenIdentifier, tokenMetadata] of tokenMetadataMap) {
      const availableOutputs =
        await this.tokenOutputManager.getAvailableOutputs(tokenIdentifier);

      const humanReadableTokenIdentifier = encodeBech32mTokenIdentifier({
        tokenIdentifier: tokenMetadata.rawTokenIdentifier,
        network: this.config.getNetworkType(),
      });

      const pendingOutputs =
        await this.tokenOutputManager.getPendingOutboundOutputs(
          humanReadableTokenIdentifier,
        );

      const allOutputsSum = sumTokenOutputs([
        ...availableOutputs,
        ...pendingOutputs,
      ]);
      const availableToSendBalance = sumTokenOutputs(availableOutputs);

      result.set(humanReadableTokenIdentifier, {
        ownedBalance: allOutputsSum,
        availableToSendBalance,
        tokenMetadata: tokenMetadata,
      });
    }

    return result;
  }

  private getInternalBalance(): number {
    return this.leaves.reduce((acc, leaf) => acc + leaf.value, 0);
  }

  // ***** Deposit Flow *****

  /**
   * Generates a new deposit address for receiving bitcoin funds.
   * Note that this function returns a bitcoin address, not a spark address, and this address is single use.
   * Once you deposit funds to this address, it cannot be used again.
   * For Layer 1 Bitcoin deposits, Spark generates Pay to Taproot (P2TR) addresses.
   * These addresses start with "bc1p" and can be used to receive Bitcoin from any wallet.
   *
   * @returns {Promise<string>} A Bitcoin address for depositing funds
   */
  public async getSingleUseDepositAddress(): Promise<string> {
    return await this.generateDepositAddress();
  }

  /**
   * Generates a new static deposit address for receiving bitcoin funds.
   * This address is permanent and can be used multiple times.
   *
   * @returns {Promise<string>} A Bitcoin address for depositing funds
   */
  public async getStaticDepositAddress(): Promise<string> {
    const signingPubkey =
      await this.config.signer.getStaticDepositSigningKey(0);

    const address = await this.depositService.generateStaticDepositAddress({
      signingPubkey,
    });
    if (!address.depositAddress) {
      throw new SparkError("Failed to generate static deposit address", {
        signingPubkey,
      });
    }

    return address.depositAddress.address;
  }

  /**
   * Generates a deposit address for receiving funds.
   * @returns {Promise<string>} A deposit address
   * @private
   */
  private async generateDepositAddress(): Promise<string> {
    const leafId = uuidv7();

    const signingPubkey = await this.config.signer.getPublicKeyFromDerivation({
      type: KeyDerivationType.LEAF,
      path: leafId,
    });

    const address = await this.depositService.generateDepositAddress({
      signingPubkey,
      leafId,
    });
    if (!address.depositAddress) {
      throw new SparkRequestError("Failed to generate deposit address", {
        signingPubkey,
        leafId,
      });
    }
    return address.depositAddress.address;
  }

  public async queryStaticDepositAddresses(): Promise<string[]> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );
    return (
      await sparkClient.query_static_deposit_addresses({
        identityPublicKey: await this.config.signer.getIdentityPublicKey(),
        network: NetworkToProto[this.config.getNetwork()],
      })
    ).depositAddresses.map((addr) => addr.depositAddress);
  }

  /**
   * Returns confirmed UTXOs for a given Spark deposit address.
   *
   * @param depositAddress - The deposit address to query.
   * @param limit - Maximum number of UTXOs to return (default 100).
   * @param offset - Pagination offset (default 0).
   * @returns {Promise<{ txid: string, vout: number }[]>} List of confirmed UTXOs.
   */
  public async getUtxosForDepositAddress(
    depositAddress: string,
    limit: number = 100,
    offset: number = 0,
    excludeClaimed: boolean = false,
  ): Promise<{ txid: string; vout: number }[]> {
    if (!depositAddress) {
      throw new SparkValidationError("Deposit address cannot be empty", {
        field: "depositAddress",
      });
    }

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    try {
      const response = await sparkClient.get_utxos_for_address({
        address: depositAddress,
        network: NetworkToProto[this.config.getNetwork()],
        limit,
        offset,
        excludeClaimed,
      });

      return (
        response.utxos.map((utxo) => ({
          txid: bytesToHex(utxo.txid),
          vout: utxo.vout,
        })) ?? []
      );
    } catch (error) {
      throw new SparkRequestError("Failed to get UTXOs for deposit address", {
        operation: "get_utxos_for_address",
        error,
      });
    }
  }

  /**
   * Get a quote on how much credit you can claim for a deposit from the SSP.
   *
   * @param {string} transactionId - The ID of the transaction
   * @param {number} [outputIndex] - The index of the output
   * @returns {Promise<StaticDepositQuoteOutput>} Quote for claiming a deposit to a static deposit address
   */
  public async getClaimStaticDepositQuote(
    transactionId: string,
    outputIndex?: number,
  ): Promise<StaticDepositQuoteOutput> {
    const sspClient = this.getSspClient();
    let network = this.config.getSspNetwork();

    if (network === BitcoinNetwork.FUTURE_VALUE) {
      network = BitcoinNetwork.REGTEST;
    }

    if (outputIndex === undefined) {
      outputIndex = await this.getDepositTransactionVout({
        txid: transactionId,
      });
    }

    const quote = await sspClient.getClaimDepositQuote({
      transactionId,
      outputIndex,
      network,
    });

    if (!quote) {
      throw new Error("Failed to get claim deposit quote");
    }

    return quote;
  }

  /**
   * Claims a deposit to a static deposit address.
   *
   * @param {string} transactionId - The ID of the transaction
   * @param {number} creditAmountSats - The amount of credit to claim
   * @param {string} sspSignature - The SSP signature for the deposit
   * @param {number} [outputIndex] - The index of the output
   * @returns {Promise<RequestClaimDepositQuoteOutput | null>} Quote for claiming a deposit to a static deposit address
   */
  public async claimStaticDeposit({
    transactionId,
    creditAmountSats,
    sspSignature,
    outputIndex,
  }: {
    transactionId: string;
    creditAmountSats: number;
    sspSignature: string;
    outputIndex?: number;
  }): Promise<ClaimStaticDepositOutput | null> {
    if (!this.sspClient) {
      throw new Error("SSP client not initialized");
    }

    if (outputIndex === undefined) {
      outputIndex = await this.getDepositTransactionVout({
        txid: transactionId,
      });
    }

    let network = this.config.getSspNetwork();

    if (network === BitcoinNetwork.FUTURE_VALUE) {
      network = BitcoinNetwork.REGTEST;
    }

    // const network =  BitcoinNetwork.REGTEST;
    const depositSecretKey = bytesToHex(
      await this.config.signer.getStaticDepositSecretKey(0),
    );

    const message = await this.getStaticDepositSigningPayload(
      transactionId,
      outputIndex,
      network.toLowerCase(),
      UtxoSwapRequestType.Fixed,
      creditAmountSats,
      sspSignature,
    );

    const hashBuffer = sha256(message);
    const signatureBytes =
      await this.config.signer.signMessageWithIdentityKey(hashBuffer);
    const signature = bytesToHex(signatureBytes);

    const response = await this.sspClient.claimStaticDeposit({
      transactionId,
      outputIndex,
      network,
      creditAmountSats,
      depositSecretKey,
      signature,
      sspSignature,
    });

    if (!response) {
      throw new Error("Failed to claim static deposit");
    }

    return response;
  }

  /**
   * Get a quote on how much credit you can claim for a deposit from the SSP. If the quote charges less fees than the max fee, claim the deposit.
   *
   * @param {Object} params - The parameters object
   * @param {string} params.transactionId - The ID of the transaction
   * @param {number} params.maxFee - The maximum fee to claim the deposit for
   * @param {number} [params.outputIndex] - The index of the output
   * @returns {Promise<StaticDepositQuoteOutput>} Quote for claiming a deposit to a static deposit address
   */
  public async claimStaticDepositWithMaxFee({
    transactionId,
    maxFee,
    outputIndex,
  }: {
    transactionId: string;
    maxFee: number;
    outputIndex?: number;
  }): Promise<ClaimStaticDepositOutput | null> {
    const sspClient = this.getSspClient();
    let network = this.config.getSspNetwork();

    if (network === BitcoinNetwork.FUTURE_VALUE) {
      network = BitcoinNetwork.REGTEST;
    }

    const depositTx = await this.getDepositTransaction(transactionId);

    if (outputIndex === undefined) {
      outputIndex = await this.getDepositTransactionVout({
        txid: transactionId,
        depositTx,
      });
    }

    const depositAmount = Number(depositTx.getOutput(outputIndex).amount);

    const quote = await sspClient.getClaimDepositQuote({
      transactionId,
      outputIndex,
      network,
    });

    if (!quote) {
      throw new Error("Failed to get claim deposit quote");
    }

    const { creditAmountSats, signature: sspSignature } = quote;

    const feeCharged = depositAmount - creditAmountSats;

    if (feeCharged > maxFee) {
      throw new SparkValidationError("Fee larger than max fee", {
        field: "feeCharged",
        value: feeCharged,
      });
    }

    const response = await this.claimStaticDeposit({
      transactionId,
      creditAmountSats,
      sspSignature,
      outputIndex,
    });

    if (!response) {
      throw new Error("Failed to claim static deposit");
    }

    return response;
  }

  /**
   * Refunds a static deposit to a destination address.
   *
   * @param {Object} params - The refund parameters
   * @param {string} params.depositTransactionId - The ID of the transaction
   * @param {number} [params.outputIndex] - The index of the output
   * @param {string} params.destinationAddress - The destination address
   * @param {number} [params.fee] - **@deprecated** The fee to refund
   * @param {number} [params.satsPerVbyteFee] - The fee per vbyte to refund
   * @returns {Promise<string>} The hex of the refund transaction
   */
  public async refundStaticDeposit({
    depositTransactionId,
    outputIndex,
    destinationAddress,
    fee,
    satsPerVbyteFee,
  }: {
    depositTransactionId: string;
    outputIndex?: number;
    destinationAddress: string;
    /** @deprecated use `satsPerVbyteFee` */ fee?: number;
    satsPerVbyteFee?: number;
  }): Promise<string> {
    if (fee === undefined && satsPerVbyteFee === undefined) {
      throw new SparkValidationError("Fee or satsPerVbyteFee must be provided");
    }

    // Users can set this to 300 or higher due to our old flow so they may be trained to type in 300 or higher which would make the fee way too high.
    if (satsPerVbyteFee && satsPerVbyteFee > 150) {
      throw new SparkValidationError("satsPerVbyteFee must be less than 150");
    }

    const finalFee = satsPerVbyteFee
      ? satsPerVbyteFee * getTxEstimatedVbytesSizeByNumberOfInputsOutputs(1, 1)
      : fee!;

    if (finalFee < 194) {
      throw new SparkValidationError("Fee must be at least 194", {
        field: "fee",
        value: finalFee,
      });
    }

    let network = this.config.getNetwork();
    let networkType = this.config.getNetworkProto();
    const networkJSON = networkToJSON(networkType);

    const depositTx = await this.getDepositTransaction(depositTransactionId);

    if (outputIndex === undefined) {
      outputIndex = await this.getDepositTransactionVout({
        txid: depositTransactionId,
        depositTx,
      });
    }

    const totalAmount = depositTx.getOutput(outputIndex).amount;
    const creditAmountSats = Number(totalAmount) - finalFee;

    if (creditAmountSats <= 0) {
      throw new SparkValidationError(
        "Fee too large. Credit amount must be greater than 0",
        {
          field: "creditAmountSats",
          value: creditAmountSats,
        },
      );
    }

    const tx = new Transaction({
      version: 3,
    });

    tx.addInput({
      txid: depositTransactionId,
      index: outputIndex,
      witnessScript: new Uint8Array(),
    });

    // Decode the address and create output script
    const addressDecoded = Address(getNetwork(network)).decode(
      destinationAddress,
    );
    const outputScript = OutScript.encode(addressDecoded);

    // Add the output to the transaction
    tx.addOutput({
      script: outputScript,
      amount: BigInt(creditAmountSats),
    });

    const spendTxSighash = getSigHashFromTx(
      tx,
      0,
      depositTx.getOutput(outputIndex),
    );

    // Used in the signing job and frost.
    const signingNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();

    const signingJob: SigningJob = {
      rawTx: tx.toBytes(),
      signingPublicKey: await this.config.signer.getStaticDepositSigningKey(0),
      signingNonceCommitment: signingNonceCommitment.commitment,
    };

    const message = await this.getStaticDepositSigningPayload(
      depositTransactionId,
      outputIndex,
      networkJSON.toLowerCase(),
      UtxoSwapRequestType.Refund,
      creditAmountSats,
      bytesToHex(spendTxSighash),
    );
    const hashBuffer = sha256(message);
    const swapResponseUserSignature =
      await this.config.signer.signMessageWithIdentityKey(hashBuffer);

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    // Initiate Utxo Swap
    const swapResponse = await sparkClient.initiate_static_deposit_utxo_refund({
      onChainUtxo: {
        txid: hexToBytes(depositTransactionId),
        vout: outputIndex,
        network: networkType,
      },
      userSignature: swapResponseUserSignature,
      refundTxSigningJob: signingJob,
    });

    if (!swapResponse) {
      throw new Error("Failed to initiate utxo swap");
    }

    // Sign the spend tx
    const userSignature = await this.config.signer.signFrost({
      message: spendTxSighash,
      publicKey: swapResponse.depositAddress!.verifyingPublicKey,
      keyDerivation: {
        type: KeyDerivationType.STATIC_DEPOSIT,
        path: 0,
      },
      selfCommitment: signingNonceCommitment,
      statechainCommitments:
        swapResponse.refundTxSigningResult!.signingNonceCommitments,
      verifyingKey: swapResponse.depositAddress!.verifyingPublicKey,
    });

    const signatureResult = await this.config.signer.aggregateFrost({
      message: spendTxSighash,
      statechainSignatures: swapResponse.refundTxSigningResult!.signatureShares,
      statechainPublicKeys: swapResponse.refundTxSigningResult!.publicKeys,
      verifyingKey: swapResponse.depositAddress!.verifyingPublicKey,
      statechainCommitments:
        swapResponse.refundTxSigningResult!.signingNonceCommitments,
      selfCommitment: signingNonceCommitment,
      publicKey: await this.config.signer.getStaticDepositSigningKey(0),
      selfSignature: userSignature,
    });

    // Update the input with the signature
    tx.updateInput(0, {
      finalScriptWitness: [signatureResult],
    });

    return tx.hex;
  }

  /**
   * Refunds a static deposit and broadcasts the transaction to the network.
   *
   * @param {Object} params - The refund parameters
   * @param {string} params.depositTransactionId - The ID of the transaction
   * @param {number} [params.outputIndex] - The index of the output
   * @param {string} params.destinationAddress - The destination address
   * @param {number} [params.satsPerVbyteFee] - The fee per vbyte to refund
   * @returns {Promise<string>} The transaction ID
   */
  public async refundAndBroadcastStaticDeposit({
    depositTransactionId,
    outputIndex,
    destinationAddress,
    satsPerVbyteFee,
  }: {
    depositTransactionId: string;
    outputIndex?: number;
    destinationAddress: string;
    satsPerVbyteFee?: number;
  }): Promise<string> {
    const txHex = await this.refundStaticDeposit({
      depositTransactionId,
      outputIndex,
      destinationAddress,
      satsPerVbyteFee,
    });

    return await this.broadcastTx(txHex);
  }

  /**
   * Broadcasts a transaction to the network.
   *
   * @param {string} txHex - The hex of the transaction
   * @returns {Promise<string>} The transaction ID
   */
  private async broadcastTx(txHex: string): Promise<string> {
    if (!txHex) {
      throw new SparkValidationError("Transaction hex cannot be empty", {
        field: "txHex",
      });
    }

    const { fetch, Headers } = getFetch();
    const baseUrl = this.config.getElectrsUrl();
    const headers = new Headers();

    if (this.config.getNetwork() === Network.LOCAL) {
      const localFaucet = BitcoinFaucet.getInstance();
      const response = await localFaucet.broadcastTx(txHex);
      return response;
    } else {
      if (this.config.getNetwork() === Network.REGTEST) {
        const auth = btoa(
          `${ELECTRS_CREDENTIALS.username}:${ELECTRS_CREDENTIALS.password}`,
        );
        headers.set("Authorization", `Basic ${auth}`);
      }

      const response = await fetch(`${baseUrl}/tx`, {
        method: "POST",
        body: txHex,
        headers,
      });

      return response.text();
    }
  }

  private async getStaticDepositSigningPayload(
    transactionID: string,
    outputIndex: number,
    network: string,
    requestType: UtxoSwapRequestType,
    creditAmountSats: number,
    sspSignature: string,
  ): Promise<Uint8Array> {
    const encoder = new TextEncoder();
    // Create arrays to hold all the data parts
    const parts: Uint8Array[] = [];

    // Add action name as UTF-8 bytes
    parts.push(encoder.encode("claim_static_deposit"));

    // Add network value as UTF-8 bytes
    parts.push(encoder.encode(network));

    // Add transaction ID as UTF-8 bytes
    parts.push(encoder.encode(transactionID));

    // Add output index as 4-byte unsigned integer (little-endian)
    const outputIndexBuffer = new ArrayBuffer(4);
    new DataView(outputIndexBuffer).setUint32(0, outputIndex, true); // true for little-endian
    parts.push(new Uint8Array(outputIndexBuffer));

    let requestTypeInt: number;
    switch (requestType) {
      case UtxoSwapRequestType.Fixed:
        requestTypeInt = 0;
        break;
      case UtxoSwapRequestType.MaxFee:
        requestTypeInt = 1;
        break;
      case UtxoSwapRequestType.Refund:
        requestTypeInt = 2;
        break;
      default:
        requestTypeInt = 0;
    }
    const requestTypeBuffer = new ArrayBuffer(1);
    new DataView(requestTypeBuffer).setUint8(0, requestTypeInt);
    parts.push(new Uint8Array(requestTypeBuffer));

    // Add credit amount as 8-byte unsigned integer (little-endian)
    const creditAmountBuffer = new ArrayBuffer(8);
    const creditAmountView = new DataView(creditAmountBuffer);

    // Split the number into low and high 32-bit parts
    const lowerHalf = creditAmountSats >>> 0; // Get the lower 32 bits
    const upperHalf = Math.floor(creditAmountSats / 0x100000000); // Get the upper 32 bits

    creditAmountView.setUint32(0, lowerHalf, true); // Lower 32 bits
    creditAmountView.setUint32(4, upperHalf, true); // Upper 32 bits

    parts.push(new Uint8Array(creditAmountBuffer));

    // Add SSP signature as bytes
    parts.push(hexToBytes(sspSignature));

    // Combine all parts into a single buffer
    const totalLength = parts.reduce((sum, part) => sum + part.length, 0);
    const payload = new Uint8Array(totalLength);

    let offset = 0;
    for (const part of parts) {
      payload.set(part, offset);
      offset += part.length;
    }
    return payload;
  }

  private async getDepositTransactionVout({
    txid,
    depositTx,
  }: {
    txid: string;
    depositTx?: Transaction;
  }): Promise<number> {
    if (!depositTx) {
      depositTx = await this.getDepositTransaction(txid);
    }

    const staticDepositAddresses = new Set(
      await this.queryStaticDepositAddresses(),
    );

    let vout = -1;

    for (let i = 0; i < depositTx.outputsLength; i++) {
      const output = depositTx.getOutput(i);
      if (!output) {
        continue;
      }
      const parsedScript = OutScript.decode(output.script!);
      const address = Address(getNetwork(this.config.getNetwork())).encode(
        parsedScript,
      );
      if (staticDepositAddresses.has(address)) {
        vout = i;
        break;
      }
    }

    if (vout === -1) {
      throw new Error("No static deposit address found");
    }

    return vout;
  }

  private async getDepositTransaction(txid: string): Promise<Transaction> {
    if (!txid) {
      throw new SparkValidationError("Transaction ID cannot be empty", {
        field: "txid",
      });
    }

    const { fetch, Headers } = getFetch();
    const baseUrl = this.config.getElectrsUrl();
    const headers = new Headers();

    let txHex: string | undefined;

    if (this.config.getNetwork() === Network.LOCAL) {
      const localFaucet = BitcoinFaucet.getInstance();
      const response = await localFaucet.getRawTransaction(txid);
      txHex = response.hex;
    } else {
      if (this.config.getNetwork() === Network.REGTEST) {
        const auth = btoa(
          `${ELECTRS_CREDENTIALS.username}:${ELECTRS_CREDENTIALS.password}`,
        );
        headers.set("Authorization", `Basic ${auth}`);
      }

      const response = await fetch(`${baseUrl}/tx/${txid}/hex`, {
        headers,
      });

      txHex = await response.text();
    }

    if (!txHex) {
      throw new Error("Transaction not found");
    }

    if (!/^[0-9A-Fa-f]+$/.test(txHex)) {
      throw new SparkValidationError("Invalid transaction hex", {
        field: "txHex",
        value: txHex,
      });
    }
    const depositTx = getTxFromRawTxHex(txHex);

    return depositTx;
  }

  /**
   * Finalizes a deposit to the wallet.
   *
   * @param {DepositParams} params - Parameters for finalizing the deposit
   * @returns {Promise<void>} The nodes created from the deposit
   * @private
   */
  private async finalizeDeposit({
    keyDerivation,
    verifyingKey,
    depositTx,
    vout,
  }: DepositParams) {
    if (!Number.isSafeInteger(vout)) {
      throw new SparkValidationError("vout must be less than 2^53", {
        field: "vout",
        value: vout,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    const res = await this.depositService!.createTreeRoot({
      keyDerivation,
      verifyingKey,
      depositTx,
      vout,
    });
    return res.nodes;
  }

  /**
   * Gets all unused deposit addresses for the wallet.
   *
   * @returns {Promise<string[]>} The unused deposit addresses
   */
  public async getUnusedDepositAddresses(): Promise<string[]> {
    return (await this.queryAllUnusedDepositAddresses({})).map(
      (addr) => addr.depositAddress,
    );
  }

  /**
   * Gets all unused deposit addresses for the wallet.
   *
   * @param {Object} params - Parameters for querying unused deposit addresses
   * @param {Uint8Array<ArrayBufferLike>} [params.identityPublicKey] - The identity public key
   * @param {NetworkProto} [params.network] - The network
   * @returns {Promise<DepositAddressQueryResult[]>} The unused deposit addresses
   */
  private async queryAllUnusedDepositAddresses({
    identityPublicKey,
    network,
  }: {
    identityPublicKey?: Uint8Array<ArrayBufferLike>;
    network?: NetworkProto | undefined;
  }): Promise<DepositAddressQueryResult[]> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    let limit = 100;
    let offset = 0;
    const pastOffsets = new Set<number>();
    const depositAddresses: DepositAddressQueryResult[] = [];

    while (offset >= 0) {
      // Prevent infinite loop in case error with coordinator
      if (pastOffsets.has(offset)) {
        console.warn("Offset has already been seen, stopping");
        break;
      }

      const response = await sparkClient.query_unused_deposit_addresses({
        identityPublicKey:
          identityPublicKey ??
          (await this.config.signer.getIdentityPublicKey()),
        network: network ?? NetworkToProto[this.config.getNetwork()],
        limit,
        offset,
      });

      depositAddresses.push(...response.depositAddresses);

      pastOffsets.add(offset);
      offset = response.offset;
    }

    return depositAddresses;
  }

  /**
   * Claims a deposit to the wallet.
   * Note that if you used advancedDeposit, you don't need to call this function.
   * @param {string} txid - The transaction ID of the deposit
   * @returns {Promise<WalletLeaf[] | undefined>} The nodes resulting from the deposit
   */
  public async claimDeposit(txid: string): Promise<WalletLeaf[]> {
    if (!txid) {
      throw new SparkValidationError("Transaction ID cannot be empty", {
        field: "txid",
      });
    }

    let mutex = this.mutexes.get(txid);
    if (!mutex) {
      mutex = new Mutex();
      this.mutexes.set(txid, mutex);
    }

    const nodes = await mutex.runExclusive(async () => {
      const { fetch, Headers } = getFetch();
      const baseUrl = this.config.getElectrsUrl();
      const headers = new Headers();

      let txHex: string | undefined;

      if (this.config.getNetwork() === Network.LOCAL) {
        const localFaucet = BitcoinFaucet.getInstance();
        const response = await localFaucet.getRawTransaction(txid);
        txHex = response.hex;
      } else {
        if (this.config.getNetwork() === Network.REGTEST) {
          const auth = btoa(
            `${ELECTRS_CREDENTIALS.username}:${ELECTRS_CREDENTIALS.password}`,
          );
          headers.set("Authorization", `Basic ${auth}`);
        }

        const response = await fetch(`${baseUrl}/tx/${txid}/hex`, {
          headers,
        });

        txHex = await response.text();
      }

      if (!txHex) {
        throw new Error("Transaction not found");
      }

      if (!/^[0-9A-Fa-f]+$/.test(txHex)) {
        throw new SparkValidationError("Invalid transaction hex", {
          field: "txHex",
          value: txHex,
        });
      }
      const depositTx = getTxFromRawTxHex(txHex);

      const unusedDepositAddresses: Map<string, DepositAddressQueryResult> =
        new Map(
          (
            await this.queryAllUnusedDepositAddresses({
              identityPublicKey:
                await this.config.signer.getIdentityPublicKey(),
              network: NetworkToProto[this.config.getNetwork()],
            })
          ).map((addr) => [addr.depositAddress, addr]),
        );
      let depositAddress: DepositAddressQueryResult | undefined;
      let vout = 0;
      for (let i = 0; i < depositTx.outputsLength; i++) {
        const output = depositTx.getOutput(i);
        if (!output) {
          continue;
        }
        const parsedScript = OutScript.decode(output.script!);
        const address = Address(getNetwork(this.config.getNetwork())).encode(
          parsedScript,
        );
        if (unusedDepositAddresses.has(address)) {
          vout = i;
          depositAddress = unusedDepositAddresses.get(address);
          break;
        }
      }
      if (!depositAddress) {
        throw new SparkValidationError(
          "Deposit address has already been used",
          {
            field: "depositAddress",
            value: depositAddress,
          },
        );
      }

      let keyDerivation: KeyDerivation;
      if (!depositAddress.leafId) {
        keyDerivation = {
          type: KeyDerivationType.DEPOSIT,
        };
      } else {
        keyDerivation = {
          type: KeyDerivationType.LEAF,
          path: depositAddress.leafId,
        };
      }

      const nodes = await this.finalizeDeposit({
        keyDerivation,
        verifyingKey: depositAddress.verifyingPublicKey,
        depositTx,
        vout,
      });

      await this.withLeaves(async () => {
        const availableNodes = nodes.filter(
          (node) => node.status === "AVAILABLE",
        );

        this.leaves.push(...availableNodes);
      });

      return nodes;
    });

    this.mutexes.delete(txid);

    return nodes.map(mapTreeNodeToWalletLeaf);
  }

  /**
   * Non-trusty flow for depositing funds to the wallet.
   * Construct the tx spending from an L1 wallet to the Spark address.
   * After calling this function, you must sign and broadcast the tx.
   *
   * @param {string} txHex - The hex string of the transaction to deposit
   * @returns {Promise<TreeNode[] | undefined>} The nodes resulting from the deposit
   */
  public async advancedDeposit(txHex: string) {
    const depositTx = getTxFromRawTxHex(txHex);

    const unusedDepositAddresses: Map<string, DepositAddressQueryResult> =
      new Map(
        (
          await this.queryAllUnusedDepositAddresses({
            identityPublicKey: await this.config.signer.getIdentityPublicKey(),
            network: NetworkToProto[this.config.getNetwork()],
          })
        ).map((addr) => [addr.depositAddress, addr]),
      );

    let vout = 0;
    const responses: TreeNode[] = [];
    for (let i = 0; i < depositTx.outputsLength; i++) {
      const output = depositTx.getOutput(i);
      if (!output) {
        continue;
      }
      const parsedScript = OutScript.decode(output.script!);
      const address = Address(getNetwork(this.config.getNetwork())).encode(
        parsedScript,
      );
      const unusedDepositAddress = unusedDepositAddresses.get(address);
      if (unusedDepositAddress) {
        vout = i;
        let keyDerivation: KeyDerivation;
        if (!unusedDepositAddress.leafId) {
          keyDerivation = {
            type: KeyDerivationType.DEPOSIT,
          };
        } else {
          keyDerivation = {
            type: KeyDerivationType.LEAF,
            path: unusedDepositAddress.leafId,
          };
        }

        const response = await this.depositService!.createTreeRoot({
          keyDerivation,
          verifyingKey: unusedDepositAddress.verifyingPublicKey,
          depositTx,
          vout,
        });
        responses.push(...response.nodes);
      }
    }
    if (responses.length === 0) {
      throw new Error(
        `No unused deposit address found for tx: ${getTxId(depositTx)}`,
      );
    }

    return responses;
  }

  /**
   * Transfers deposit to self to claim ownership.
   *
   * @param {TreeNode[]} leaves - The leaves to transfer
   * @param {Uint8Array} signingPubKey - The signing public key
   * @returns {Promise<TreeNode[] | undefined>} The nodes resulting from the transfer
   * @private
   */
  private async transferLeavesToSelf(
    leaves: TreeNode[],
    keyDerivation: KeyDerivation,
  ): Promise<TreeNode[]> {
    const leafKeyTweaks: LeafKeyTweak[] = await Promise.all(
      leaves.map(async (leaf) => ({
        leaf,
        keyDerivation,
        newKeyDerivation: {
          type: KeyDerivationType.RANDOM,
        },
      })),
    );

    const transfer = await this.transferService.sendTransferWithKeyTweaks(
      leafKeyTweaks,
      await this.config.signer.getIdentityPublicKey(),
    );

    const pendingTransfer = await this.transferService.queryTransfer(
      transfer.id,
    );

    const resultNodes = !pendingTransfer
      ? []
      : await this.claimTransfer({ transfer: pendingTransfer });

    const leavesToRemove = new Set(leaves.map((leaf) => leaf.id));

    this.leaves = [
      ...this.leaves.filter((leaf) => !leavesToRemove.has(leaf.id)),
      ...resultNodes,
    ];

    return resultNodes;
  }
  // ***** Transfer Flow *****

  /**
   * Sends a transfer to another Spark user.
   *
   * @param {TransferParams} params - Parameters for the transfer
   * @param {string} params.receiverSparkAddress - The recipient's Spark address
   * @param {number} params.amountSats - Amount to send in satoshis
   * @returns {Promise<WalletTransfer>} The completed transfer details
   */
  public async transfer({
    amountSats,
    receiverSparkAddress,
  }: TransferParams): Promise<WalletTransfer> {
    if (!receiverSparkAddress) {
      throw new SparkValidationError("Receiver Spark address cannot be empty", {
        field: "receiverSparkAddress",
      });
    }

    const receiverAddress = decodeSparkAddress(
      receiverSparkAddress,
      this.config.getNetworkType(),
    );

    if (receiverAddress.sparkInvoiceFields) {
      throw new SparkValidationError(
        "Spark address is a Spark invoice. Use fulfillSparkInvoice instead.",
        {
          field: "receiverSparkAddress",
          value: receiverSparkAddress,
        },
      );
    }

    const [outcome] = await this.transferWithInvoice([
      {
        amountSats,
        receiverIdentityPubkey: hexToBytes(receiverAddress.identityPublicKey),
      },
    ]);
    if (!outcome) throw new Error("no transfer created");
    if (!outcome.ok) throw outcome.error;
    return outcome.transfer;
  }

  /**
   * Transfers with optional invoices.
   * Does not parse/validate invoices or enforce amount-vs-invoice.
   * If an invoice is provided, the caller must pass in the correct:
   *  - amountSats
   *  - receiverIdentityPubkey
   *
   * @param {TransferWithInvoiceParams[]} params - The parameters for the transfers
   * @returns {Promise<TransferWithInvoiceOutcome[]>} The outcomes of the transfers
   * @private
   */
  private async transferWithInvoice(
    params: TransferWithInvoiceParams[],
  ): Promise<TransferWithInvoiceOutcome[]> {
    const amountSatsArray: number[] = [];
    for (const param of params) {
      const { amountSats } = param;
      if (!Number.isSafeInteger(amountSats)) {
        throw new SparkValidationError("Sats amount must be less than 2^53", {
          field: "amountSats",
          value: amountSats,
          expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
        });
      }
      if (amountSats <= 0) {
        throw new SparkValidationError("Amount must be greater than 0", {
          field: "amountSats",
          value: amountSats,
        });
      }
      amountSatsArray.push(amountSats);
    }

    return await this.withLeaves(async () => {
      const selectLeavesToSendMap: Map<number, TreeNode[][]> =
        await this.selectLeaves(amountSatsArray);

      for (const [amount, selection] of selectLeavesToSendMap) {
        for (let groupIndex = 0; groupIndex < selection.length; groupIndex++) {
          const group = selection[groupIndex];
          if (!group) {
            throw new SparkValidationError(
              `TreeNode group at index ${groupIndex} not found for amount ${amount} after selection`,
            );
          }
          const available = await this.checkRenewLeaves(group);

          if (available.length < group.length) {
            throw new Error(
              `Not enough available nodes after refresh/extend. Expected ${group.length}, got ${available.length}`,
            );
          }
          selection[groupIndex] = available;
        }
      }

      const tweaksByAmount = this.buildTweaksByAmount(selectLeavesToSendMap);

      const jobs = params.map((param) => {
        const { amountSats, receiverIdentityPubkey, sparkInvoice } = param;
        const leafKeyTweaks = this.popOrThrow(
          tweaksByAmount.get(amountSats),
          `no leaves key tweaks for ${amountSats}`,
        );

        return { leafKeyTweaks, receiverIdentityPubkey, sparkInvoice, param };
      });

      const signerIdentityPublicKey =
        await this.config.signer.getIdentityPublicKey();

      const outcomes = await Promise.all(
        jobs.map(async (job) => {
          try {
            const transfer =
              await this.transferService.sendTransferWithKeyTweaks(
                job.leafKeyTweaks,
                job.receiverIdentityPubkey,
                job.sparkInvoice,
              );

            const idsToRemove = new Set<string>();
            for (const leaf of job.leafKeyTweaks) {
              idsToRemove.add(leaf.leaf.id);
            }
            if (idsToRemove.size > 0) {
              this.leaves = this.leaves.filter(
                (leaf) => !idsToRemove.has(leaf.id),
              );
            }
            const isSelfTransfer = equalBytes(
              signerIdentityPublicKey,
              job.receiverIdentityPubkey,
            );
            if (isSelfTransfer) {
              const pending = await this.transferService.queryTransfer(
                transfer.id,
              );
              if (pending) {
                await this.claimTransfer({ transfer: pending });
              }
            }
            return {
              ok: true as const,
              transfer: mapTransferToWalletTransfer(
                transfer,
                bytesToHex(await this.config.signer.getIdentityPublicKey()),
              ),
              param: job.param,
            };
          } catch (error) {
            return {
              ok: false as const,
              error: error instanceof Error ? error : new Error(String(error)),
              param: job.param,
            };
          }
        }),
      );

      return outcomes;
    });
  }

  private buildTweaksByAmount(
    selectedByAmount: Map<number, TreeNode[][]>,
  ): Map<number, LeafKeyTweak[][]> {
    const tweaksByAmount = new Map<number, LeafKeyTweak[][]>();
    for (const [amount, treeNodes] of selectedByAmount) {
      const keyTweaksForAmount: LeafKeyTweak[][] = [];
      for (const nodes of treeNodes) {
        const batch: LeafKeyTweak[] = [];
        for (let i = 0; i < nodes.length; i++) {
          if (!nodes[i]) {
            throw new SparkValidationError(
              `TreeNode at index ${i} not found for amount ${amount} while building key tweaks by amount`,
            );
          }
          batch.push(this.toSendTweak(nodes[i]!));
        }
        keyTweaksForAmount.push(batch);
      }
      tweaksByAmount.set(amount, keyTweaksForAmount);
    }
    return tweaksByAmount;
  }

  private toSendTweak(node: TreeNode): LeafKeyTweak {
    return {
      leaf: node,
      keyDerivation: { type: KeyDerivationType.LEAF, path: node.id },
      newKeyDerivation: { type: KeyDerivationType.RANDOM },
    };
  }

  private async checkRenewLeaves(nodes: TreeNode[]): Promise<TreeNode[]> {
    const nodesToRenewNode: TreeNode[] = [];
    const nodesToRenewRefund: TreeNode[] = [];
    const nodesToRenewZeroTimelock: TreeNode[] = [];

    const nodeIds: string[] = [];
    const validNodes: TreeNode[] = [];

    for (const node of nodes) {
      const nodeTx = getTxFromRawTxBytes(node.nodeTx);
      const refundTx = getTxFromRawTxBytes(node.refundTx);

      const nodeSequence = nodeTx.getInput(0).sequence;
      const refundSequence = refundTx.getInput(0).sequence;

      if (nodeSequence === undefined) {
        throw new SparkValidationError("Invalid node transaction", {
          field: "sequence",
          value: nodeTx.getInput(0),
          expected: "Non-null sequence",
        });
      }
      if (!refundSequence) {
        throw new SparkValidationError("Invalid refund transaction", {
          field: "sequence",
          value: refundTx.getInput(0),
          expected: "Non-null sequence",
        });
      }

      if (doesTxnNeedRenewed(refundSequence)) {
        if (isZeroTimelock(nodeSequence)) {
          nodesToRenewZeroTimelock.push(node);
        } else if (doesTxnNeedRenewed(nodeSequence)) {
          nodesToRenewNode.push(node);
        } else {
          nodesToRenewRefund.push(node);
        }
        nodeIds.push(node.id);
      } else {
        validNodes.push(node);
      }
    }

    if (
      nodesToRenewNode.length === 0 &&
      nodesToRenewRefund.length === 0 &&
      nodesToRenewZeroTimelock.length === 0
    ) {
      return validNodes;
    }

    const nodesResp = await this.queryNodes({
      source: {
        $case: "nodeIds",
        nodeIds: {
          nodeIds,
        },
      },
      includeParents: true,
      network: NetworkToProto[this.config.getNetwork()],
      statuses: [],
    });

    const nodesMap = new Map<string, TreeNode>();
    for (const node of Object.values(nodesResp.nodes)) {
      nodesMap.set(node.id, node);
    }

    const nodesToAdd: TreeNode[] = [];
    for (const node of nodesToRenewNode) {
      if (!node.parentNodeId) {
        throw new Error(`node ${node.id} has no parent`);
      }

      const parentNode = nodesMap.get(node.parentNodeId);
      if (!parentNode) {
        throw new Error(`parent node ${node.parentNodeId} not found`);
      }

      const newNode = await this.transferService.renewNodeTxn(node, parentNode);
      nodesToAdd.push(newNode);
    }

    for (const node of nodesToRenewRefund) {
      if (!node.parentNodeId) {
        throw new Error(`node ${node.id} has no parent`);
      }

      const parentNode = nodesMap.get(node.parentNodeId);
      if (!parentNode) {
        throw new Error(`parent node ${node.parentNodeId} not found`);
      }

      const newNode = await this.transferService.renewRefundTxn(
        node,
        parentNode,
      );
      nodesToAdd.push(newNode);
    }

    for (const node of nodesToRenewZeroTimelock) {
      const newNode = await this.transferService.renewZeroTimelockNodeTxn(node);
      nodesToAdd.push(newNode);
    }

    this.updateLeaves(nodeIds, nodesToAdd);
    validNodes.push(...nodesToAdd);

    return validNodes;
  }

  private async claimTransferCore(transfer: Transfer) {
    return await this.claimTransferMutex.runExclusive(async () => {
      const leafPubKeyMap =
        await this.transferService.verifyPendingTransfer(transfer);

      let leavesToClaim: LeafKeyTweak[] = [];

      for (const leaf of transfer.leaves) {
        if (leaf.leaf) {
          const leafPubKey = leafPubKeyMap.get(leaf.leaf.id);
          if (leafPubKey) {
            leavesToClaim.push({
              leaf: {
                ...leaf.leaf,
                refundTx: leaf.intermediateRefundTx,
                directRefundTx: leaf.intermediateDirectRefundTx,
                directFromCpfpRefundTx: leaf.intermediateDirectFromCpfpRefundTx,
              },
              keyDerivation: {
                type: KeyDerivationType.ECIES,
                path: leaf.secretCipher,
              },
              newKeyDerivation: {
                type: KeyDerivationType.LEAF,
                path: leaf.leaf.id,
              },
            });
          }
        }
      }

      const response = await this.transferService.claimTransfer(
        transfer,
        leavesToClaim,
      );

      return response.nodes;
    });
  }

  private async processClaimedTransferResults(
    result: TreeNode[],
    transfer: Transfer,
    emit?: boolean,
  ): Promise<TreeNode[]> {
    result = await this.checkRenewLeaves(result);

    const existingIds = new Set(this.leaves.map((leaf) => leaf.id));
    const uniqueResults = result.filter((node) => !existingIds.has(node.id));
    this.leaves.push(...uniqueResults);

    if (
      transfer.type !== TransferType.COUNTER_SWAP &&
      transfer.type !== TransferType.COUNTER_SWAP_V3 &&
      this.config.getOptimizationOptions().auto &&
      shouldOptimize(
        this.leaves.map((leaf) => leaf.value),
        this.config.getOptimizationOptions().multiplicity ?? 0,
      )
    ) {
      for await (const _ of this.optimizeLeaves()) {
        // run all optimizer steps, do nothing with them
      }
    }

    if (
      emit &&
      transfer.type !== TransferType.COUNTER_SWAP &&
      transfer.type !== TransferType.COUNTER_SWAP_V3
    ) {
      this.emit(
        SparkWalletEvent.TransferClaimed,
        transfer.id,
        (await this.getBalance()).balance,
      );
    }

    return result;
  }

  /**
   * Claims a specific transfer.
   *
   * @param {Transfer} transfer - The transfer to claim
   * @returns {Promise<Object>} The claim result
   */
  private async claimTransfer({
    transfer,
    emit,
  }: {
    transfer: Transfer;
    emit?: boolean;
  }) {
    const onError = async (
      context: RetryContext<TreeNode[], Transfer>,
    ): Promise<TreeNode[] | undefined> => {
      const error = context.error;
      if (
        error instanceof SparkRequestError &&
        error.originalError instanceof ClientError &&
        error.originalError.code === Status.ALREADY_EXISTS
      ) {
        const transferToUse = context.data || transfer;
        const updatedTransfer = await this.transferService.queryTransfer(
          transferToUse.id,
        );

        if (!updatedTransfer) {
          return undefined;
        }

        const leaves = updatedTransfer.leaves.flatMap((leaf) =>
          leaf.leaf ? [leaf.leaf] : [],
        );

        return leaves;
      }
      return;
    };

    const fetchData = async (context: RetryContext<TreeNode[], Transfer>) => {
      const transferToUse = context.data || transfer;
      const updatedTransfer = await this.transferService.queryPendingTransfers([
        transferToUse.id,
      ]);
      if (!updatedTransfer.transfers[0]) {
        return undefined;
      }
      return updatedTransfer.transfers[0];
    };

    try {
      const result = await withRetry(
        async (updatedTransfer?: Transfer) => {
          const transferToUse = updatedTransfer ?? transfer;
          return await this.claimTransferCore(transferToUse);
        },
        {
          callbacks: {
            onError,
            fetchData,
          },
        },
      );

      if (result.length === 0) {
        return [];
      }

      return await this.processClaimedTransferResults(result, transfer, emit);
    } catch (error) {
      console.warn(
        `Failed to claim transfer after all retries. Please try reinitializing your wallet in a few minutes. Transfer ID: ${transfer.id}`,
        error,
      );

      throw new SparkError("Failed to claim transfer", { error });
    }
  }

  /**
   * Claims all pending transfers.
   *
   * @returns {Promise<string[]>} Array of successfully claimed transfer IDs
   * @private
   */
  private async claimTransfers(
    types?: TransferType[],
    emit?: boolean,
  ): Promise<string[]> {
    const transfers = await this.transferService.queryPendingTransfers();
    const promises: Promise<string | null>[] = [];
    for (const transfer of transfers.transfers) {
      if (types && !types.includes(transfer.type)) {
        continue;
      }

      if (
        transfer.status !== TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED &&
        transfer.status !==
          TransferStatus.TRANSFER_STATUS_RECEIVER_KEY_TWEAKED &&
        transfer.status !==
          TransferStatus.TRANSFER_STATUS_RECEIVER_REFUND_SIGNED &&
        transfer.status !==
          TransferStatus.TRANSFER_STATUS_RECEIVER_KEY_TWEAK_APPLIED &&
        transfer.status !==
          TransferStatus.TRANSFER_STATUS_RECEIVER_KEY_TWEAK_LOCKED
      ) {
        continue;
      }
      promises.push(
        this.claimTransfer({ transfer, emit })
          .then(() => transfer.id)
          .catch((error) => {
            console.warn(`Failed to claim transfer ${transfer.id}:`, error);
            return null;
          }),
      );
    }
    const results = await Promise.allSettled(promises);
    return results
      .filter(
        (result) => result.status === "fulfilled" && result.value !== null,
      )
      .map((result) => (result as PromiseFulfilledResult<string>).value);
  }

  // ***** Lightning Flow *****

  /**
   * Creates a Lightning invoice for receiving payments.
   *
   * @param {Object} params - Parameters for the lightning invoice
   * @param {number} params.amountSats - Amount in satoshis
   * @param {string} [params.memo] - Description for the invoice. Should not be provided if the descriptionHash is provided.
   * @param {number} [params.expirySeconds] - Optional expiry time in seconds
   * @param {boolean} [params.includeSparkAddress] - Optional boolean signalling whether or not to include the spark address in the invoice. Mutually exclusive with includeSparkInvoice.
   * @param {boolean} [params.includeSparkInvoice] - Optional boolean signalling whether to include a spark invoice in the invoice routing hints. Mutually exclusive with includeSparkAddress.
   * @param {string} [params.receiverIdentityPubkey] - Optional public key of the wallet receiving the lightning invoice. If not present, the receiver will be the creator of this request.
   * @param {string} [params.descriptionHash] - Optional h tag of the invoice. This is the hash of a longer description to include in the lightning invoice. It is used in LNURL and UMA as the hash of the metadata. This field is mutually exclusive with the memo field. Only one or the other should be provided.
   * @returns {Promise<LightningReceiveRequest>} BOLT11 encoded invoice
   */
  public async createLightningInvoice({
    amountSats,
    memo,
    expirySeconds = 60 * 60 * 24 * 30,
    includeSparkAddress = false,
    includeSparkInvoice = false,
    receiverIdentityPubkey,
    descriptionHash,
  }: CreateLightningInvoiceParams): Promise<LightningReceiveRequest> {
    const requestLightningInvoice = async (
      amountSats: number,
      paymentHash: Uint8Array,
      memo?: string,
      receiverIdentityPubkey?: string,
      descriptionHash?: string,
    ) => {
      return await this.validateAndCreateLightningInvoice({
        amountSats,
        paymentHashHex: bytesToHex(paymentHash),
        memo,
        expirySeconds,
        includeSparkAddress,
        includeSparkInvoice,
        receiverIdentityPubkey,
        descriptionHash,
      });
    };

    const invoice = await this.lightningService.createLightningInvoice({
      amountSats,
      memo,
      invoiceCreator: requestLightningInvoice,
      receiverIdentityPubkey,
      descriptionHash,
    });

    return invoice;
  }

  private async validateAndCreateLightningInvoice({
    amountSats,
    paymentHashHex,
    memo,
    expirySeconds,
    includeSparkAddress,
    includeSparkInvoice,
    receiverIdentityPubkey,
    descriptionHash,
  }: {
    amountSats: number;
    paymentHashHex: string;
    memo?: string;
    expirySeconds: number;
    includeSparkAddress: boolean;
    includeSparkInvoice: boolean;
    receiverIdentityPubkey?: string;
    descriptionHash?: string;
  }): Promise<LightningReceiveRequest> {
    const sspClient = this.getSspClient();

    if (isNaN(amountSats) || amountSats < 0) {
      throw new SparkValidationError("Invalid amount", {
        field: "amountSats",
        value: amountSats,
        expected: "non-negative number",
      });
    }

    if (!Number.isSafeInteger(amountSats)) {
      throw new SparkValidationError("Sats amount must be less than 2^53", {
        field: "amountSats",
        value: amountSats,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    if (!Number.isSafeInteger(expirySeconds)) {
      throw new SparkValidationError("Expiration time must be less than 2^53", {
        field: "expirySeconds",
        value: expirySeconds,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    if (expirySeconds < 0) {
      throw new SparkValidationError("Invalid expiration time", {
        field: "expirySeconds",
        value: expirySeconds,
        expected: "Non-negative expiration time",
      });
    }

    if (memo && memo.length > 639) {
      throw new SparkValidationError("Invalid memo size", {
        field: "memo",
        value: memo,
        expected: "Memo size within limits",
      });
    }

    if (memo && descriptionHash) {
      throw new SparkValidationError(
        "Memo and descriptionHash cannot be provided together. Please provide only one.",
        {
          field: "memo",
          value: memo,
          expected: "Memo or descriptionHash",
        },
      );
    }

    if (includeSparkAddress && includeSparkInvoice) {
      throw new SparkValidationError(
        "includeSparkAddress and includeSparkInvoice are mutually exclusive",
        {
          field: "includeSparkInvoice",
          value: includeSparkInvoice,
          expected: "Only one of includeSparkAddress or includeSparkInvoice",
        },
      );
    }

    let sparkInvoice: string | undefined;
    if (includeSparkInvoice) {
      const sparkAmount = amountSats > 0 ? amountSats : undefined;
      sparkInvoice = await this.createSatsInvoice({
        amount: sparkAmount,
        expiryTime: new Date(Date.now() + expirySeconds * 1000),
        receiverIdentityPubkey: receiverIdentityPubkey,
        // Note: memo does not need to be duplicated in the spark invoice.
      });
    }

    const network = this.config.getNetwork();
    let bitcoinNetwork: BitcoinNetwork = BitcoinNetwork.REGTEST;
    if (network === Network.MAINNET) {
      bitcoinNetwork = BitcoinNetwork.MAINNET;
    } else if (network === Network.REGTEST) {
      bitcoinNetwork = BitcoinNetwork.REGTEST;
    }

    const invoice = await sspClient.requestLightningReceive({
      amountSats,
      network: bitcoinNetwork,
      paymentHash: paymentHashHex,
      expirySecs: expirySeconds,
      memo,
      includeSparkAddress,
      receiverIdentityPubkey,
      descriptionHash,
      sparkInvoice,
    });

    if (!invoice) {
      throw new Error("Failed to create lightning invoice");
    }

    const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);

    if (
      invoice.invoice.paymentHash !== paymentHashHex ||
      decodedInvoice.paymentHash !== paymentHashHex
    ) {
      throw new SparkValidationError("Payment hash mismatch", {
        field: "paymentHash",
        value: invoice.invoice.paymentHash,
        expected: paymentHashHex,
      });
    }

    if (decodedInvoice.amountMSats === null && amountSats !== 0) {
      throw new SparkValidationError("Amount mismatch", {
        field: "amountMSats",
        value: "null",
        expected: amountSats * 1000,
      });
    }

    if (
      decodedInvoice.amountMSats !== null &&
      decodedInvoice.amountMSats !== BigInt(amountSats * 1000)
    ) {
      throw new SparkValidationError("Amount mismatch", {
        field: "amountMSats",
        value: decodedInvoice.amountMSats.toString(),
        expected: amountSats * 1000,
      });
    }

    // Validate the spark address embedded in the lightning invoice
    if (includeSparkAddress) {
      const sparkFallbackAddress = decodedInvoice.fallbackAddress;

      if (!sparkFallbackAddress) {
        console.warn(
          "No spark fallback address found in lightning invoice",
          invoice.invoice.encodedInvoice,
        );
        throw new SparkValidationError(
          "No spark fallback address found in lightning invoice",
          {
            field: "sparkFallbackAddress",
            value: sparkFallbackAddress,
            expected: "Valid spark fallback address",
          },
        );
      }

      const expectedIdentityPubkey =
        receiverIdentityPubkey ?? (await this.getIdentityPublicKey());

      if (sparkFallbackAddress !== expectedIdentityPubkey) {
        throw new SparkValidationError(
          "Mismatch between spark identity embedded in lightning invoice and designated recipient spark identity",
          {
            field: "sparkFallbackAddress",
            value: sparkFallbackAddress,
            expected: expectedIdentityPubkey,
          },
        );
      }
    } else if (includeSparkInvoice) {
      // Validate the spark invoice embedded in the lightning invoice
      const embeddedSparkInvoice = decodedInvoice.fallbackAddress;

      if (!embeddedSparkInvoice) {
        throw new SparkValidationError(
          "No spark invoice found in lightning invoice",
          {
            field: "sparkInvoice",
            value: embeddedSparkInvoice,
            expected: "Valid spark invoice",
          },
        );
      }

      if (embeddedSparkInvoice !== sparkInvoice) {
        throw new SparkValidationError(
          "Mismatch between spark invoice embedded in lightning invoice and expected spark invoice",
          {
            field: "sparkInvoice",
            value: embeddedSparkInvoice,
            expected: sparkInvoice,
          },
        );
      }
    } else if (decodedInvoice.fallbackAddress !== undefined) {
      throw new SparkValidationError(
        "Spark fallback address found in lightning invoice but includeSparkAddress is false",
        {
          field: "sparkFallbackAddress",
          value: decodedInvoice.fallbackAddress,
        },
      );
    }

    return invoice;
  }

  /**
   * Creates a Lightning Hodl invoice with a user-provided payment hash.
   * Hodl invoices allow the receiver to hold the HTLC until they decide to settle or fail it.
   *
   * @param {Object} params - Lightning invoice parameters
   * @param {number} params.amountSats - Amount in satoshis
   * @param {string} params.paymentHash - Payment hash as hex string (64 characters)
   * @param {string} [params.memo] - Optional description of the invoice
   * @param {number} [params.expirySeconds=2592000] - Invoice expiry time in seconds (default: 30 days)
   * @param {boolean} [params.includeSparkAddress=false] - Whether to include a Spark address as fallback
   * @param {boolean} [params.includeSparkInvoice=false] - Whether to include a Spark invoice as fallback
   * @param {string} [params.receiverIdentityPubkey] - Optional receiver identity public key (hex)
   * @param {string} [params.descriptionHash] - Optional h tag of the invoice. This is the hash of a longer description to include in the lightning invoice. It is used in LNURL and UMA as the hash of the metadata. This field is mutually exclusive with the memo field. Only one or the other should be provided.
   * @returns {Promise<LightningReceiveRequest>} BOLT11 encoded invoice
   */
  public async createLightningHodlInvoice({
    amountSats,
    paymentHash,
    memo,
    expirySeconds = 60 * 60 * 24 * 30,
    includeSparkAddress = false,
    includeSparkInvoice = false,
    receiverIdentityPubkey,
    descriptionHash,
  }: CreateLightningHodlInvoiceParams): Promise<LightningReceiveRequest> {
    if (!/^[0-9a-fA-F]{64}$/.test(paymentHash)) {
      throw new SparkValidationError("Invalid payment hash", {
        field: "paymentHash",
        value: paymentHash,
        expected: "64 character hex string",
      });
    }

    return await this.validateAndCreateLightningInvoice({
      amountSats,
      paymentHashHex: paymentHash,
      memo,
      expirySeconds,
      includeSparkAddress,
      includeSparkInvoice,
      receiverIdentityPubkey,
      descriptionHash,
    });
  }

  /**
   * Attempts to pay over Spark using the fallback data embedded in a Lightning invoice.
   * Returns the transfer if successful, or undefined if the fallback data is not valid Spark data.
   */
  private async tryPayOverSpark(
    decodedInvoice: DecodedInvoice,
    amountSats: number,
    network: Network,
  ): Promise<WalletTransfer | undefined> {
    const fallbackAddress = decodedInvoice.fallbackAddress;
    if (!fallbackAddress) {
      console.warn("No fallback address found in invoice");
      return undefined;
    }

    // Try bech32m spark address/invoice first
    // Auto-detect network from spark address prefix since REGTEST and LOCAL
    // share the same lightning invoice prefix (lnbcrt) but have different
    // spark address prefixes (sparkrt vs sparkl)
    const sparkNetwork = this.tryGetNetworkFromSparkAddress(fallbackAddress);
    if (sparkNetwork && !this.isCompatibleNetwork(network, sparkNetwork)) {
      console.warn(
        `Spark address network ${sparkNetwork} incompatible with invoice network ${Network[network]}`,
      );
      return undefined;
    }
    const networkType = sparkNetwork ?? (Network[network] as NetworkType);
    const decoded = this.tryDecodeSparkAddress(fallbackAddress, networkType);
    if (decoded?.sparkInvoiceFields) {
      const isZeroAmountInvoice = !decodedInvoice.amountMSats;
      this.validateSparkInvoiceAmount(
        decoded.sparkInvoiceFields,
        amountSats,
        isZeroAmountInvoice,
      );
      return this.fulfillSparkInvoiceInternal(
        fallbackAddress as SparkAddressFormat,
        amountSats,
      );
    }
    if (decoded) {
      return this.transfer({
        amountSats,
        receiverSparkAddress: fallbackAddress as SparkAddressFormat,
      });
    }

    if (!isValidSparkAddressFallback(fallbackAddress)) {
      console.warn("Invalid spark fallback address", fallbackAddress);
      return undefined;
    }

    const sparkAddress = encodeSparkAddress({
      identityPublicKey: fallbackAddress,
      network: Network[network] as NetworkType,
    });
    return this.transfer({ amountSats, receiverSparkAddress: sparkAddress });
  }

  private tryDecodeSparkAddress(
    address: string,
    networkType: NetworkType,
  ): ReturnType<typeof decodeSparkAddress> | undefined {
    try {
      return decodeSparkAddress(address, networkType);
    } catch {
      return undefined;
    }
  }

  private tryGetNetworkFromSparkAddress(
    address: string,
  ): NetworkType | undefined {
    try {
      return getNetworkFromSparkAddress(address);
    } catch {
      return undefined;
    }
  }

  private isCompatibleNetwork(
    invoiceNetwork: Network,
    sparkNetwork: NetworkType,
  ): boolean {
    const invoiceNetworkType = Network[invoiceNetwork] as NetworkType;
    if (invoiceNetworkType === sparkNetwork) return true;
    // REGTEST and LOCAL share the same lightning invoice prefix (lnbcrt)
    if (
      (invoiceNetworkType === "REGTEST" || invoiceNetworkType === "LOCAL") &&
      (sparkNetwork === "REGTEST" || sparkNetwork === "LOCAL")
    ) {
      return true;
    }
    return false;
  }

  private validateSparkInvoiceAmount(
    sparkInvoiceFields: NonNullable<
      ReturnType<typeof decodeSparkAddress>["sparkInvoiceFields"]
    >,
    expectedAmountSats: number,
    isZeroAmountLightningInvoice: boolean,
  ): void {
    const paymentType = sparkInvoiceFields.paymentType;
    if (paymentType?.type !== "sats") {
      throw new SparkValidationError(
        "Lightning invoice should only contain sats payment type",
      );
    }
    const invoiceAmount = Number(paymentType.amount || 0);
    const isZeroAmountSparkInvoice = invoiceAmount === 0;
    if (isZeroAmountSparkInvoice !== isZeroAmountLightningInvoice) {
      throw new SparkValidationError(
        "Zero amount mismatch. Either both or neither the lightning invoice and the spark invoice should have a zero amount",
        {
          field: "isZeroAmountLightningInvoice",
          value: isZeroAmountLightningInvoice,
          expected: isZeroAmountSparkInvoice,
        },
      );
    }
    if (invoiceAmount !== expectedAmountSats && !isZeroAmountSparkInvoice) {
      throw new SparkValidationError(
        "Lightning invoice amount does not match embedded spark invoice amount",
        {
          field: "amountSats",
          value: expectedAmountSats,
          expected: invoiceAmount,
        },
      );
    }
  }

  private async fulfillSparkInvoiceInternal(
    invoice: SparkAddressFormat,
    amountSats: number,
  ): Promise<WalletTransfer> {
    const result = await this.fulfillSparkInvoice([
      { invoice, amount: BigInt(amountSats) },
    ]);
    const firstError = result.satsTransactionErrors[0];
    if (firstError) {
      throw firstError.error;
    }
    const firstSuccess = result.satsTransactionSuccess[0];
    if (!firstSuccess) {
      throw new Error("Failed to fulfill spark invoice");
    }
    return firstSuccess.transferResponse;
  }

  /**
   * Pays a Lightning invoice.
   *
   * @param {Object} params - Parameters for paying the invoice
   * @param {string} params.invoice - The BOLT11-encoded Lightning invoice to pay
   * @param {boolean} [params.preferSpark] - Whether to prefer a spark transfer over lightning for the payment
   * @param {number} [params.amountSatsToSend] - The amount in sats to send. This is only valid for 0 amount lightning invoices.
   * @returns {Promise<LightningSendRequest | WalletTransfer>} The Lightning payment request details or the transfer details if the payment is over Spark
   */
  public async payLightningInvoice({
    invoice,
    maxFeeSats,
    preferSpark = false,
    amountSatsToSend,
    idempotencyKey,
  }: PayLightningInvoiceParams): Promise<
    LightningSendRequest | WalletTransfer
  > {
    invoice = invoice.toLowerCase();

    const invoiceNetwork = getNetworkFromInvoice(invoice);
    const walletNetwork = this.config.getNetwork();

    const isValidNetworkForWallet =
      invoiceNetwork === walletNetwork ||
      (invoiceNetwork === Network.REGTEST &&
        (walletNetwork === Network.REGTEST || walletNetwork === Network.LOCAL));

    if (!isValidNetworkForWallet) {
      throw new SparkValidationError(
        `Invoice network: ${invoiceNetwork} does not match wallet network: ${walletNetwork}`,
        {
          field: "invoice",
          value: invoiceNetwork,
          expected: walletNetwork,
        },
      );
    }

    const decodedInvoice = decodeInvoice(invoice);
    const amountMSats = decodedInvoice.amountMSats;
    const isZeroAmountInvoice = !amountMSats;

    // Check if user is trying to send amountSatsToSend for non 0 amount lightning invoice
    if (!isZeroAmountInvoice && amountSatsToSend !== undefined) {
      throw new SparkValidationError(
        "Invalid amount. User can only specify amountSatsToSend for 0 amount lightning invoice",
        {
          field: "amountMSats",
          value: Number(amountMSats),
          expected: "0",
        },
      );
    }

    // If 0 amount lightning invoice, check that user has specified amountSatsToSend
    if (isZeroAmountInvoice && amountSatsToSend === undefined) {
      throw new SparkValidationError(
        "Invalid amount. User must specify amountSatsToSend for 0 amount lightning invoice",
        {
          field: "amountMSats",
          value: Number(amountMSats),
          expected: "0",
        },
      );
    }

    const amountSats = isZeroAmountInvoice
      ? amountSatsToSend!
      : Math.ceil(Number(amountMSats) / 1000);

    if (isNaN(amountSats) || amountSats <= 0) {
      throw new SparkValidationError("Invalid amount", {
        field: "amountSats",
        value: amountSats,
        expected: "greater than 0",
      });
    }

    const sparkFallbackAddress = decodedInvoice.fallbackAddress;
    const paymentHash = decodedInvoice.paymentHash;

    // Try to pay over Spark if preferred
    if (preferSpark && sparkFallbackAddress) {
      const sparkPayment = await this.tryPayOverSpark(
        decodedInvoice,
        amountSats,
        invoiceNetwork,
      );
      if (sparkPayment) {
        return sparkPayment;
      }
      console.warn(
        "No valid spark data found in invoice. Defaulting to lightning.",
      );
    }

    // Pay over Lightning
    return await this.withLeaves(async () => {
      // Make expiry time 16 days from now.
      const expiryTime = new Date(Date.now() + 16 * 24 * 60 * 60 * 1000);
      const sspClient = this.getSspClient();

      // If 0 amount lightning invoice, use amountSatsToSend for fee estimate
      const feeEstimate = await this.getLightningSendFeeEstimate({
        encodedInvoice: invoice,
        amountSats: isZeroAmountInvoice ? amountSatsToSend! : undefined,
      });

      if (maxFeeSats < feeEstimate) {
        throw new SparkValidationError(
          "maxFeeSats does not cover fee estimate",
          {
            field: "maxFeeSats",
            value: maxFeeSats,
            expected: `${feeEstimate} sats`,
          },
        );
      }

      const totalAmount = amountSats + feeEstimate;

      const internalBalance = this.getInternalBalance();
      if (totalAmount > internalBalance) {
        throw new SparkValidationError("Insufficient balance", {
          field: "balance",
          value: internalBalance,
          expected: `${totalAmount} sats`,
        });
      }

      const selectedLeaves = (await this.selectLeaves([totalAmount])).get(
        totalAmount,
      )!;
      let leaves = this.popOrThrow(
        selectedLeaves,
        `no leaves for ${totalAmount}`,
      );
      leaves = await this.checkRenewLeaves(leaves);

      const leavesToSend: LeafKeyTweak[] = await Promise.all(
        leaves.map(async (leaf) => ({
          leaf,
          keyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leaf.id,
          },
          newKeyDerivation: {
            type: KeyDerivationType.RANDOM,
          },
        })),
      );

      const transferID = uuidv7();

      const startTransferRequest =
        await this.transferService.prepareTransferForLightning(
          leavesToSend,
          hexToBytes(this.config.getSspIdentityPublicKey()),
          hexToBytes(paymentHash),
          expiryTime,
          transferID,
        );

      const swapResponse = await this.lightningService.swapNodesForPreimage({
        leaves: leavesToSend,
        receiverIdentityPubkey: hexToBytes(
          this.config.getSspIdentityPublicKey(),
        ),
        paymentHash: hexToBytes(paymentHash),
        isInboundPayment: false,
        invoiceString: invoice,
        feeSats: feeEstimate,
        amountSatsToSend: amountSatsToSend,
        startTransferRequest,
        expiryTime,
        transferID,
        idempotencyKey,
      });

      if (!swapResponse.transfer) {
        throw new Error("Failed to swap nodes for preimage");
      }

      const sspResponse = await sspClient.requestLightningSend({
        encodedInvoice: invoice,
        amountSats: isZeroAmountInvoice ? amountSatsToSend! : undefined,
        userOutboundTransferExternalId: swapResponse.transfer.id,
      });

      if (!sspResponse) {
        throw new Error("Failed to contact SSP");
      }

      const leavesToRemove = new Set(leavesToSend.map((leaf) => leaf.leaf.id));
      this.leaves = this.leaves.filter((leaf) => !leavesToRemove.has(leaf.id));

      return sspResponse;
    });
  }

  // ***** HTLC Flow *****
  /**
   * Creates a HTLC.
   *
   * @param {Object} params - Parameters for creating a HTLC
   * @param {string} params.receiverSparkAddress - The Spark address of the receiver
   * @param {number} params.amountSats - The amount in sats to send
   * @param {string} params.preimage - The preimage of the HTLC
   * @param {number} params.expiryTimeMinutes - The expiry time in minutes
   * @returns {Promise<Transfer>} The HTLC transfer details
   */
  public async createHTLC({
    receiverSparkAddress,
    amountSats,
    preimage,
    expiryTime,
  }: CreateHTLCParams): Promise<Transfer> {
    if (expiryTime.getTime() <= Date.now()) {
      throw new SparkValidationError("Expiry time must be in the future", {
        field: "expiryTime",
        value: expiryTime,
        expected: "greater than 0",
      });
    }

    return await this.withLeaves(async () => {
      const internalBalance = this.getInternalBalance();
      if (amountSats > internalBalance) {
        throw new SparkValidationError("Insufficient balance", {
          field: "balance",
          value: internalBalance,
          expected: `${amountSats} sats`,
        });
      }
      const selectedLeaves = (await this.selectLeaves([amountSats])).get(
        amountSats,
      )!;
      let leaves = this.popOrThrow(
        selectedLeaves,
        `no leaves for ${amountSats}`,
      );
      leaves = await this.checkRenewLeaves(leaves);

      const leavesToSend: LeafKeyTweak[] = await Promise.all(
        leaves.map(async (leaf) => ({
          leaf,
          keyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leaf.id,
          },
          newKeyDerivation: {
            type: KeyDerivationType.RANDOM,
          },
        })),
      );

      const transferID = uuidv7();

      if (!preimage) {
        const preimageBytes = await this.getHTLCPreimage(transferID);
        preimage = bytesToHex(preimageBytes);
      }

      const paymentHash = sha256(hexToBytes(preimage));

      const receiverIdentityPubkey = decodeSparkAddress(
        receiverSparkAddress,
        this.config.getNetworkType(),
      ).identityPublicKey;

      const startTransferRequest =
        await this.transferService.prepareTransferForLightning(
          leavesToSend,
          hexToBytes(receiverIdentityPubkey),
          paymentHash,
          expiryTime,
          transferID,
        );

      const swapResponse = await this.lightningService.swapNodesForPreimage({
        leaves: leavesToSend,
        receiverIdentityPubkey: hexToBytes(receiverIdentityPubkey),
        paymentHash,
        isInboundPayment: false,
        startTransferRequest,
        expiryTime,
        transferID,
      });
      if (!swapResponse.transfer) {
        throw new Error("Failed to swap nodes for preimage");
      }

      const leavesToRemove = new Set(leavesToSend.map((leaf) => leaf.leaf.id));
      this.leaves = this.leaves.filter((leaf) => !leavesToRemove.has(leaf.id));
      return swapResponse.transfer;
    });
  }

  public async getHTLCPreimage(transferID: string): Promise<Uint8Array> {
    const cleanedTransferID = transferID
      .trim()
      .toLowerCase()
      .replaceAll("-", "");
    return await this.config.signer.htlcHMAC(cleanedTransferID);
  }

  /**
   * Claims a HTLC.
   *
   * @param {string} preimage - the preimage of the HTLC
   * @returns {Promise<Transfer>} The HTLC transfer details
   */
  public async claimHTLC(preimage: string): Promise<Transfer> {
    const bytes = hexToBytes(preimage);
    if (bytes.length !== 32) {
      throw new SparkValidationError("Preimage must be 32 bytes", {
        field: "preimage",
        value: preimage,
        expected: "32 bytes",
      });
    }
    const transfer = await this.lightningService.providePreimage(bytes);
    if (!transfer) {
      throw new Error("Failed to provide preimage");
    }
    const receiverIdentityPublicKey = transfer.receiverIdentityPublicKey;
    const isSelfClaim = equalBytes(
      receiverIdentityPublicKey,
      await this.config.signer.getIdentityPublicKey(),
    );
    if (isSelfClaim) {
      await this.claimTransfer({
        transfer: transfer,
        emit: true,
      });
    }
    return transfer;
  }

  async createHTLCSenderSpendTx({
    htlcTx,
    hash,
    hashLockDestinationPubkey,
    sequenceLockDestinationPubkey,
    satsPerVbyteFee,
  }: {
    htlcTx: string;
    hash: string;
    hashLockDestinationPubkey: string;
    sequenceLockDestinationPubkey: string;
    satsPerVbyteFee: number;
  }): Promise<string> {
    const fee =
      satsPerVbyteFee * getTxEstimatedVbytesSizeByNumberOfInputsOutputs(1, 1);
    const htlxTxFromHex = Transaction.fromRaw(hexToBytes(htlcTx));
    const hashBytes = hexToBytes(hash);
    const hashLockDestinationPubkeyBytes = hexToBytes(
      hashLockDestinationPubkey,
    );
    const sequenceLockDestinationPubkeyBytes = hexToBytes(
      sequenceLockDestinationPubkey,
    );

    const { senderSpendTx } = createSenderSpendTx({
      htlcTx: htlxTxFromHex,
      network: getNetwork(this.config.getNetwork()),
      hash: hashBytes,
      hashLockDestinationPubkey: hashLockDestinationPubkeyBytes,
      sequenceLockDestinationPubkey: sequenceLockDestinationPubkeyBytes,
      fee,
    });

    this.config.signer.signTransactionIndex(
      senderSpendTx,
      0,
      await this.config.signer.getIdentityPublicKey(),
    );

    senderSpendTx.finalizeIdx(0);

    return senderSpendTx.hex;
  }

  async createHTLCReceiverSpendTx({
    htlcTx,
    hash,
    hashLockDestinationPubkey,
    sequenceLockDestinationPubkey,
    preimage,
    satsPerVbyteFee,
  }: {
    htlcTx: string;
    hash: string;
    hashLockDestinationPubkey: string;
    sequenceLockDestinationPubkey: string;
    preimage: string;
    satsPerVbyteFee: number;
  }): Promise<string> {
    const fee =
      satsPerVbyteFee * getTxEstimatedVbytesSizeByNumberOfInputsOutputs(1, 1);
    const htlxTxFromHex = Transaction.fromRaw(hexToBytes(htlcTx));
    const hashBytes = hexToBytes(hash);
    const hashLockDestinationPubkeyBytes = hexToBytes(
      hashLockDestinationPubkey,
    );
    const sequenceLockDestinationPubkeyBytes = hexToBytes(
      sequenceLockDestinationPubkey,
    );

    const { spendTx, controlBlockBytes, leafHash, hashLockScript } =
      createReceiverSpendTx({
        htlcTx: htlxTxFromHex,
        network: getNetwork(this.config.getNetwork()),
        hash: hashBytes,
        hashLockDestinationPubkey: hashLockDestinationPubkeyBytes,
        sequenceLockDestinationPubkey: sequenceLockDestinationPubkeyBytes,
        fee,
      });

    this.config.signer.signTransactionIndex(
      spendTx,
      0,
      await this.config.signer.getIdentityPublicKey(),
    );

    const sig = spendTx
      .getInput(0)
      .tapScriptSig!.find(([ref]) => equalBytes(ref.leafHash, leafHash))?.[1];

    spendTx.updateInput(0, {
      finalScriptWitness: [
        sig!,
        hexToBytes(preimage),
        hashLockScript,
        controlBlockBytes,
      ],
    });

    return spendTx.hex;
  }

  async queryHTLC({
    paymentHashes,
    status,
    transferIds,
    matchRole = PreimageRequestRole.PREIMAGE_REQUEST_ROLE_RECEIVER,
    limit = 100,
    offset = 0,
  }: {
    paymentHashes?: string[];
    status?: PreimageRequestStatus;
    transferIds?: string[];
    matchRole?: PreimageRequestRole;
    limit?: number;
    offset?: number;
  }): Promise<QueryHtlcResponse> {
    if (limit && (limit > 100 || limit < 1)) {
      throw new SparkValidationError(
        "Limit must be between 1 and 100 if provided.",
        {
          field: "limit",
          value: limit,
          expected: "between 1 and 100",
        },
      );
    }
    if (offset !== undefined && offset < 0) {
      throw new SparkValidationError(
        "Offset must be non-negative if provided",
        {
          field: "offset",
          value: offset,
          expected: "non-negative",
        },
      );
    }
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );
    let response: QueryHtlcResponse;
    const identityPublicKey = await this.config.signer.getIdentityPublicKey();
    try {
      response = await sparkClient.query_htlc({
        paymentHashes: paymentHashes?.map((hash) => hexToBytes(hash)) ?? [],
        status,
        transferIds: transferIds ?? [],
        matchRole,
        identityPublicKey,
        limit,
        offset,
      });
    } catch (error) {
      throw new SparkRequestError("Failed to query HTLC", {
        operation: "query_htlc",
        error,
      });
    }
    return response;
  }

  /**
   * Fulfills one or more Spark invoices.
   *
   * Processes each provided invoice and attempts to pay it according to the wallet's
   * funding/selection strategy.
   *
   * @param sparkInvoices - Invoices to fulfill.
   * @param sparkInvoices[].invoice - The parsed Spark address/invoice to pay.
   *   Must be a valid Spark address or invoice.
   *   Must use spark1... prefixed invoices.
   *   Deprecated sp1... prefixed invoices are not supported.
   * @param sparkInvoices[].amount - Used to define an amount for invoices without an amount encoded.
   *   For sats invoices, this is the amount in sats. For token invoices, this is the amount in tokens.
   *   Amount encoded in the invoice takes precedence if both are provided.
   *
   * @returns Promise<string> A payment or transaction identifier (implementation‑specific).
   *
   * @throws {SparkValidationError} If validation fails (malformed invoice, zero/negative amount, unsupported network),
   *
   * @example
   * await wallet.fulfillSparkInvoice([
   *   { invoice: invoiceWithNilAmount, amount: 1000n },
   *   { invoice: invoiceWithEncodedAmount }, // uses amount encoded in the invoice
   * ]);
   */
  public async fulfillSparkInvoice(
    sparkInvoices: {
      invoice: SparkAddressFormat;
      amount?: bigint;
    }[],
  ): Promise<FulfillSparkInvoiceResponse> {
    if (!Array.isArray(sparkInvoices) || sparkInvoices.length === 0) {
      throw new SparkValidationError("No Spark invoices provided", {
        field: "sparkInvoices",
        value: sparkInvoices,
        expected: "Non-empty array",
      });
    }
    const satsTransactionSuccess: {
      invoice: SparkAddressFormat;
      transferResponse: WalletTransfer;
    }[] = [];
    const satsTransactionErrors: {
      invoice: SparkAddressFormat;
      error: Error;
    }[] = [];
    const tokenTransactionSuccess: {
      tokenIdentifier: Bech32mTokenIdentifier;
      invoices: SparkAddressFormat[];
      txid: string;
    }[] = [];
    const tokenTransactionErrors: {
      tokenIdentifier: Bech32mTokenIdentifier;
      invoices: SparkAddressFormat[];
      error: Error;
    }[] = [];
    const { satsInvoices, tokenInvoices, invalidInvoices } =
      await this.groupSparkInvoicesByPaymentType(sparkInvoices);
    if (invalidInvoices.length > 0) {
      return {
        satsTransactionSuccess,
        satsTransactionErrors,
        tokenTransactionSuccess,
        tokenTransactionErrors,
        invalidInvoices,
      };
    }
    if (tokenInvoices.size > 0) {
      await this.syncTokenOutputs();
      const tokenTransferTasks: Promise<
        | {
            ok: true;
            tokenIdentifier: Bech32mTokenIdentifier;
            invoices: SparkAddressFormat[];
            txid: string;
          }
        | {
            ok: false;
            tokenIdentifier: Bech32mTokenIdentifier;
            invoices: SparkAddressFormat[];
            error: Error;
          }
      >[] = [];
      for (const [identifierHex, decodedInvoices] of tokenInvoices.entries()) {
        const tokenIdentifier = hexToBytes(identifierHex);
        const tokenIdB32 = encodeBech32mTokenIdentifier({
          tokenIdentifier,
          network: this.config.getNetworkType(),
        }) as Bech32mTokenIdentifier;
        const receiverOutputs = decodedInvoices.map((d) => ({
          tokenIdentifier: tokenIdB32,
          tokenAmount: d.amount!,
          receiverSparkAddress: d.invoice,
        }));
        const invoices = decodedInvoices.map(
          (d) => d.invoice as SparkAddressFormat,
        );
        const totalTokenAmount = receiverOutputs.reduce(
          (sum, o) => sum + o.tokenAmount,
          0n,
        );

        tokenTransferTasks.push(
          (async () => {
            let release = async () => {};
            try {
              const { outputs: acquiredOutputs, release: lockRelease } =
                await this.tokenOutputManager.acquireOutputs(
                  tokenIdB32,
                  (available) =>
                    this.tokenTransactionService.selectTokenOutputs(
                      available,
                      totalTokenAmount,
                      "SMALL_FIRST",
                    ),
                  `fulfill-invoice-${tokenIdB32}`,
                );
              release = lockRelease;

              const tokenOutputsMap: TokenOutputsMap = new Map([
                [tokenIdB32, acquiredOutputs],
              ]);
              const txid = await this.tokenTransactionService.tokenTransfer({
                tokenOutputs: tokenOutputsMap,
                receiverOutputs,
                selectedOutputs: acquiredOutputs,
              });

              await this.tokenOutputManager.lockOutputs(acquiredOutputs);

              return {
                ok: true as const,
                tokenIdentifier: tokenIdB32,
                invoices,
                txid,
              };
            } catch (e: any) {
              return {
                ok: false as const,
                tokenIdentifier: tokenIdB32,
                invoices,
                error: e instanceof Error ? e : new Error(String(e)),
              };
            } finally {
              await release();
            }
          })(),
        );
      }
      const results = await Promise.all(tokenTransferTasks);
      for (const r of results) {
        if (r.ok) {
          tokenTransactionSuccess.push({
            tokenIdentifier: r.tokenIdentifier,
            invoices: r.invoices,
            txid: r.txid,
          });
        } else {
          tokenTransactionErrors.push({
            tokenIdentifier: r.tokenIdentifier,
            invoices: r.invoices,
            error: r.error,
          });
        }
      }
    }
    if (satsInvoices.length > 0) {
      const transfers = await this.transferWithInvoice(satsInvoices);
      for (const transfer of transfers) {
        if (transfer.ok) {
          satsTransactionSuccess.push({
            invoice: transfer.param.sparkInvoice ?? ("" as SparkAddressFormat),
            transferResponse: transfer.transfer,
          });
        } else {
          satsTransactionErrors.push({
            invoice: transfer.param.sparkInvoice ?? ("" as SparkAddressFormat),
            error: transfer.error,
          });
        }
      }
    }
    return {
      satsTransactionSuccess,
      satsTransactionErrors,
      tokenTransactionSuccess,
      tokenTransactionErrors,
      invalidInvoices,
    };
  }

  private async groupSparkInvoicesByPaymentType(
    sparkInvoices: {
      invoice: SparkAddressFormat;
      amount?: bigint;
    }[],
  ): Promise<GroupSparkInvoicesResult> {
    const satsInvoices: TransferWithInvoiceParams[] = [];
    const tokenInvoices: Map<string, TokenInvoice[]> = new Map();
    const invalidInvoices: InvalidInvoice[] = [];

    const identityPublicKey = await this.getIdentityPublicKey();

    sparkInvoices.forEach((input) => {
      const { invoice, amount } = input;
      if (isLegacySparkAddress(invoice)) {
        invalidInvoices.push({
          invoice,
          error: new SparkValidationError("Deprecated spark invoice format", {
            field: "invoice",
            value: invoice,
            expected:
              "Spark invoice prefixed with spark... Deprecated sp... formats are not supported.",
          }),
        });
        return;
      }
      const addressData = decodeSparkAddress(
        invoice,
        this.config.getNetworkType(),
      );
      if (!addressData.sparkInvoiceFields) {
        invalidInvoices.push({
          invoice,
          error: new SparkValidationError("Missing invoice fields", {
            field: "invoice",
            value: invoice,
            expected: "Valid invoice fields",
          }),
        });
        return;
      }

      const fields = addressData.sparkInvoiceFields;

      if (fields.expiryTime) {
        if (fields.expiryTime.getTime() <= Date.now()) {
          invalidInvoices.push({
            invoice,
            error: new SparkValidationError("Invoice expired", {
              field: "invoice",
              value: fields.expiryTime.getTime(),
              expected: "Expiry time in the future",
            }),
          });
          return;
        }
      }
      if (
        fields.senderPublicKey &&
        fields.senderPublicKey !== identityPublicKey
      ) {
        invalidInvoices.push({
          invoice,
          error: new SparkValidationError("Sender public key mismatch", {
            field: "invoice",
            value: fields.senderPublicKey,
            expected: identityPublicKey,
          }),
        });
        return;
      }

      if (fields.paymentType?.type === "sats") {
        const encodedAmount = fields.paymentType.amount;
        if (amount && !isSafeForNumber(amount)) {
          invalidInvoices.push({
            invoice,
            error: new SparkValidationError("Invalid amount", {
              field: "invoice",
              value: amount,
              expected: "Safe for number",
            }),
          });
          return;
        }
        if (!encodedAmount && !amount) {
          invalidInvoices.push({
            invoice,
            error: new SparkValidationError(
              "No amount passed for nil amount invoice",
              {
                field: "invoice",
                expected:
                  "Amount to fulfill passed to function for nil amount invoice",
              },
            ),
          });
          return;
        }
        satsInvoices.push({
          amountSats: encodedAmount ?? Number(amount!),
          receiverIdentityPubkey: hexToBytes(addressData.identityPublicKey),
          sparkInvoice: invoice as SparkAddressFormat,
        });
      } else if (fields.paymentType?.type === "tokens") {
        const tokenIdentifierHex = fields.paymentType.tokenIdentifier;
        const encodedAmount = fields.paymentType.amount;
        if (!tokenIdentifierHex) {
          invalidInvoices.push({
            invoice,
            error: new SparkValidationError(
              "No token identifier passed for tokens invoice",
              {
                field: "invoice",
                value: invoice,
                expected: "Token identifier passed",
              },
            ),
          });
          return;
        }
        if (!encodedAmount && !amount) {
          invalidInvoices.push({
            invoice,
            error: new SparkValidationError(
              "No amount passed for nil amount invoice",
              {
                field: "invoice",
                expected:
                  "Amount to fulfill passed to function for nil amount invoice",
              },
            ),
          });
          return;
        }
        if (!tokenInvoices.has(tokenIdentifierHex)) {
          tokenInvoices.set(tokenIdentifierHex, [
            {
              invoice,
              identifierHex: tokenIdentifierHex,
              amount: encodedAmount ?? amount!,
            },
          ]);
        } else {
          tokenInvoices.get(tokenIdentifierHex)!.push({
            invoice,
            identifierHex: tokenIdentifierHex,
            amount: encodedAmount ?? amount!,
          });
        }
      } else {
        invalidInvoices.push({
          invoice,
          error: new SparkValidationError("Invalid payment type", {
            field: "invoice",
            expected: "sats or tokens invoice",
          }),
        });
      }
    });
    return { satsInvoices, tokenInvoices, invalidInvoices };
  }

  public async querySparkInvoices(
    invoices: string[],
  ): Promise<QuerySparkInvoicesResponse> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );
    return await sparkClient.query_spark_invoices({
      invoice: invoices,
    });
  }

  /**
   * Gets fee estimate for sending Lightning payments.
   *
   * @param {LightningSendFeeEstimateInput} params - Input parameters for fee estimation
   * @returns {Promise<number>} Fee estimate for sending Lightning payments
   */
  public async getLightningSendFeeEstimate({
    encodedInvoice,
    amountSats,
  }: LightningSendFeeEstimateInput): Promise<number> {
    const sspClient = this.getSspClient();

    const feeEstimate = await sspClient.getLightningSendFeeEstimate(
      encodedInvoice,
      amountSats,
    );

    if (
      !feeEstimate ||
      feeEstimate.feeEstimate.originalUnit !== CurrencyUnit.Millisatoshi
    ) {
      throw new Error("Failed to get lightning send fee estimate");
    }

    const satsFeeEstimate = feeEstimate.feeEstimate.originalValue / 1000;
    return Math.ceil(satsFeeEstimate);
  }

  // ***** Cooperative Exit Flow *****

  /**
   * Initiates a withdrawal to move funds from the Spark network to an on-chain Bitcoin address.
   *
   * @param {Object} params - Parameters for the withdrawal
   * @param {string} params.onchainAddress - The Bitcoin address where the funds should be sent
   * @param {CoopExitFeeQuote} params.feeQuote - The fee quote for the withdrawal
   * @param {ExitSpeed} params.exitSpeed - The exit speed chosen for the withdrawal
   * @param {number} [params.amountSats] - The amount in satoshis to withdraw. If not specified, attempts to withdraw all available funds and deductFeeFromWithdrawalAmount is set to true.
   * @param {boolean} [params.deductFeeFromWithdrawalAmount] - Controls how the withdrawal fee is handled. If true, the fee is deducted from the withdrawal amount (amountSats), meaning the recipient will receive amountSats minus the fee. If false, the fee is paid separately from the wallet balance, meaning the recipient will receive the full amountSats.
   * @returns {Promise<CoopExitRequest | null | undefined>} The withdrawal request details, or null/undefined if the request cannot be completed
   */
  public async withdraw({
    onchainAddress,
    exitSpeed,
    feeQuote,
    amountSats,
    feeAmountSats,
    feeQuoteId,
    deductFeeFromWithdrawalAmount = true,
  }: WithdrawParams) {
    if (!Number.isSafeInteger(amountSats)) {
      throw new SparkValidationError("Sats amount must be less than 2^53", {
        field: "amountSats",
        value: amountSats,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    if (feeQuote) {
      switch (exitSpeed) {
        case ExitSpeed.FAST:
          feeAmountSats =
            (feeQuote.l1BroadcastFeeFast?.originalValue || 0) +
            (feeQuote.userFeeFast?.originalValue || 0);
          break;
        case ExitSpeed.MEDIUM:
          feeAmountSats =
            (feeQuote.l1BroadcastFeeMedium?.originalValue || 0) +
            (feeQuote.userFeeMedium?.originalValue || 0);
          break;
        case ExitSpeed.SLOW:
          feeAmountSats =
            (feeQuote.l1BroadcastFeeSlow?.originalValue || 0) +
            (feeQuote.userFeeSlow?.originalValue || 0);
          break;
        default:
          throw new SparkValidationError("Invalid exit speed", {
            field: "exitSpeed",
            value: exitSpeed,
            expected: "FAST, MEDIUM, or SLOW",
          });
      }

      feeQuoteId = feeQuote.id;
    }
    if (!feeAmountSats) {
      throw new SparkValidationError("No fee quote or fee amount provided", {
        field: "feeQuote",
        value: feeQuote,
      });
    }

    if (!feeQuoteId) {
      throw new SparkValidationError("No fee quote ID provided", {
        field: "feeQuoteId",
        value: feeQuoteId,
      });
    }

    return await this.withLeaves(async () => {
      return await this.coopExit(
        onchainAddress,
        feeAmountSats,
        feeQuoteId,
        exitSpeed,
        deductFeeFromWithdrawalAmount,
        amountSats,
      );
    });
  }

  /**
   * Internal method to perform a cooperative exit (withdrawal).
   *
   * @param {string} onchainAddress - The Bitcoin address where the funds should be sent
   * @param {number} [targetAmountSats] - The amount in satoshis to withdraw
   * @returns {Promise<Object | null | undefined>} The exit request details
   * @private
   */
  private async coopExit(
    onchainAddress: string,
    feeAmountSats: number,
    feeQuoteId: string,
    exitSpeed: ExitSpeed,
    deductFeeFromWithdrawalAmount: boolean,
    targetAmountSats?: number,
  ) {
    if (!Number.isSafeInteger(targetAmountSats)) {
      throw new SparkValidationError("Sats amount must be less than 2^53", {
        field: "targetAmountSats",
        value: targetAmountSats,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    if (!targetAmountSats) {
      deductFeeFromWithdrawalAmount = true;
    }

    let leavesToSendToSsp: TreeNode[] = [];
    let leavesToSendToSE: TreeNode[] = [];

    if (deductFeeFromWithdrawalAmount) {
      leavesToSendToSsp = targetAmountSats
        ? this.popOrThrow(
            (await this.selectLeaves([targetAmountSats])).get(
              targetAmountSats,
            )!,
            `no leaves for ${targetAmountSats}`,
          )
        : this.leaves;

      if (
        feeAmountSats >
        leavesToSendToSsp.reduce((acc, leaf) => acc + leaf.value, 0)
      ) {
        throw new SparkValidationError(
          "The fee for the withdrawal is greater than the target withdrawal amount",
          {
            field: "fee",
            value: feeAmountSats,
            expected: "less than or equal to the target amount",
          },
        );
      }
    } else {
      if (!targetAmountSats) {
        throw new SparkValidationError(
          "targetAmountSats is required when deductFeeFromWithdrawalAmount is false",
          {
            field: "targetAmountSats",
            value: targetAmountSats,
            expected: "defined when deductFeeFromWithdrawalAmount is false",
          },
        );
      }

      const leaves = await this.selectLeaves([targetAmountSats, feeAmountSats]);

      const leavesForTargetAmount = this.popOrThrow(
        leaves.get(targetAmountSats)!,
        `failed to get leaves leaves for targetAmount, val: ${targetAmountSats}`,
      );
      const leavesForFee = this.popOrThrow(
        leaves.get(feeAmountSats)!,
        `failed to get leaves leaves for fee, val: ${feeAmountSats}`,
      );

      if (!leavesForTargetAmount || !leavesForFee) {
        throw new Error("Failed to select leaves for target amount and fee");
      }

      leavesToSendToSsp = leavesForTargetAmount;
      leavesToSendToSE = leavesForFee;
    }

    leavesToSendToSsp = await this.checkRenewLeaves(leavesToSendToSsp);
    leavesToSendToSE = await this.checkRenewLeaves(leavesToSendToSE);

    const leafKeyTweaks: LeafKeyTweak[] = await Promise.all(
      [...leavesToSendToSE, ...leavesToSendToSsp].map(async (leaf) => ({
        leaf,
        keyDerivation: {
          type: KeyDerivationType.LEAF,
          path: leaf.id,
        },
        newKeyDerivation: {
          type: KeyDerivationType.RANDOM,
        },
      })),
    );

    const transferId = uuidv7();

    const requestCoopExitParams: RequestCoopExitInput = {
      leafExternalIds: leavesToSendToSsp.map((leaf) => leaf.id),
      withdrawalAddress: onchainAddress,
      exitSpeed,
      withdrawAll: deductFeeFromWithdrawalAmount,
      userOutboundTransferExternalId: transferId,
    };

    if (!deductFeeFromWithdrawalAmount) {
      requestCoopExitParams.feeQuoteId = feeQuoteId;
      requestCoopExitParams.feeLeafExternalIds = leavesToSendToSE.map(
        (leaf) => leaf.id,
      );
    }

    const sspClient = this.getSspClient();

    const coopExitRequest = await sspClient.requestCoopExit(
      requestCoopExitParams,
    );

    if (!coopExitRequest?.rawConnectorTransaction) {
      throw new Error("Failed to request coop exit");
    }

    const connectorTx = getTxFromRawTxHex(
      coopExitRequest.rawConnectorTransaction,
    );

    // SSP stores coop_exit_txid in little-endian format and returns it as hex string
    // Converting hex to bytes gives us the correct little-endian format that SO expects
    const coopExitTxId = hexToBytes(coopExitRequest.coopExitTxid);
    const connectorTxId = getTxId(connectorTx);

    const connectorOutputs: TransactionInput[] = [];
    for (let i = 0; i < connectorTx.outputsLength - 1; i++) {
      connectorOutputs.push({
        txid: hexToBytes(connectorTxId),
        index: i,
      });
    }

    const sspPubIdentityKey = hexToBytes(this.config.getSspIdentityPublicKey());
    const connectorTxBytes = hexToBytes(
      coopExitRequest.rawConnectorTransaction,
    );
    const transfer = await this.coopExitService.getConnectorRefundSignatures({
      leaves: leafKeyTweaks,
      exitTxId: coopExitTxId,
      connectorOutputs,
      receiverPubKey: sspPubIdentityKey,
      transferId,
      connectorTx: connectorTxBytes,
    });

    const completeResponse = await sspClient.completeCoopExit({
      userOutboundTransferExternalId: transfer.transfer.id,
    });

    return completeResponse;
  }

  /**
   * Gets fee estimate for cooperative exit (on-chain withdrawal).
   *
   * @param {Object} params - Input parameters for fee estimation
   * @param {number} params.amountSats - The amount in satoshis to withdraw
   * @param {string} params.withdrawalAddress - The Bitcoin address where the funds should be sent
   * @returns {Promise<CoopExitFeeQuote | null>} Fee estimate for the withdrawal
   */
  public async getWithdrawalFeeQuote({
    amountSats,
    withdrawalAddress,
  }: {
    amountSats: number;
    withdrawalAddress: string;
  }): Promise<CoopExitFeeQuote | null> {
    const sspClient = this.getSspClient();

    if (!Number.isSafeInteger(amountSats)) {
      throw new SparkValidationError("Sats amount must be less than 2^53", {
        field: "amountSats",
        value: amountSats,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    let leaves = this.popOrThrow(
      (await this.selectLeaves([amountSats])).get(amountSats)!,
      `no leaves for ${amountSats}`,
    );

    leaves = await this.checkRenewLeaves(leaves);

    const feeEstimate = await sspClient.getCoopExitFeeQuote({
      leafExternalIds: leaves.map((leaf) => leaf.id),
      withdrawalAddress,
    });

    return feeEstimate;
  }

  /**
   * Gets a transfer that has been sent by the SSP to the wallet.
   *
   * @param {string} id - The ID of the transfer
   * @returns {Promise<TransferWithUserRequest | undefined>} The transfer
   */
  public async getTransferFromSsp(
    id: string,
  ): Promise<TransferWithUserRequest | undefined> {
    const sspClient = this.getSspClient();
    const transfers = await sspClient.getTransfers([id]);
    return transfers?.[0];
  }

  private async constructTransfersWithUserRequest(
    transfers: Transfer[],
  ): Promise<WalletTransfer[]> {
    const identityPublicKey = bytesToHex(
      await this.config.signer.getIdentityPublicKey(),
    );

    const userRequests = await this.sspClient?.getTransfers(
      transfers
        .filter((transfer) =>
          [
            TransferType.COOPERATIVE_EXIT,
            TransferType.COUNTER_SWAP_V3,
            TransferType.COUNTER_SWAP,
            TransferType.PREIMAGE_SWAP,
            TransferType.PRIMARY_SWAP_V3,
            TransferType.SWAP,
            TransferType.UTXO_SWAP,
          ].includes(transfer.type),
        )
        .map((transfer) => transfer.id),
    );

    const userRequestsMap = new Map<
      string,
      Omit<UserRequestType, "transfer">
    >();
    for (const userRequest of userRequests || []) {
      if (userRequest && userRequest.sparkId && userRequest.userRequest) {
        userRequestsMap.set(userRequest.sparkId, userRequest.userRequest);
      }
    }

    return transfers.map((transfer) =>
      mapTransferToWalletTransfer(
        transfer,
        identityPublicKey,
        userRequestsMap.get(transfer.id),
      ),
    );
  }

  /**
   * Gets a transfer, that the wallet is a participant of, in the Spark network.
   * Only contains data about the spark->spark transfer, use getTransferFromSsp if you're
   * looking for information related to a lightning transfer.
   *
   * @param {string} id - The ID of the transfer
   * @returns {Promise<Transfer | undefined>} The transfer
   */
  public async getTransfer(id: string): Promise<WalletTransfer | undefined> {
    const transfer = await this.transferService.queryTransfer(id);
    if (!transfer) {
      return undefined;
    }

    return (await this.constructTransfersWithUserRequest([transfer]))[0];
  }

  /**
   * Gets all transfers for the wallet, optionally filtered by creation time.
   *
   * @param {number} [limit=20] - Maximum number of transfers to return
   * @param {number} [offset=0] - Offset for pagination
   * @param {Date} [createdAfter] - Optional: Return transfers created strictly after this time (exclusive). Mutually exclusive with createdBefore.
   * @param {Date} [createdBefore] - Optional: Return transfers created strictly before this time (exclusive). Mutually exclusive with createdAfter.
   * @returns {Promise<{transfers: WalletTransfer[], offset: number}>} Object containing array of wallet transfers and next offset
   */
  public async getTransfers(
    limit: number = 20,
    offset: number = 0,
    createdAfter?: Date,
    createdBefore?: Date,
  ): Promise<{
    transfers: WalletTransfer[];
    offset: number;
  }> {
    // Validate that only one time filter is provided (mutually exclusive)
    if (createdAfter && createdBefore) {
      throw new Error(
        "createdAfter and createdBefore are mutually exclusive - only one can be specified",
      );
    }

    const transfers = await this.transferService.queryAllTransfers(
      limit,
      offset,
      createdAfter,
      createdBefore,
    );

    return {
      transfers: await this.constructTransfersWithUserRequest(
        transfers.transfers,
      ),
      offset: transfers.offset,
    };
  }

  // ***** Token Flow *****

  /**
   * Synchronizes token outputs for the wallet.
   *
   * @param {Bech32mTokenIdentifier[]} [tokenIdentifiers] - Optional list of token identifiers to sync.
   *   If provided, only syncs outputs for the specified tokens (preserving other cached tokens).
   *   If not provided, syncs all token outputs.
   * @returns {Promise<void>}
   * @private
   */
  protected async syncTokenOutputs(
    tokenIdentifiers?: Bech32mTokenIdentifier[],
  ) {
    const filterByIdentifiers =
      Array.isArray(tokenIdentifiers) && tokenIdentifiers.length > 0;

    const rawTokenIdentifiers = filterByIdentifiers
      ? tokenIdentifiers.map(
          (id) =>
            decodeBech32mTokenIdentifier(id, this.config.getNetworkType())
              .tokenIdentifier,
        )
      : undefined;

    const unsortedTokenOutputs =
      await this.tokenTransactionService.fetchOwnedTokenOutputs({
        ownerPublicKeys: [await this.config.signer.getIdentityPublicKey()],
        tokenIdentifiers: rawTokenIdentifiers,
      });

    // Validate and group all outputs by token identifier
    const groupedOutputs: TokenOutputsMap = new Map();

    for (const output of unsortedTokenOutputs) {
      if (!output.output?.tokenIdentifier || !output.output.id) {
        throw new SparkValidationError(
          "Server returned incomplete token output",
          {
            field: "output",
            value: output,
            expected:
              "output.output.tokenIdentifier and output.output.id to be defined",
          },
        );
      }

      const bech32mTokenIdentifier = encodeBech32mTokenIdentifier({
        tokenIdentifier: output.output.tokenIdentifier,
        network: this.config.getNetworkType(),
      });

      if (!groupedOutputs.has(bech32mTokenIdentifier)) {
        groupedOutputs.set(bech32mTokenIdentifier, []);
      }
      groupedOutputs.get(bech32mTokenIdentifier)!.push(output);
    }

    await this.tokenOutputManager.setOutputs(
      groupedOutputs,
      filterByIdentifiers ? tokenIdentifiers : undefined,
    );
  }

  /**
   * Transfers tokens to another user.
   *
   * @param {Object} params - Parameters for the token transfer
   * @param {string} params.tokenPublicKey - The public key of the token to transfer
   * @param {bigint} params.tokenAmount - The amount of tokens to transfer
   * @param {string} params.receiverSparkAddress - The recipient's public key
   * @param {OutputWithPreviousTransactionData[]} [params.selectedOutputs] - Optional specific leaves to use for the transfer
   * @returns {Promise<string>} The transaction ID of the token transfer
   */
  public async transferTokens({
    tokenIdentifier,
    tokenAmount,
    receiverSparkAddress,
    outputSelectionStrategy,
    selectedOutputs,
  }: {
    tokenIdentifier: Bech32mTokenIdentifier;
    tokenAmount: bigint;
    receiverSparkAddress: string;
    outputSelectionStrategy?: "SMALL_FIRST" | "LARGE_FIRST";
    selectedOutputs?: OutputWithPreviousTransactionData[];
  }): Promise<string> {
    const addressData = decodeSparkAddress(
      receiverSparkAddress,
      this.config.getNetworkType(),
    );

    if (addressData.sparkInvoiceFields) {
      throw new SparkValidationError(
        "Spark address is a Spark invoice. Use fulfillSparkInvoice instead.",
        {
          field: "receiverSparkAddress",
          value: receiverSparkAddress,
        },
      );
    }

    await this.syncTokenOutputs([tokenIdentifier]);

    const strategy = outputSelectionStrategy ?? "SMALL_FIRST";
    const { outputs: acquiredOutputs, release } =
      await this.tokenOutputManager.acquireOutputs(
        tokenIdentifier,
        (available) => {
          if (selectedOutputs) {
            return selectedOutputs.filter((so) =>
              available.some((a) => a.output?.id === so.output?.id),
            );
          }
          return this.tokenTransactionService.selectTokenOutputs(
            available,
            tokenAmount,
            strategy,
          );
        },
        `transfer-${tokenIdentifier}`,
      );

    try {
      const tokenOutputsMap: TokenOutputsMap = new Map([
        [tokenIdentifier, acquiredOutputs],
      ]);
      const txHash = await this.tokenTransactionService.tokenTransfer({
        tokenOutputs: tokenOutputsMap,
        receiverOutputs: [
          {
            tokenIdentifier,
            tokenAmount,
            receiverSparkAddress,
          },
        ],
        outputSelectionStrategy: strategy,
        selectedOutputs: acquiredOutputs,
      });

      await this.tokenOutputManager.lockOutputs(acquiredOutputs);
      return txHash;
    } finally {
      await release();
    }
  }

  /**
   * Transfers tokens with multiple outputs
   *
   * @param {Array} receiverOutputs - Array of transfer parameters
   * @param {string} receiverOutputs[].tokenPublicKey - The public key of the token to transfer
   * @param {bigint} receiverOutputs[].tokenAmount - The amount of tokens to transfer
   * @param {string} receiverOutputs[].receiverSparkAddress - The recipient's public key
   * @param {OutputWithPreviousTransactionData[]} [selectedOutputs] - Optional specific leaves to use for the transfer
   * @returns {Promise<string[]>} Array of transaction IDs for the token transfers
   */
  public async batchTransferTokens(
    receiverOutputs: {
      tokenIdentifier: Bech32mTokenIdentifier;
      tokenAmount: bigint;
      receiverSparkAddress: string;
    }[],
    outputSelectionStrategy: "SMALL_FIRST" | "LARGE_FIRST" = "SMALL_FIRST",
    selectedOutputs?: OutputWithPreviousTransactionData[],
  ): Promise<string> {
    if (receiverOutputs.length === 0) {
      throw new SparkValidationError(
        "At least one receiver output is required",
        {
          field: "receiverOutputs",
          value: receiverOutputs,
          expected: "Non-empty array",
        },
      );
    }
    for (const output of receiverOutputs) {
      if (output.tokenAmount <= 0n) {
        throw new SparkValidationError("Token amount must be greater than 0", {
          field: "receiverOutputs",
          value: receiverOutputs,
          expected: "All outputs must have tokenAmount > 0",
        });
      }
    }

    // Group receiver outputs by token identifier
    const amountsByToken = new Map<Bech32mTokenIdentifier, bigint>();
    for (const output of receiverOutputs) {
      const current = amountsByToken.get(output.tokenIdentifier) ?? 0n;
      amountsByToken.set(output.tokenIdentifier, current + output.tokenAmount);
    }

    const tokenIdentifiers = [...amountsByToken.keys()];

    await this.syncTokenOutputs(tokenIdentifiers);

    // Acquire output locks for each token identifier
    const acquiredByToken = new Map<
      Bech32mTokenIdentifier,
      {
        outputs: OutputWithPreviousTransactionData[];
        release: () => Promise<void>;
      }
    >();

    try {
      for (const tokenId of tokenIdentifiers) {
        const totalForToken = amountsByToken.get(tokenId)!;
        const { outputs, release } =
          await this.tokenOutputManager.acquireOutputs(
            tokenId,
            (available) => {
              if (selectedOutputs) {
                return selectedOutputs.filter((so) =>
                  available.some((a) => a.output?.id === so.output?.id),
                );
              }
              return this.tokenTransactionService.selectTokenOutputs(
                available,
                totalForToken,
                outputSelectionStrategy,
              );
            },
            `batch-transfer-${tokenId}`,
          );
        acquiredByToken.set(tokenId, { outputs, release });
      }

      const tokenOutputsMap: TokenOutputsMap = new Map();
      const allAcquiredOutputs: OutputWithPreviousTransactionData[] = [];
      for (const [tokenId, { outputs }] of acquiredByToken) {
        tokenOutputsMap.set(tokenId, outputs);
        allAcquiredOutputs.push(...outputs);
      }

      const txHash = await this.tokenTransactionService.tokenTransfer({
        tokenOutputs: tokenOutputsMap,
        receiverOutputs,
        outputSelectionStrategy,
        selectedOutputs: allAcquiredOutputs,
      });

      await this.tokenOutputManager.lockOutputs(allAcquiredOutputs);
      return txHash;
    } finally {
      for (const { release } of acquiredByToken.values()) {
        await release();
      }
    }
  }

  /**
   * @deprecated Use queryTokenTransactionsWithFilters or queryTokenTransactionsByTxHashes instead
   * Retrieves token transaction history for specified tokens
   * Can optionally filter by specific transaction hashes.
   *
   * @param sparkAddresses - Optional array of Spark addresses to query transactions for
   * @param ownerPublicKeys - Optional array of owner public keys to query transactions for (deprecated, use sparkAddresses)
   * @param issuerPublicKeys - Optional array of issuer public keys to query transactions for
   * @param tokenTransactionHashes - Optional array of specific transaction hashes to filter by
   * @param tokenIdentifiers - Optional array of token identifiers to filter by
   * @param outputIds - Optional array of output IDs to filter by
   * @param order - Optional order for results ("ASCENDING" or "DESCENDING", defaults to "DESCENDING")
   * @param pageSize - Optional page size (defaults to 50)
   * @param offset - Optional offset for pagination (defaults to 0)
   * @returns Promise resolving to array of token transactions with their current status
   */

  public async queryTokenTransactions({
    sparkAddresses,
    ownerPublicKeys,
    issuerPublicKeys,
    tokenTransactionHashes,
    tokenIdentifiers,
    outputIds,
    order,
    pageSize,
    offset,
  }: {
    sparkAddresses?: string[];
    /**
     * @deprecated Use sparkAddresses instead
     */
    ownerPublicKeys?: string[];
    issuerPublicKeys?: string[];
    tokenTransactionHashes?: string[];
    tokenIdentifiers?: string[];
    outputIds?: string[];
    order?: "asc" | "desc";
    pageSize?: number;
    offset?: number;
  }): Promise<QueryTokenTransactionsResponse> {
    return this.tokenTransactionService.queryTokenTransactions({
      sparkAddresses,
      ownerPublicKeys,
      issuerPublicKeys,
      tokenTransactionHashes,
      tokenIdentifiers,
      outputIds,
      order,
      pageSize: pageSize ?? 50,
      offset: offset ?? 0,
    });
  }

  public async getTokenL1Address(): Promise<string> {
    return getP2WPKHAddressFromPublicKey(
      await this.config.signer.getIdentityPublicKey(),
      this.config.getNetwork(),
    );
  }

  /**
   * Retrieves specific token transactions by their transaction hashes
   * Primarily meant for retrieving and/or confirming the status of specific token transactions.
   *
   * @param tokenTransactionHashes - Array of transaction hashes
   * @returns Promise resolving to array of token transactions with their current status
   */
  public async queryTokenTransactionsByTxHashes(
    tokenTransactionHashes: string[],
  ): Promise<QueryTokenTransactionsResponse> {
    return this.tokenTransactionService.queryTokenTransactionsByTxHashes(
      tokenTransactionHashes,
    );
  }

  /**
   * Retrieves token transaction history with optional filters
   *
   * @param sparkAddresses - Optional array of Spark addresses to query transactions for
   * @param issuerPublicKeys - Optional array of issuer public keys to query transactions for
   * @param tokenIdentifiers - Optional array of token identifiers to filter by
   * @param outputIds - Optional array of output IDs to filter by
   * @param pageSize - Optional page size (defaults to 50)
   * @param cursor - Optional cursor for pagination
   * @param direction - Optional direction for pagination ("NEXT" or "PREVIOUS", defaults to "NEXT")
   * @returns Promise resolving to array of token transactions with their current status
   */
  public async queryTokenTransactionsWithFilters({
    sparkAddresses,
    issuerPublicKeys,
    tokenIdentifiers,
    outputIds,
    pageSize,
    cursor,
    direction,
  }: {
    sparkAddresses?: string[];
    issuerPublicKeys?: string[];
    tokenIdentifiers?: string[];
    outputIds?: string[];
    pageSize?: number;
    cursor?: string;
    direction?: "NEXT" | "PREVIOUS";
  }): Promise<QueryTokenTransactionsResponse> {
    return this.tokenTransactionService.queryTokenTransactionsWithFilters({
      sparkAddresses,
      issuerPublicKeys,
      tokenIdentifiers,
      outputIds,
      pageSize,
      cursor,
      direction,
    });
  }

  // For internal use only
  async getTokenOutputStats(
    tokenIdentifier: Bech32mTokenIdentifier,
  ): Promise<{ outputCount: number; totalAmount: bigint }> {
    const availableOutputs =
      await this.tokenOutputManager.getAvailableOutputs(tokenIdentifier);
    return {
      outputCount: availableOutputs.length,
      totalAmount: sumTokenOutputs(availableOutputs),
    };
  }

  /**
   * Signs a message with the identity key.
   *
   * @param {string} message - The message to sign
   * @param {boolean} [compact] - Whether to use compact encoding. If false, the message will be encoded as DER.
   * @returns {Promise<string>} The signed message
   */
  public async signMessageWithIdentityKey(
    message: string,
    compact?: boolean,
  ): Promise<string> {
    const hash = sha256(message);
    const signature = await this.config.signer.signMessageWithIdentityKey(
      hash,
      compact,
    );
    return bytesToHex(signature);
  }

  /**
   * Validates a message with the identity key.
   *
   * @param {string} message - The original message that was signed
   * @param {string | Uint8Array} signature - Signature to validate
   * @returns {Promise<boolean>} Whether the message is valid
   */
  public async validateMessageWithIdentityKey(
    message: string,
    signature: string | Uint8Array,
  ): Promise<boolean> {
    const hash = sha256(message);
    if (typeof signature === "string") {
      signature = hexToBytes(signature);
    }
    return this.config.signer.validateMessageWithIdentityKey(hash, signature);
  }

  /**
   * Signs a transaction with wallet keys.
   *
   * @param {string} txHex - The transaction hex to sign
   * @param {string} keyType - The type of key to use for signing ("identity", "deposit", or "auto-detect")
   * @returns {Promise<string>} The signed transaction hex
   */
  public async signTransaction(
    txHex: string,
    keyType: string = "auto-detect",
  ): Promise<string> {
    try {
      // Parse the transaction
      const tx = Transaction.fromRaw(hexToBytes(txHex));

      let publicKey: Uint8Array;

      switch (keyType.toLowerCase()) {
        case "identity":
          publicKey = await this.config.signer.getIdentityPublicKey();
          break;
        case "deposit":
          publicKey = await this.config.signer.getDepositSigningKey();
          break;
        case "auto-detect":
        default:
          // Try to auto-detect which key to use by examining the transaction inputs
          const detectedKey = await this.detectKeyForTransaction(tx);
          if (detectedKey) {
            publicKey = detectedKey.publicKey;
          } else {
            // Fallback to identity key
            publicKey = await this.config.signer.getIdentityPublicKey();
          }
          break;
      }

      // Check each input to determine which ones need signing
      let inputsSigned = 0;
      for (let i = 0; i < tx.inputsLength; i++) {
        const input = tx.getInput(i);
        if (!input?.witnessUtxo?.script) {
          continue;
        }

        const script = input.witnessUtxo.script;

        // Check if this is an ephemeral anchor (OP_TRUE script)
        // OP_TRUE is represented as a single byte: 0x51
        if (script.length === 1 && script[0] === 0x51) {
          continue;
        }

        // Check if this script matches one of our keys
        const identityScript = getP2TRScriptFromPublicKey(
          publicKey,
          this.config.getNetwork(),
        );

        if (bytesToHex(script) === bytesToHex(identityScript)) {
          // Sign this specific input
          try {
            this.config.signer.signTransactionIndex(tx, i, publicKey);
            inputsSigned++;
          } catch (error) {
            throw new SparkValidationError(
              `Failed to sign input ${i}: ${error}`,
              {
                field: "input",
                value: i,
              },
            );
          }
        }
      }

      if (inputsSigned === 0) {
        throw new Error(
          "No inputs were signed. Check that the transaction contains inputs controlled by this wallet.",
        );
      }

      tx.finalize();

      const signedTxHex = tx.hex;

      return signedTxHex;
    } catch (error) {
      console.error("❌ Error signing transaction:", error);
      throw error;
    }
  }

  /**
   * Helper method to auto-detect which key should be used for signing a transaction.
   */
  private async detectKeyForTransaction(tx: Transaction): Promise<{
    publicKey: Uint8Array;
    keyType: string;
  } | null> {
    try {
      // Get available keys
      const identityPubKey = await this.config.signer.getIdentityPublicKey();
      const depositPubKey = await this.config.signer.getDepositSigningKey();

      // Check if any inputs reference outputs that would be controlled by our keys
      for (let i = 0; i < tx.inputsLength; i++) {
        const input = tx.getInput(i);
        if (input?.witnessUtxo?.script) {
          const script = input.witnessUtxo.script;

          // Check if this script corresponds to one of our keys
          // This is a simplified check - in practice, you might need more sophisticated script analysis
          const identityScript = getP2TRScriptFromPublicKey(
            identityPubKey,
            this.config.getNetwork(),
          );
          const depositScript = getP2TRScriptFromPublicKey(
            depositPubKey,
            this.config.getNetwork(),
          );

          if (bytesToHex(script) === bytesToHex(identityScript)) {
            return {
              publicKey: identityPubKey,
              keyType: "identity",
            };
          }

          if (bytesToHex(script) === bytesToHex(depositScript)) {
            return {
              publicKey: depositPubKey,
              keyType: "deposit",
            };
          }
        }
      }

      return null;
    } catch (error) {
      console.warn("Error during key auto-detection:", error);
      return null;
    }
  }

  /**
   * Get a Lightning receive request by ID.
   *
   * @param {string} id - The ID of the Lightning receive request
   * @returns {Promise<LightningReceiveRequest | null>} The Lightning receive request
   */
  public async getLightningReceiveRequest(
    id: string,
  ): Promise<LightningReceiveRequest | null> {
    const sspClient = this.getSspClient();
    return await sspClient.getLightningReceiveRequest(id);
  }

  /**
   * Get a Lightning send request by ID.
   *
   * @param {string} id - The ID of the Lightning send request
   * @returns {Promise<LightningSendRequest | null>} The Lightning send request
   */
  public async getLightningSendRequest(
    id: string,
  ): Promise<LightningSendRequest | null> {
    const sspClient = this.getSspClient();
    return await sspClient.getLightningSendRequest(id);
  }

  /**
   * Get a coop exit request by ID.
   *
   * @param {string} id - The ID of the coop exit request
   * @returns {Promise<CoopExitRequest | null>} The coop exit request
   */
  public async getCoopExitRequest(id: string): Promise<CoopExitRequest | null> {
    const sspClient = this.getSspClient();
    return await sspClient.getCoopExitRequest(id);
  }

  /**
   * Check the remaining timelock on a given node.
   *
   * @param {string} nodeId - The ID of the node to check
   * @returns {Promise<{nodeTimelock: number, refundTimelock: number}>} The remaining timelocks in blocks for both node and refund transactions
   */
  public async checkTimelock(nodeId: string): Promise<{
    nodeTimelock: number;
    refundTimelock: number;
  }> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    try {
      const response = await sparkClient.query_nodes({
        source: {
          $case: "nodeIds",
          nodeIds: {
            nodeIds: [nodeId],
          },
        },
        includeParents: false,
        network: NetworkToProto[this.config.getNetwork()],
      });

      const node = response.nodes[nodeId];
      if (!node) {
        throw new SparkValidationError("Node not found", {
          field: "nodeId",
          value: nodeId,
        });
      }

      // Check if this is a root node (no parent)
      const isRootNode = !node.parentNodeId;

      // Validate transaction data exists
      if (!node.nodeTx || node.nodeTx.length === 0) {
        throw new SparkValidationError(
          `Node transaction data is missing or empty for ${isRootNode ? "root" : "non-root"} node`,
          {
            field: "nodeTx",
            value: node.nodeTx?.length || 0,
          },
        );
      }

      if (!node.refundTx || node.refundTx.length === 0) {
        throw new SparkValidationError(
          `Refund transaction data is missing or empty for ${isRootNode ? "root" : "non-root"} node`,
          {
            field: "refundTx",
            value: node.refundTx?.length || 0,
          },
        );
      }

      let nodeTx, refundTx;

      try {
        // Get the node transaction to check its timelock
        nodeTx = getTxFromRawTxBytes(node.nodeTx);
      } catch (error) {
        throw new SparkValidationError(
          `Failed to parse node transaction for ${isRootNode ? "root" : "non-root"} node: ${error instanceof Error ? error.message : String(error)}`,
          {
            field: "nodeTx",
            value: node.nodeTx.length,
          },
        );
      }

      try {
        // Get the refund transaction to check its timelock
        refundTx = getTxFromRawTxBytes(node.refundTx);
      } catch (error) {
        throw new SparkValidationError(
          `Failed to parse refund transaction for ${isRootNode ? "root" : "non-root"} node: ${error instanceof Error ? error.message : String(error)}`,
          {
            field: "refundTx",
            value: node.refundTx.length,
          },
        );
      }

      const nodeInput = nodeTx.getInput(0);
      if (!nodeInput) {
        throw new SparkValidationError(
          `Node transaction has no inputs for ${isRootNode ? "root" : "non-root"} node`,
          {
            field: "nodeInput",
            value: nodeTx.inputsLength,
          },
        );
      }

      const refundInput = refundTx.getInput(0);
      if (!refundInput) {
        throw new SparkValidationError(
          `Refund transaction has no inputs for ${isRootNode ? "root" : "non-root"} node`,
          {
            field: "refundInput",
            value: refundTx.inputsLength,
          },
        );
      }

      if (!refundInput.sequence) {
        throw new SparkValidationError(
          `Refund transaction has no sequence for ${isRootNode ? "root" : "non-root"} node`,
          {
            field: "sequence",
            value: refundInput.sequence,
          },
        );
      }

      // Extract timelock from sequence (lower 16 bits)
      const nodeTimelock = nodeInput.sequence & 0xffff;
      const refundTimelock = refundInput.sequence & 0xffff;

      return {
        nodeTimelock,
        refundTimelock,
      };
    } catch (error) {
      throw new SparkRequestError(
        `Failed to check timelock for node ${nodeId}`,
        {
          operation: "query_nodes",
          error,
        },
      );
    }
  }

  private cleanup() {
    if (this.claimTransfersInterval) {
      clearInterval(this.claimTransfersInterval);
      this.claimTransfersInterval = null;
    }
    if (this.tokenOptimizationInterval) {
      clearInterval(this.tokenOptimizationInterval);
      this.tokenOptimizationInterval = null;
    }
    if (this.streamController) {
      this.streamController.abort();
    }
    this.removeAllListeners();
  }

  public async cleanupConnections() {
    this.cleanup();
    await this.connectionManager.closeConnections();
  }

  // Add this new method to start periodic claiming
  private async startPeriodicClaimTransfers() {
    // Clear any existing interval first
    if (this.claimTransfersInterval) {
      clearInterval(this.claimTransfersInterval);
    }

    await this.claimTransfers();

    // Set up new interval to claim transfers every 5 seconds
    // @ts-ignore
    this.claimTransfersInterval = setInterval(async () => {
      try {
        await this.claimTransfers(
          [
            TransferType.TRANSFER,
            TransferType.COOPERATIVE_EXIT,
            TransferType.PREIMAGE_SWAP,
            TransferType.UTXO_SWAP,
          ],
          true,
        );
      } catch (error) {
        console.error("Error in periodic transfer claiming:", error);
      }
    }, 10000);
  }

  private async updateLeaves(
    leavesToRemove: string[],
    leavesToAdd: TreeNode[],
  ) {
    const leavesToRemoveSet = new Set(leavesToRemove);
    this.leaves = this.leaves.filter((leaf) => !leavesToRemoveSet.has(leaf.id));
    this.leaves.push(...leavesToAdd);
  }

  private async queryNodes(
    baseRequest: Omit<QueryNodesRequest, "limit" | "offset">,
    sparkClientAddress?: string,
    pageSize: number = 100,
  ): Promise<QueryNodesResponse> {
    const address = sparkClientAddress ?? this.config.getCoordinatorAddress();
    const aggregatedNodes: {
      [key: string]: QueryNodesResponse["nodes"][string];
    } = {};
    let offset = 0;

    while (true) {
      const sparkClient =
        await this.connectionManager.createSparkClient(address);

      const response = await sparkClient.query_nodes({
        ...baseRequest,
        limit: pageSize,
        offset,
      });

      /* Merge nodes from this page. If user is sending or receiving payments results can shift
         accross pages, potentially causing duplicates. Dedupe by node id: */
      Object.assign(aggregatedNodes, response.nodes ?? {});

      /* If we received fewer nodes than requested, this was the last page. */
      const received = Object.keys(response.nodes ?? {}).length;
      if (received < pageSize || baseRequest.source?.$case === "nodeIds") {
        return {
          nodes: aggregatedNodes,
          offset: response.offset,
        } as QueryNodesResponse;
      }

      offset += pageSize;
    }
  }

  public async getUserRequests(
    params: GetUserRequestsParams = {},
  ): Promise<SparkWalletUserToUserRequestsConnection | null> {
    const sspClient = this.getSspClient();
    return await sspClient.getUserRequests(params);
  }

  public async setPrivacyEnabled(
    privacyEnabled: boolean,
  ): Promise<WalletSettings | undefined> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );
    const response = await sparkClient.update_wallet_setting({
      privateEnabled: privacyEnabled,
    });

    const walletSetting = response.walletSetting
      ? mapSettingsProtoToWalletSettings(response.walletSetting)
      : undefined;

    return walletSetting;
  }

  public async getWalletSettings(): Promise<WalletSettings | undefined> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );
    const response = await sparkClient.query_wallet_setting({});

    const walletSetting = response.walletSetting
      ? mapSettingsProtoToWalletSettings(response.walletSetting)
      : undefined;

    return walletSetting;
  }

  public async isOptimizationInProgress() {
    return this.optimizationInProgress;
  }

  public async isTokenOptimizationInProgress() {
    return this.tokenOptimizationInProgress;
  }

  protected getOtelTraceUrls() {
    const soConfig = this.config.getSigningOperators();
    const sspBaseUrl = this.config.getSspBaseUrl();
    const domains: string[] = [];
    Object.values(soConfig).forEach((so) => {
      domains.push(so.address);
    });
    if (sspBaseUrl) {
      domains.push(sspBaseUrl);
    }
    return domains;
  }

  protected initializeTracer(wallet: SparkWallet) {
    const consoleOptions = wallet.config.getConsoleOptions();
    const spanProcessors: SpanProcessor[] = [];
    if (consoleOptions.otel) {
      spanProcessors.push(new SimpleSpanProcessor(new ConsoleSpanExporter()));
    }
    const traceUrls = this.getOtelTraceUrls();
    wallet.initializeTracerEnv({ spanProcessors, traceUrls });
  }

  protected getTracer(): Tracer | null {
    return null;
  }

  protected initializeTracerEnv({
    spanProcessors,
    traceUrls,
  }: {
    spanProcessors: SpanProcessor[];
    traceUrls: string[];
  }) {
    /* This needs to be implemented differently depending on platform due to
       incompatible dependencies in both */
  }

  protected static async handlePublicMethodError(
    error: unknown,
    { wallet, traceId }: HandlePublicMethodErrorParams = {},
  ) {
    const context: Record<string, unknown> = {};

    if (typeof traceId === "string" && traceId.length > 0) {
      context.traceId = traceId;
    }

    if (wallet) {
      try {
        const keyBytes = await wallet.config.signer.getIdentityPublicKey();
        context.idPubKey = bytesToHex(keyBytes);
      } catch (keyError) {
        /* Signer not initialized yet, ignore */
      }
    }

    if (error instanceof SparkError) {
      if (Object.keys(context).length > 0) {
        error.update({ context });
      }
      return error;
    }

    if (error instanceof Error) {
      return new SparkError(error.message, { ...context, error });
    }

    /* Non-Error throwables: coerce to string and wrap */
    const message = String(error);
    return new SparkError(message, { ...context, error });
  }

  protected getTraceName(methodName: string) {
    return `SparkWallet.${methodName}`;
  }

  protected static wrapMethod(
    methodName: string,
    originalFn: (...args: unknown[]) => Promise<unknown>,
    wallet: SparkWallet,
  ) {
    const tracer = wallet.getTracer();
    let wrapped: (...args: unknown[]) => Promise<unknown>;

    if (tracer) {
      wrapped = async (...args: unknown[]) => {
        const activeTracer = wallet.getTracer();
        if (!activeTracer) {
          throw new Error("Tracer not initialized");
        }
        return activeTracer.startActiveSpan(
          wallet.getTraceName(methodName),
          async (span) => {
            const traceId = span.spanContext().traceId;
            try {
              const result = await originalFn.apply(wallet, args);
              return result;
            } catch (error) {
              const err = await SparkWallet.handlePublicMethodError(error, {
                wallet,
                traceId,
              });
              throw err;
            } finally {
              span.end();
            }
          },
        );
      };
    } else {
      wrapped = async (...args: unknown[]) => {
        try {
          const result = await originalFn.apply(wallet, args);
          return result;
        } catch (error) {
          const err = await SparkWallet.handlePublicMethodError(error, {
            wallet,
          });
          throw err;
        }
      };
    }
    return wrapped;
  }

  protected wrapPublicMethod<M extends keyof SparkWallet>(methodName: M) {
    const original = this[methodName];

    if (typeof original !== "function") {
      throw new Error(`Method ${methodName} is not a function on SparkWallet.`);
    }

    const originalFn = original as (...args: unknown[]) => Promise<unknown>;
    const wrapped = SparkWallet.wrapMethod(
      String(methodName),
      originalFn,
      this,
    ) as SparkWallet[M];
    (this as SparkWallet)[methodName] = wrapped;
  }

  private wrapPublicMethods() {
    PUBLIC_SPARK_WALLET_METHODS.forEach((methodName) =>
      this.wrapPublicMethod(methodName),
    );
  }
}

type AssertNever<T extends never> = T;

type SparkWalletFunctionKeys = Extract<
  {
    [K in keyof SparkWallet]: SparkWallet[K] extends (
      ...args: any[]
    ) => PromiseLike<unknown>
      ? K
      : never;
  }[keyof SparkWallet],
  string
>;

type WrappableSparkWalletMethod = Exclude<
  SparkWalletFunctionKeys,
  "constructor" | "getTokenOutputStats"
>;

const PUBLIC_SPARK_WALLET_METHODS = [
  "advancedDeposit",
  "batchTransferTokens",
  "checkTimelock",
  "claimDeposit",
  "claimStaticDeposit",
  "claimStaticDepositWithMaxFee",
  "cleanupConnections",
  "createHTLC",
  "getHTLCPreimage",
  "claimHTLC",
  "queryHTLC",
  "createHTLCSenderSpendTx",
  "createHTLCReceiverSpendTx",
  "createLightningHodlInvoice",
  "createLightningInvoice",
  "createSatsInvoice",
  "createTokensInvoice",
  "fulfillSparkInvoice",
  "getBalance",
  "getClaimStaticDepositQuote",
  "getCoopExitRequest",
  "getIdentityPublicKey",
  "getLeaves",
  "getLightningReceiveRequest",
  "getLightningSendFeeEstimate",
  "getLightningSendRequest",
  "getSingleUseDepositAddress",
  "getSparkAddress",
  "getStaticDepositAddress",
  "getSwapFeeEstimate",
  "getTokenL1Address",
  "getTransfer",
  "getTransferFromSsp",
  "getTransfers",
  "getUnusedDepositAddresses",
  "getUserRequests",
  "getUtxosForDepositAddress",
  "getWalletSettings",
  "getWithdrawalFeeQuote",
  "isOptimizationInProgress",
  "isTokenOptimizationInProgress",
  "optimizeTokenOutputs",
  "payLightningInvoice",
  "querySparkInvoices",
  "queryStaticDepositAddresses",
  "queryTokenTransactions",
  "queryTokenTransactionsByTxHashes",
  "queryTokenTransactionsWithFilters",
  "refundAndBroadcastStaticDeposit",
  "refundStaticDeposit",
  "setPrivacyEnabled",
  "signMessageWithIdentityKey",
  "signTransaction",
  "transfer",
  "transferTokens",
  "validateMessageWithIdentityKey",
  "withdraw",
] as const satisfies readonly WrappableSparkWalletMethod[];

/* Type guard to ensure all public methods are in PUBLIC_SPARK_WALLET_METHODS */
type _AllWrappableMethodsCovered = AssertNever<
  Exclude<
    WrappableSparkWalletMethod,
    (typeof PUBLIC_SPARK_WALLET_METHODS)[number]
  >
>;

function isConnectedStreamEvent(
  event: SubscribeToEventsResponse["event"],
): event is { $case: "connected"; connected: ConnectedEvent } {
  return event?.$case === "connected";
}

function isTransferStreamEvent(
  event: SubscribeToEventsResponse["event"],
): event is { $case: "transfer"; transfer: { transfer: Transfer } } {
  return Boolean(event?.$case === "transfer" && event.transfer.transfer);
}

function isDepositStreamEvent(
  event: SubscribeToEventsResponse["event"],
): event is { $case: "deposit"; deposit: { deposit: TreeNode } } {
  return Boolean(event?.$case === "deposit" && event.deposit.deposit);
}
