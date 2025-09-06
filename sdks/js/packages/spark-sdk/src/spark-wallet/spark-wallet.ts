import { isNode, isObject, mapCurrencyAmount } from "@lightsparkdev/core";
import { secp256k1 } from "@noble/curves/secp256k1";
import {
  bytesToHex,
  bytesToNumberBE,
  equalBytes,
  hexToBytes,
  numberToVarBytesBE,
} from "@noble/curves/utils";
import { validateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { Mutex } from "async-mutex";
import { uuidv7, uuidv7obj } from "uuidv7";
import {
  ConfigurationError,
  NetworkError,
  NotImplementedError,
  RPCError,
  ValidationError,
} from "../errors/types.js";
import SspClient, { TransferWithUserRequest } from "../graphql/client.js";
import {
  BitcoinNetwork,
  ClaimStaticDepositOutput,
  CoopExitFeeQuote,
  CoopExitRequest,
  ExitSpeed,
  LeavesSwapFeeEstimateOutput,
  LeavesSwapRequest,
  LightningReceiveRequest,
  LightningSendFeeEstimateInput,
  LightningSendRequest,
  RequestCoopExitInput,
  StaticDepositQuoteOutput,
  UserLeafInput,
} from "../graphql/objects/index.js";
import {
  DepositAddressQueryResult,
  OutputWithPreviousTransactionData,
  QueryNodesRequest,
  QueryNodesResponse,
  SigningJob,
  SubscribeToEventsResponse,
  Transfer,
  TransferStatus,
  TransferType,
  TreeNode,
  UtxoSwapRequestType,
} from "../proto/spark.js";
import { QueryTokenTransactionsResponse } from "../proto/spark_token.js";
import { WalletConfigService } from "../services/config.js";
import { ConnectionManager } from "../services/connection.js";
import { CoopExitService } from "../services/coop-exit.js";
import { DepositService } from "../services/deposit.js";
import { LightningService } from "../services/lightning.js";
import { TokenTransactionService } from "../services/token-transactions.js";
import type { LeafKeyTweak } from "../services/transfer.js";
import { TransferService } from "../services/transfer.js";
import {
  ConfigOptions,
  ELECTRS_CREDENTIALS,
} from "../services/wallet-config.js";
import {
  applyAdaptorToSignature,
  generateAdaptorFromSignature,
  generateSignatureFromExistingAdaptor,
} from "../utils/adaptor-signature.js";
import {
  computeTaprootKeyNoScript,
  getP2TRScriptFromPublicKey,
  getP2WPKHAddressFromPublicKey,
  getSigHashFromTx,
  getTxEstimatedVbytesSizeByNumberOfInputsOutputs,
  getTxFromRawTxBytes,
  getTxFromRawTxHex,
  getTxId,
} from "../utils/bitcoin.js";
import {
  getNetwork,
  Network,
  NetworkToProto,
  NetworkType,
} from "../utils/network.js";
import { sumAvailableTokens } from "../utils/token-transactions.js";
import {
  doesLeafNeedRefresh,
  getCurrentTimelock,
} from "../utils/transaction.js";

import { sha256 } from "@noble/hashes/sha2";
import { trace, Tracer } from "@opentelemetry/api";
import {
  ConsoleSpanExporter,
  SimpleSpanProcessor,
  SpanProcessor,
} from "@opentelemetry/sdk-trace-base";
import { EventEmitter } from "eventemitter3";
import { ClientError, Status } from "nice-grpc-common";
import { isReactNative } from "../constants.js";
import { Network as NetworkProto, networkToJSON } from "../proto/spark.js";
import {
  decodeInvoice,
  getNetworkFromInvoice,
  isValidSparkFallback,
} from "../services/bolt11-spark.js";
import { SigningService } from "../services/signing.js";
import { SparkSigner } from "../signer/signer.js";
import { KeyDerivation, KeyDerivationType } from "../signer/types.js";
import { BitcoinFaucet } from "../tests/utils/test-faucet.js";
import {
  mapTransferToWalletTransfer,
  mapTreeNodeToWalletLeaf,
  UserRequestType,
  WalletLeaf,
  WalletTransfer,
} from "../types/sdk-types.js";
import {
  decodeSparkAddress,
  encodeSparkAddress,
  encodeSparkAddressWithSignature,
  SparkAddressFormat,
  validateSparkInvoiceFields,
  isSafeForNumber,
} from "../utils/address.js";
import { chunkArray } from "../utils/chunkArray.js";
import { getFetch } from "../utils/fetch.js";
import { HashSparkInvoice } from "../utils/invoice-hashing.js";
import { addPublicKeys } from "../utils/keys.js";
import { RetryContext, withRetry } from "../utils/retry.js";
import {
  Bech32mTokenIdentifier,
  decodeBech32mTokenIdentifier,
  encodeBech32mTokenIdentifier,
} from "../utils/token-identifier.js";
import type {
  CreateLightningInvoiceParams,
  DepositParams,
  InitWalletResponse,
  PayLightningInvoiceParams,
  SparkWalletProps,
  TokenBalanceMap,
  TokenMetadataMap,
  TokenOutputsMap,
  TransferParams,
  UserTokenMetadata,
  TransferWithInvoiceParams,
  TransferWithInvoiceOutcome,
  FulfillSparkInvoiceResponse,
  TokenInvoice,
  InvalidInvoice,
  GroupSparkInvoicesResult,
} from "./types.js";

/**
 * The SparkWallet class is the primary interface for interacting with the Spark network.
 * It provides methods for creating and managing wallets, handling deposits, executing transfers,
 * and interacting with the Lightning Network.
 */
export class SparkWallet extends EventEmitter {
  protected config: WalletConfigService;

  protected connectionManager: ConnectionManager;
  protected transferService: TransferService;
  protected tracerId = "spark-sdk";

  private depositService: DepositService;
  private lightningService: LightningService;
  private coopExitService: CoopExitService;
  private signingService: SigningService;
  private tokenTransactionService: TokenTransactionService;

  private claimTransferMutex = new Mutex();
  private leavesMutex = new Mutex();
  private optimizationInProgress = false;
  private sspClient: SspClient | null = null;

  private mutexes: Map<string, Mutex> = new Map();

  private pendingWithdrawnOutputIds: string[] = [];

  private sparkAddress: SparkAddressFormat | undefined;

  private streamController: AbortController | null = null;

  protected leaves: TreeNode[] = [];

  protected tokenMetadata: TokenMetadataMap = new Map();
  protected tokenOutputs: TokenOutputsMap = new Map();

  // Add this property near the top of the class with other private properties
  private claimTransfersInterval: ReturnType<typeof setInterval> | null = null;

  private tracer: Tracer | null = null;

  protected constructor(options?: ConfigOptions, signer?: SparkSigner) {
    super();

    this.config = new WalletConfigService(options, signer);
    this.connectionManager = new ConnectionManager(this.config);
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

    this.tracer = trace.getTracer(this.tracerId);
    this.wrapSparkWalletMethodsWithTracing();
  }

  public static async initialize({
    mnemonicOrSeed,
    accountNumber,
    signer,
    options,
  }: SparkWalletProps) {
    const wallet = new SparkWallet(options, signer);
    wallet.initializeTracer(wallet);

    if (options && options.signerWithPreExistingKeys) {
      await wallet.initWalletWithoutSeed();

      return {
        wallet,
      };
    }

    const initResponse = await wallet.initWallet(mnemonicOrSeed, accountNumber);

    return {
      wallet,
      ...initResponse,
    };
  }

  private async initializeWallet() {
    this.sspClient = new SspClient(this.config);
    await this.connectionManager.createClients();

    if (isReactNative) {
      this.startPeriodicClaimTransfers();
    } else {
      this.setupBackgroundStream();
    }

    await this.syncWallet();
  }

  private getSspClient() {
    if (!this.sspClient) {
      throw new ConfigurationError("SSP client not initialized", {
        configKey: "sspClient",
      });
    }
    return this.sspClient;
  }

  private async handleStreamEvent({ event }: SubscribeToEventsResponse) {
    try {
      if (
        event?.$case === "transfer" &&
        event.transfer.transfer &&
        event.transfer.transfer.type !== TransferType.COUNTER_SWAP
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
            optimize: true,
          });
        }
      } else if (event?.$case === "deposit" && event.deposit.deposit) {
        const deposit = event.deposit.deposit;

        const newLeaf = await this.transferService.extendTimelock(deposit);
        await this.transferLeavesToSelf(newLeaf.nodes, {
          type: KeyDerivationType.LEAF,
          path: deposit.id,
        });
        this.emit(
          "deposit:confirmed",
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

    this.streamController = new AbortController();

    const delay = (ms: number, signal?: AbortSignal): Promise<boolean> => {
      return new Promise((resolve) => {
        const timer = setTimeout(() => {
          if (signal) {
            signal.removeEventListener("abort", onAbort);
          }
          resolve(true);
        }, ms);

        function onAbort() {
          clearTimeout(timer);
          resolve(false);
          signal?.removeEventListener("abort", onAbort);
        }

        if (signal) {
          signal.addEventListener("abort", onAbort);
        }
      });
    };

    let retryCount = 0;
    while (retryCount <= MAX_RETRIES) {
      try {
        const address = this.config.getCoordinatorAddress();

        const sparkClient =
          await this.connectionManager.createSparkStreamClient(address);
        const channel = await this.connectionManager.getStreamChannel(address);

        const stream = sparkClient.subscribe_to_events(
          {
            identityPublicKey: await this.config.signer.getIdentityPublicKey(),
          },
          {
            signal: this.streamController?.signal,
          },
        );

        // In Node.js, long-lived gRPC streams keep the underlying socket "ref'd",
        // which prevents the process from exiting. To avoid that (e.g. in CLI tools),
        // we manually unref the socket so Node can shut down when nothing else is active.
        //
        // The gRPC client doesn't expose the socket directly, so we dig through
        // internal fields to find it. This is a bit of a hack and may break if the
        // internals change.
        //
        // Since the socket isn't always immediately available, we retry with setTimeout
        // until it shows up.
        const maybeUnref = () => {
          const internalChannel = (channel as any).internalChannel;
          if (
            internalChannel?.currentPicker?.subchannel?.child?.transport
              ?.session?.socket
          ) {
            internalChannel.currentPicker.subchannel.child.transport.session.socket.unref();
          } else {
            setTimeout(maybeUnref, 100);
          }
        };

        // Only need to unref in Node environments.
        // In the browser and React Native, the runtime handles shutdown when the tab/app closes.
        if (isNode) {
          maybeUnref();
        }

        const claimedTransfersIds = await this.claimTransfers();

        try {
          for await (const data of stream) {
            if (this.streamController?.signal.aborted) {
              break;
            }

            if (data.event?.$case === "connected") {
              this.emit("stream:connected");
              retryCount = 0;
            }

            if (
              data.event?.$case === "transfer" &&
              data.event.transfer.transfer &&
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
        if (this.streamController?.signal.aborted) {
          break;
        }

        const backoffDelay = Math.min(
          INITIAL_DELAY * Math.pow(2, retryCount),
          MAX_DELAY,
        );

        if (retryCount < MAX_RETRIES) {
          retryCount++;
          this.emit(
            "stream:reconnecting",
            retryCount,
            MAX_RETRIES,
            backoffDelay,
            error instanceof Error ? error.message : String(error),
          );
          try {
            const completed = await delay(
              backoffDelay,
              this.streamController?.signal,
            );
            if (!completed) {
              break;
            }
          } catch (error) {
            if (this.streamController?.signal.aborted) {
              break;
            }
          }
        } else {
          this.emit("stream:disconnected", "Max reconnection attempts reached");
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

  private async checkExtendLeaves(leaves: TreeNode[]): Promise<void> {
    await this.withLeaves(async () => {
      for (const leaf of leaves) {
        if (!leaf.parentNodeId && leaf.status === "AVAILABLE") {
          const res = await this.transferService.extendTimelock(leaf);
          await this.transferLeavesToSelf(res.nodes, {
            type: KeyDerivationType.LEAF,
            path: leaf.id,
          });
        }
      }
    });
  }

  private verifyKey(
    pubkey1: Uint8Array,
    pubkey2: Uint8Array,
    verifyingKey: Uint8Array,
  ): boolean {
    return equalBytes(addPublicKeys(pubkey1, pubkey2), verifyingKey);
  }

  private popOrThrow<T>(arr: T[] | undefined, msg: string): T {
    if (!arr || arr.length === 0) throw new ValidationError(msg);
    return arr.pop() as T;
  }

  private async selectLeaves(
    targetAmounts: number[],
  ): Promise<Map<number, TreeNode[][]>> {
    if (targetAmounts.length === 0) {
      throw new ValidationError("Target amounts must be non-empty", {
        field: "targetAmounts",
        value: targetAmounts,
      });
    }

    if (targetAmounts.some((amount) => amount <= 0)) {
      throw new ValidationError("Target amount must be positive", {
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
      throw new ValidationError(
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
      throw new ValidationError("No owned leaves found", {
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

  private areLeavesInefficient() {
    const totalAmount = this.getInternalBalance();

    if (this.leaves.length <= 1) {
      return false;
    }

    const nextLowerPowerOfTwo = 31 - Math.clz32(totalAmount);

    let remainingAmount = totalAmount;
    let optimalLeavesLength = 0;

    for (let i = nextLowerPowerOfTwo; i >= 0; i--) {
      const denomination = 2 ** i;
      while (remainingAmount >= denomination) {
        remainingAmount -= denomination;
        optimalLeavesLength++;
      }
    }

    return this.leaves.length > optimalLeavesLength * 5;
  }

  private async optimizeLeaves() {
    if (this.optimizationInProgress || !this.areLeavesInefficient()) {
      return;
    }

    await this.withLeaves(async () => {
      this.optimizationInProgress = true;
      try {
        if (this.leaves.length > 0) {
          await this.requestLeavesSwap({ leaves: this.leaves });
        }
        this.leaves = await this.getLeaves();
      } finally {
        this.optimizationInProgress = false;
      }
    });
  }

  private async syncWallet() {
    await this.syncTokenOutputs();

    let leaves = await this.getLeaves();

    leaves = await this.checkRefreshTimelockNodes(leaves);
    leaves = await this.checkExtendTimeLockNodes(leaves);

    this.leaves = leaves;
    this.checkExtendLeaves(leaves);

    this.optimizeLeaves().catch((e) => {
      console.error("Failed to optimize leaves", e);
    });
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
   * @param {string} [params.senderPublicKey] - The public key of the expected sender
   * @param {Date} [params.expiryTime] - The expiry time of the payment
   * @returns {Promise<SparkAddressFormat>} The Spark address for the sats payment
   */
  public async createSatsInvoice({
    amount,
    memo,
    senderPublicKey,
    expiryTime,
  }: {
    amount?: number;
    memo?: string;
    senderPublicKey?: string;
    expiryTime?: Date;
  }): Promise<SparkAddressFormat> {
    const MAX_SATS_AMOUNT = 2_100_000_000_000_000; // 21_000_000 BTC * 100_000_000 sats/BTC
    if (amount && (amount < 0 || amount > MAX_SATS_AMOUNT)) {
      throw new ValidationError(
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
    const invoiceFields = {
      version: 1,
      id: uuidv7obj().bytes,
      paymentType: protoPayment,
      memo: memo,
      senderPublicKey: senderPublicKey
        ? hexToBytes(senderPublicKey)
        : undefined,
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
   * Creates a Spark invoice for a tokens payment on Spark.
   *
   * @param {Object} params - Parameters for the tokens payment
   * @param {bigint} [params.amount] - The amount of tokens to receive
   * @param {Bech32mTokenIdentifier} [params.tokenIdentifier] - The token identifier
   * @param {string} [params.memo] - The memo for the payment
   * @param {string} [params.senderPublicKey] - The public key of the expected sender
   * @param {Date} [params.expiryTime] - The expiry time of the payment
   * @returns {Promise<SparkAddressFormat>} The Spark address for the tokens payment
   */
  public async createTokensInvoice({
    amount,
    tokenIdentifier,
    memo,
    senderPublicKey,
    expiryTime,
  }: {
    tokenIdentifier?: Bech32mTokenIdentifier;
    amount?: bigint;
    memo?: string;
    senderPublicKey?: string;
    expiryTime?: Date;
  }): Promise<SparkAddressFormat> {
    const MAX_UINT128 = BigInt(2 ** 128 - 1);
    if (amount && (amount < 0 || amount > MAX_UINT128)) {
      throw new ValidationError(`Amount must be between 0 and ${MAX_UINT128}`, {
        field: "amount",
        value: amount,
        expected: `greater than or equal to 0 and less than or equal to ${MAX_UINT128}`,
      });
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
    const invoiceFields = {
      version: 1,
      id: uuidv7obj().bytes,
      paymentType: protoPayment,
      memo: memo ?? undefined,
      senderPublicKey: senderPublicKey
        ? hexToBytes(senderPublicKey)
        : undefined,
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
   *   - balance: The wallet's initial balance in satoshis
   *   - tokenBalance: Map of token balances
   * @private
   */
  protected async initWallet(
    mnemonicOrSeed?: Uint8Array | string,
    accountNumber?: number,
  ): Promise<InitWalletResponse | undefined> {
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
    };
  }

  /**
   * Initializes the wallet without a seed. Meant for use with a signer with pre-existing keys.
   * @private
   */
  protected async initWalletWithoutSeed() {
    await this.initializeWallet();

    const identityPublicKey = await this.config.signer.getIdentityPublicKey();

    if (!identityPublicKey || identityPublicKey.length === 0) {
      throw new ValidationError("Identity public key not found in signer", {
        field: "identityPublicKey",
        value: identityPublicKey,
      });
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
    await this.initializeWallet();

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
      throw new ValidationError("targetAmount must be less than 2^53", {
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
        throw new ValidationError(
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

    const {
      transfer,
      signatureMap,
      directSignatureMap,
      directFromCpfpSignatureMap,
    } = await this.transferService.startSwapSignRefund(
      leafKeyTweaks,
      hexToBytes(this.config.getSspIdentityPublicKey()),
      new Date(Date.now() + 2 * 60 * 1000),
    );

    try {
      if (!transfer.leaves[0]?.leaf) {
        console.error("[processSwapBatch] First leaf is missing");
        throw new Error("Failed to get leaf");
      }

      const cpfpRefundSignature = signatureMap.get(transfer.leaves[0].leaf.id);
      if (!cpfpRefundSignature) {
        console.error(
          "[processSwapBatch] Missing CPFP refund signature for first leaf",
        );
        throw new Error("Failed to get CPFP refund signature");
      }

      const directRefundSignature = directSignatureMap.get(
        transfer.leaves[0].leaf.id,
      );
      if (!directRefundSignature) {
        console.error(
          "[processSwapBatch] Missing direct refund signature for first leaf",
        );
        throw new Error("Failed to get direct refund signature");
      }

      const directFromCpfpRefundSignature = directFromCpfpSignatureMap.get(
        transfer.leaves[0].leaf.id,
      );
      if (!directFromCpfpRefundSignature) {
        console.error(
          "[processSwapBatch] Missing direct from CPFP refund signature for first leaf",
        );
        throw new Error("Failed to get direct from CPFP refund signature");
      }

      const {
        adaptorPrivateKey: cpfpAdaptorPrivateKey,
        adaptorSignature: cpfpAdaptorSignature,
      } = generateAdaptorFromSignature(cpfpRefundSignature);

      let directAdaptorPrivateKey: Uint8Array = new Uint8Array();
      let directAdaptorSignature: Uint8Array = new Uint8Array();
      let directFromCpfpAdaptorPrivateKey: Uint8Array = new Uint8Array();
      let directFromCpfpAdaptorSignature: Uint8Array = new Uint8Array();

      if (directRefundSignature.length > 0) {
        const { adaptorPrivateKey, adaptorSignature } =
          generateAdaptorFromSignature(directRefundSignature);

        directAdaptorPrivateKey = adaptorPrivateKey;
        directAdaptorSignature = adaptorSignature;
      }

      if (directFromCpfpRefundSignature.length > 0) {
        const { adaptorPrivateKey, adaptorSignature } =
          generateAdaptorFromSignature(directFromCpfpRefundSignature);
        directFromCpfpAdaptorPrivateKey = adaptorPrivateKey;
        directFromCpfpAdaptorSignature = adaptorSignature;
      }

      if (!transfer.leaves[0].leaf) {
        console.error(
          "[processSwapBatch] First leaf missing when preparing user leaves",
        );
        throw new Error("Failed to get leaf");
      }

      const userLeaves: UserLeafInput[] = [];
      userLeaves.push({
        leaf_id: transfer.leaves[0].leaf.id,
        raw_unsigned_refund_transaction: bytesToHex(
          transfer.leaves[0].intermediateRefundTx,
        ),
        direct_raw_unsigned_refund_transaction: bytesToHex(
          transfer.leaves[0].intermediateDirectRefundTx,
        ),
        direct_from_cpfp_raw_unsigned_refund_transaction: bytesToHex(
          transfer.leaves[0].intermediateDirectFromCpfpRefundTx,
        ),
        adaptor_added_signature: bytesToHex(cpfpAdaptorSignature),
        direct_adaptor_added_signature: bytesToHex(directAdaptorSignature),
        direct_from_cpfp_adaptor_added_signature: bytesToHex(
          directFromCpfpAdaptorSignature,
        ),
      });

      for (let i = 1; i < transfer.leaves.length; i++) {
        const leaf = transfer.leaves[i];
        if (!leaf?.leaf) {
          console.error(`[processSwapBatch] Leaf ${i + 1} is missing`);
          throw new Error("Failed to get leaf");
        }

        const cpfpRefundSignature = signatureMap.get(leaf.leaf.id);
        if (!cpfpRefundSignature) {
          console.error(
            `[processSwapBatch] Missing CPFP refund signature for leaf ${i + 1}`,
          );
          throw new Error("Failed to get CPFP refund signature");
        }

        const directRefundSignature = directSignatureMap.get(leaf.leaf.id);
        if (!directRefundSignature) {
          console.error(
            `[processSwapBatch] Missing direct refund signature for leaf ${i + 1}`,
          );
          throw new Error("Failed to get direct refund signature");
        }

        const directFromCpfpRefundSignature = directFromCpfpSignatureMap.get(
          leaf.leaf.id,
        );
        if (!directFromCpfpRefundSignature) {
          console.error(
            `[processSwapBatch] Missing direct from CPFP refund signature for leaf ${i + 1}`,
          );
          throw new Error("Failed to get direct from CPFP refund signature");
        }

        const cpfpSignature = generateSignatureFromExistingAdaptor(
          cpfpRefundSignature,
          cpfpAdaptorPrivateKey,
        );

        let directSignature: Uint8Array = new Uint8Array();
        if (directRefundSignature.length > 0) {
          directSignature = generateSignatureFromExistingAdaptor(
            directRefundSignature,
            directAdaptorPrivateKey,
          );
        }

        let directFromCpfpSignature: Uint8Array = new Uint8Array();
        if (directFromCpfpRefundSignature.length > 0) {
          directFromCpfpSignature = generateSignatureFromExistingAdaptor(
            directFromCpfpRefundSignature,
            directFromCpfpAdaptorPrivateKey,
          );
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
          adaptor_added_signature: bytesToHex(cpfpSignature),
          direct_adaptor_added_signature: bytesToHex(directSignature),
          direct_from_cpfp_adaptor_added_signature: bytesToHex(
            directFromCpfpSignature,
          ),
        });
      }

      const sspClient = this.getSspClient();
      const cpfpAdaptorPubkey = bytesToHex(
        secp256k1.getPublicKey(cpfpAdaptorPrivateKey),
      );
      if (!cpfpAdaptorPubkey) {
        throw new Error("Failed to generate CPFP adaptor pubkey");
      }

      let directAdaptorPubkey: string | undefined;
      if (directAdaptorPrivateKey.length > 0) {
        directAdaptorPubkey = bytesToHex(
          secp256k1.getPublicKey(directAdaptorPrivateKey),
        );
      }

      let directFromCpfpAdaptorPubkey: string | undefined;
      if (directFromCpfpAdaptorPrivateKey.length > 0) {
        directFromCpfpAdaptorPubkey = bytesToHex(
          secp256k1.getPublicKey(directFromCpfpAdaptorPrivateKey),
        );
      }

      let request: LeavesSwapRequest | null | undefined = null;
      const targetAmountSats =
        targetAmounts?.reduce((acc, amount) => acc + amount, 0) ||
        leavesBatch.reduce((acc, leaf) => acc + leaf.value, 0);
      const totalAmountSats = leavesBatch.reduce(
        (acc, leaf) => acc + leaf.value,
        0,
      );

      request = await sspClient.requestLeaveSwap({
        userLeaves,
        adaptorPubkey: cpfpAdaptorPubkey,
        directAdaptorPubkey: directAdaptorPubkey,
        directFromCpfpAdaptorPubkey: directFromCpfpAdaptorPubkey,
        targetAmountSats,
        totalAmountSats,
        targetAmountSatsList: targetAmounts,
        // TODO: Request fee from SSP
        feeSats: 0,
        idempotencyKey: uuidv7(),
      });

      if (!request) {
        console.error("[processSwapBatch] Leave swap request returned null");
        throw new Error("Failed to request leaves swap. No response returned.");
      }

      const nodes = await this.queryNodes({
        source: {
          $case: "nodeIds",
          nodeIds: {
            nodeIds: request.swapLeaves.map((leaf) => leaf.leafId),
          },
        },
        includeParents: false,
        network: NetworkToProto[this.config.getNetwork()],
      });

      if (Object.values(nodes.nodes).length !== request.swapLeaves.length) {
        console.error("[processSwapBatch] Node count mismatch:", {
          actual: Object.values(nodes.nodes).length,
          expected: request.swapLeaves.length,
        });
        throw new Error("Expected same number of nodes as swapLeaves");
      }

      for (const [nodeId, node] of Object.entries(nodes.nodes)) {
        if (!node.nodeTx) {
          console.error(`[processSwapBatch] Node tx missing for ${nodeId}`);
          throw new Error(`Node tx not found for leaf ${nodeId}`);
        }

        if (!node.verifyingPublicKey) {
          console.error(
            `[processSwapBatch] Verifying public key missing for ${nodeId}`,
          );
          throw new Error(`Node public key not found for leaf ${nodeId}`);
        }

        const leaf = request.swapLeaves.find((leaf) => leaf.leafId === nodeId);
        if (!leaf) {
          console.error(`[processSwapBatch] Leaf not found for node ${nodeId}`);
          throw new Error(`Leaf not found for node ${nodeId}`);
        }
        // Apply CPFP adaptor signature
        const cpfpNodeTx = getTxFromRawTxBytes(node.nodeTx);
        const cpfpRefundTxBytes = hexToBytes(leaf.rawUnsignedRefundTransaction);
        const cpfpRefundTx = getTxFromRawTxBytes(cpfpRefundTxBytes);
        const cpfpSighash = getSigHashFromTx(
          cpfpRefundTx,
          0,
          cpfpNodeTx.getOutput(0),
        );

        const nodePublicKey = node.verifyingPublicKey;
        const taprootKey = computeTaprootKeyNoScript(nodePublicKey.slice(1));
        const cpfpAdaptorSignatureBytes = hexToBytes(
          leaf.adaptorSignedSignature,
        );
        applyAdaptorToSignature(
          taprootKey.slice(1),
          cpfpSighash,
          cpfpAdaptorSignatureBytes,
          cpfpAdaptorPrivateKey,
        );

        // Apply direct adaptor signature

        if (leaf.directRawUnsignedRefundTransaction) {
          const directNodeTx = getTxFromRawTxBytes(node.directTx);
          const directRefundTxBytes = hexToBytes(
            leaf.directRawUnsignedRefundTransaction,
          );
          const directRefundTx = getTxFromRawTxBytes(directRefundTxBytes);
          const directSighash = getSigHashFromTx(
            directRefundTx,
            0,
            directNodeTx.getOutput(0),
          );
          if (!leaf.directAdaptorSignedSignature) {
            throw new Error(
              `Direct adaptor signed signature missing for node ${nodeId}`,
            );
          }
          const directAdaptorSignatureBytes = hexToBytes(
            leaf.directAdaptorSignedSignature,
          );

          applyAdaptorToSignature(
            taprootKey.slice(1),
            directSighash,
            directAdaptorSignatureBytes,
            directAdaptorPrivateKey,
          );
        }

        if (leaf.directFromCpfpRawUnsignedRefundTransaction) {
          const directFromCpfpRefundTxBytes = hexToBytes(
            leaf.directFromCpfpRawUnsignedRefundTransaction,
          );
          const directFromCpfpRefundTx = getTxFromRawTxBytes(
            directFromCpfpRefundTxBytes,
          );
          const directFromCpfpSighash = getSigHashFromTx(
            directFromCpfpRefundTx,
            0,
            cpfpNodeTx.getOutput(0),
          );
          if (!leaf.directFromCpfpAdaptorSignedSignature) {
            throw new Error(
              `Direct adaptor signed signature missing for node ${nodeId}`,
            );
          }
          const directFromCpfpAdaptorSignatureBytes = hexToBytes(
            leaf.directFromCpfpAdaptorSignedSignature,
          );
          applyAdaptorToSignature(
            taprootKey.slice(1),
            directFromCpfpSighash,
            directFromCpfpAdaptorSignatureBytes,
            directFromCpfpAdaptorPrivateKey,
          );
        }
      }
      await this.transferService.deliverTransferPackage(
        transfer,
        leafKeyTweaks,
        signatureMap,
        directSignatureMap,
        directFromCpfpSignatureMap,
      );
      const completeResponse = await sspClient.completeLeaveSwap({
        adaptorSecretKey: bytesToHex(cpfpAdaptorPrivateKey),
        directAdaptorSecretKey: bytesToHex(directAdaptorPrivateKey),
        directFromCpfpAdaptorSecretKey: bytesToHex(
          directFromCpfpAdaptorPrivateKey,
        ),
        userOutboundTransferExternalId: transfer.id,
        leavesSwapRequestId: request.id,
      });

      if (!completeResponse || !completeResponse.inboundTransfer?.sparkId) {
        console.error(
          "[processSwapBatch] Invalid complete response:",
          completeResponse,
        );
        throw new Error("Failed to complete leaves swap");
      }

      const incomingTransfer = await this.transferService.queryTransfer(
        completeResponse.inboundTransfer.sparkId,
      );

      if (!incomingTransfer) {
        console.error("[processSwapBatch] No incoming transfer found");
        throw new Error("Failed to get incoming transfer");
      }

      return await this.claimTransfer({
        transfer: incomingTransfer,
        emit: false,
        optimize: false,
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

    if (this.tokenOutputs.size !== 0) {
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
    let metadataToFetch = new Array<Bech32mTokenIdentifier>();
    for (const tokenIdentifier of this.tokenOutputs.keys()) {
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
        throw new NetworkError("Failed to fetch token metadata", {
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
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
      });
    }

    return tokenMetadataMap;
  }

  private async getTokenBalance(): Promise<TokenBalanceMap> {
    const tokenMetadataMap = await this.getTokenMetadata();

    const result: TokenBalanceMap = new Map();
    for (const [tokenIdentifier, tokenMetadata] of tokenMetadataMap) {
      const outputs = this.tokenOutputs.get(tokenIdentifier);

      const humanReadableTokenIdentifier = encodeBech32mTokenIdentifier({
        tokenIdentifier: tokenMetadata.rawTokenIdentifier,
        network: this.config.getNetworkType(),
      });

      result.set(humanReadableTokenIdentifier, {
        balance: outputs ? sumAvailableTokens(outputs) : BigInt(0),
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

    const address = await this.depositService!.generateStaticDepositAddress({
      signingPubkey,
    });
    if (!address.depositAddress) {
      throw new RPCError("Failed to generate static deposit address", {
        method: "generateStaticDepositAddress",
        params: { signingPubkey },
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

    const address = await this.depositService!.generateDepositAddress({
      signingPubkey,
      leafId,
    });
    if (!address.depositAddress) {
      throw new RPCError("Failed to generate deposit address", {
        method: "generateDepositAddress",
        params: { signingPubkey, leafId },
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
      throw new ValidationError("Deposit address cannot be empty", {
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
      throw new NetworkError(
        "Failed to get UTXOs for deposit address",
        {
          operation: "get_utxos_for_address",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
        error as Error,
      );
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
      throw new ValidationError("Fee larger than max fee", {
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
      throw new ValidationError("Fee or satsPerVbyteFee must be provided");
    }

    // Users can set this to 300 or higher due to our old flow so they may be trained to type in 300 or higher which would make the fee way too high.
    if (satsPerVbyteFee && satsPerVbyteFee > 150) {
      throw new ValidationError("satsPerVbyteFee must be less than 150");
    }

    const finalFee = satsPerVbyteFee
      ? satsPerVbyteFee * getTxEstimatedVbytesSizeByNumberOfInputsOutputs(1, 1)
      : fee!;

    if (finalFee < 194) {
      throw new ValidationError("Fee must be at least 194", {
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
      throw new ValidationError(
        "Fee too large. Credit amount must be greater than 0",
        {
          field: "creditAmountSats",
          value: creditAmountSats,
        },
      );
    }

    const tx = new Transaction();

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
      throw new ValidationError("Transaction hex cannot be empty", {
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
      throw new ValidationError("Transaction ID cannot be empty", {
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
      throw new ValidationError("Invalid transaction hex", {
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
      throw new ValidationError("vout must be less than 2^53", {
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

    const resultingNodes: TreeNode[] = [];
    for (const node of res.nodes) {
      if (node.status === "AVAILABLE") {
        const { nodes } = await this.transferService.extendTimelock(node);

        for (const n of nodes) {
          if (n.status === "AVAILABLE") {
            const transfer = await this.transferLeavesToSelf([n], {
              type: KeyDerivationType.LEAF,
              path: node.id,
            });
            resultingNodes.push(...transfer);
          } else {
            resultingNodes.push(n);
          }
        }
      } else {
        resultingNodes.push(node);
      }
    }

    return resultingNodes;
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
      throw new ValidationError("Transaction ID cannot be empty", {
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
        throw new ValidationError("Invalid transaction hex", {
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
        throw new ValidationError("Deposit address has already been used", {
          field: "depositAddress",
          value: depositAddress,
        });
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
      throw new ValidationError("Receiver Spark address cannot be empty", {
        field: "receiverSparkAddress",
      });
    }

    const receiverAddress = decodeSparkAddress(
      receiverSparkAddress,
      this.config.getNetworkType(),
    );

    if (receiverAddress.sparkInvoiceFields) {
      throw new ValidationError(
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
        throw new ValidationError("Sats amount must be less than 2^53", {
          field: "amountSats",
          value: amountSats,
          expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
        });
      }
      if (amountSats <= 0) {
        throw new ValidationError("Amount must be greater than 0", {
          field: "amountSats",
          value: amountSats,
        });
      }
      amountSatsArray.push(amountSats);
    }

    const transferJobs = await this.withLeaves(async () => {
      const selectLeavesToSendMap: Map<number, TreeNode[][]> =
        await this.selectLeaves(amountSatsArray);

      for (const [amount, selection] of selectLeavesToSendMap) {
        for (let groupIndex = 0; groupIndex < selection.length; groupIndex++) {
          const group = selection[groupIndex];
          if (!group) {
            throw new ValidationError(
              `TreeNode group at index ${groupIndex} not found for amount ${amount} after selection`,
            );
          }
          let available = await this.checkRefreshTimelockNodes(group);
          available = await this.checkExtendTimeLockNodes(available);

          if (available.length < group.length) {
            throw new Error(
              `Not enough available nodes after refresh/extend. Expected ${group.length}, got ${available.length}`,
            );
          }
          selection[groupIndex] = available;
        }
      }

      const tweaksByAmount = this.buildTweaksByAmount(selectLeavesToSendMap);

      const idsToRemove = new Set<string>();
      const jobs = params.map((param) => {
        const { amountSats, receiverIdentityPubkey, sparkInvoice } = param;
        const leafKeyTweaks = this.popOrThrow(
          tweaksByAmount.get(amountSats),
          `no leaves key tweaks for ${amountSats}`,
        );

        for (const tweak of leafKeyTweaks) {
          idsToRemove.add(tweak.leaf.id);
        }
        return { leafKeyTweaks, receiverIdentityPubkey, sparkInvoice, param };
      });
      if (idsToRemove.size > 0) {
        this.leaves = this.leaves.filter((leaf) => !idsToRemove.has(leaf.id));
      }

      return jobs;
    });

    const signerIdentityPublicKey =
      await this.config.signer.getIdentityPublicKey();

    const outcomes = await Promise.all(
      transferJobs.map(async (job) => {
        try {
          const transfer = await this.transferService.sendTransferWithKeyTweaks(
            job.leafKeyTweaks,
            job.receiverIdentityPubkey,
            job.sparkInvoice,
          );
          const isSelfTransfer = equalBytes(
            signerIdentityPublicKey,
            job.receiverIdentityPubkey,
          );
          if (isSelfTransfer) {
            const pending = await this.transferService.queryTransfer(
              transfer.id,
            );
            if (pending) {
              await this.claimTransfer({ transfer: pending, optimize: true });
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
            throw new ValidationError(
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

  private async checkExtendTimeLockNodes(
    nodes: TreeNode[],
  ): Promise<TreeNode[]> {
    const nodesToExtend: TreeNode[] = [];
    const nodeIds: string[] = [];
    const validNodes: TreeNode[] = [];

    for (const node of nodes) {
      const nodeTx = getTxFromRawTxBytes(node.nodeTx);
      const sequence = nodeTx.getInput(0).sequence;
      if (!sequence) {
        throw new ValidationError("Invalid node transaction", {
          field: "sequence",
          value: nodeTx.getInput(0),
          expected: "Non-null sequence",
        });
      }
      const needsRefresh = doesLeafNeedRefresh(sequence, true);
      if (needsRefresh) {
        nodesToExtend.push(node);
        nodeIds.push(node.id);
      } else {
        validNodes.push(node);
      }
    }

    if (nodesToExtend.length === 0) {
      return validNodes;
    }

    const nodesToAdd: TreeNode[] = [];
    for (const node of nodesToExtend) {
      const { nodes } = await this.transferService.extendTimelock(node);
      this.leaves = this.leaves.filter((leaf) => leaf.id !== node.id);
      const newNodes = await this.transferLeavesToSelf(nodes, {
        type: KeyDerivationType.LEAF,
        path: node.id,
      });
      nodesToAdd.push(...newNodes);
    }

    this.updateLeaves(nodeIds, nodesToAdd);
    validNodes.push(...nodesToAdd);

    return validNodes;
  }

  /**
   * Internal method to refresh timelock nodes.
   *
   * @param {string} nodeId - The optional ID of the node to refresh. If not provided, all nodes will be checked.
   * @returns {Promise<void>}
   * @private
   */
  private async checkRefreshTimelockNodes(
    nodes: TreeNode[],
  ): Promise<TreeNode[]> {
    const nodesToRefresh: TreeNode[] = [];
    const nodeIds: string[] = [];
    const validNodes: TreeNode[] = [];

    for (const node of nodes) {
      const refundTx = getTxFromRawTxBytes(node.refundTx);
      const sequence = refundTx.getInput(0).sequence;
      if (!sequence) {
        throw new ValidationError("Invalid refund transaction", {
          field: "sequence",
          value: refundTx.getInput(0),
          expected: "Non-null sequence",
        });
      }
      const needsRefresh = doesLeafNeedRefresh(sequence);
      if (needsRefresh) {
        nodesToRefresh.push(node);
        nodeIds.push(node.id);
      } else {
        validNodes.push(node);
      }
    }

    if (nodesToRefresh.length === 0) {
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
    });

    const nodesMap = new Map<string, TreeNode>();
    for (const node of Object.values(nodesResp.nodes)) {
      nodesMap.set(node.id, node);
    }

    const nodesToAdd: TreeNode[] = [];
    for (const node of nodesToRefresh) {
      if (!node.parentNodeId) {
        throw new Error(`node ${node.id} has no parent`);
      }

      const parentNode = nodesMap.get(node.parentNodeId);
      if (!parentNode) {
        throw new Error(`parent node ${node.parentNodeId} not found`);
      }

      const { nodes } = await this.transferService.refreshTimelockNodes(
        node,
        parentNode,
      );

      if (nodes.length !== 1) {
        throw new Error(`expected 1 node, got ${nodes.length}`);
      }

      const newNode = nodes[0];
      if (!newNode) {
        throw new Error("Failed to refresh timelock node");
      }

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
    optimize?: boolean,
  ): Promise<TreeNode[]> {
    result = await this.checkRefreshTimelockNodes(result);
    result = await this.checkExtendTimeLockNodes(result);

    const existingIds = new Set(this.leaves.map((leaf) => leaf.id));
    const uniqueResults = result.filter((node) => !existingIds.has(node.id));
    this.leaves.push(...uniqueResults);

    if (optimize && transfer.type !== TransferType.COUNTER_SWAP) {
      await this.optimizeLeaves();
    }

    if (emit) {
      this.emit(
        "transfer:claimed",
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
    optimize,
  }: {
    transfer: Transfer;
    emit?: boolean;
    optimize?: boolean;
  }) {
    const onError = async (
      context: RetryContext<TreeNode[], Transfer>,
    ): Promise<TreeNode[] | undefined> => {
      const error = context.error;
      if (
        error instanceof RPCError &&
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

      return await this.processClaimedTransferResults(
        result,
        transfer,
        emit,
        optimize,
      );
    } catch (error) {
      console.warn(
        `Failed to claim transfer after all retries. Please try reinitializing your wallet in a few minutes. Transfer ID: ${transfer.id}`,
        error,
      );

      throw new NetworkError(
        "Failed to claim transfer",
        {
          operation: "claimTransfer",
          errors: error instanceof Error ? error.message : String(error),
        },
        error instanceof Error ? error : undefined,
      );
    }
  }

  /**
   * Claims all pending transfers.
   *
   * @returns {Promise<string[]>} Array of successfully claimed transfer IDs
   * @private
   */
  private async claimTransfers(
    type?: TransferType,
    emit?: boolean,
  ): Promise<string[]> {
    const transfers = await this.transferService.queryPendingTransfers();
    const promises: Promise<string | null>[] = [];
    for (const transfer of transfers.transfers) {
      if (type && transfer.type !== type) {
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
        this.claimTransfer({ transfer, emit, optimize: true })
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
   * @param {boolean} [params.includeSparkAddress] - Optional boolean signalling whether or not to include the spark address in the invoice
   * @param {string} [params.receiverIdentityPubkey] - Optional public key of the wallet receiving the lightning invoice. If not present, the receiver will be the creator of this request.
   * @param {string} [params.descriptionHash] - Optional h tag of the invoice. This is the hash of a longer description to include in the lightning invoice. It is used in LNURL and UMA as the hash of the metadata. This field is mutually exclusive with the memo field. Only one or the other should be provided.
   * @returns {Promise<LightningReceiveRequest>} BOLT11 encoded invoice
   */
  public async createLightningInvoice({
    amountSats,
    memo,
    expirySeconds = 60 * 60 * 24 * 30,
    includeSparkAddress = false,
    receiverIdentityPubkey,
    descriptionHash,
  }: CreateLightningInvoiceParams): Promise<LightningReceiveRequest> {
    const sspClient = this.getSspClient();

    if (isNaN(amountSats) || amountSats < 0) {
      throw new ValidationError("Invalid amount", {
        field: "amountSats",
        value: amountSats,
        expected: "non-negative number",
      });
    }

    if (!Number.isSafeInteger(amountSats)) {
      throw new ValidationError("Sats amount must be less than 2^53", {
        field: "amountSats",
        value: amountSats,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    if (!Number.isSafeInteger(expirySeconds)) {
      throw new ValidationError("Expiration time must be less than 2^53", {
        field: "expirySeconds",
        value: expirySeconds,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    if (expirySeconds < 0) {
      throw new ValidationError("Invalid expiration time", {
        field: "expirySeconds",
        value: expirySeconds,
        expected: "Non-negative expiration time",
      });
    }

    if (memo && memo.length > 639) {
      throw new ValidationError("Invalid memo size", {
        field: "memo",
        value: memo,
        expected: "Memo size within limits",
      });
    }

    if (memo && descriptionHash) {
      throw new ValidationError(
        "Memo and descriptionHash cannot be provided together. Please provide only one.",
        {
          field: "memo",
          value: memo,
          expected: "Memo or descriptionHash",
        },
      );
    }

    const requestLightningInvoice = async (
      amountSats: number,
      paymentHash: Uint8Array,
      memo?: string,
      receiverIdentityPubkey?: string,
      descriptionHash?: string,
    ) => {
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
        paymentHash: bytesToHex(paymentHash),
        expirySecs: expirySeconds,
        memo,
        includeSparkAddress: includeSparkAddress,
        receiverIdentityPubkey,
        descriptionHash,
      });

      if (!invoice) {
        throw new Error("Failed to create lightning invoice");
      }

      // Validate the spark address embedded in the lightning invoice
      if (includeSparkAddress) {
        const sparkFallbackAddress = decodeInvoice(
          invoice.invoice.encodedInvoice,
        ).fallbackAddress;

        if (!sparkFallbackAddress) {
          throw new ValidationError(
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
          throw new ValidationError(
            "Mismatch between spark identity embedded in lightning invoice and designated recipient spark identity",
            {
              field: "sparkFallbackAddress",
              value: sparkFallbackAddress,
              expected: expectedIdentityPubkey,
            },
          );
        }
      }

      return invoice;
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

  /**
   * Pays a Lightning invoice.
   *
   * @param {Object} params - Parameters for paying the invoice
   * @param {string} params.invoice - The BOLT11-encoded Lightning invoice to pay
   * @param {boolean} [params.preferSpark] - Whether to prefer a spark transfer over lightning for the payment
   * @param {number} [params.amountSatsToSend] - The amount in sats to send. This is only valid for 0 amount lightning invoices.
   * @returns {Promise<LightningSendRequest>} The Lightning payment request details
   */
  public async payLightningInvoice({
    invoice,
    maxFeeSats,
    preferSpark = false,
    amountSatsToSend,
  }: PayLightningInvoiceParams) {
    const invoiceNetwork = getNetworkFromInvoice(invoice);
    const walletNetwork = this.config.getNetwork();

    const isValidNetworkForWallet =
      invoiceNetwork === walletNetwork ||
      (invoiceNetwork === Network.REGTEST &&
        (walletNetwork === Network.REGTEST || walletNetwork === Network.LOCAL));

    if (!isValidNetworkForWallet) {
      throw new ValidationError(
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
      throw new ValidationError(
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
      throw new ValidationError(
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
      throw new ValidationError("Invalid amount", {
        field: "amountSats",
        value: amountSats,
        expected: "greater than 0",
      });
    }

    const sparkFallbackAddress = decodedInvoice.fallbackAddress;
    const paymentHash = decodedInvoice.paymentHash;

    // Pay over Spark
    if (preferSpark) {
      if (
        sparkFallbackAddress === undefined ||
        isValidSparkFallback(hexToBytes(sparkFallbackAddress)) === false
      ) {
        console.warn(
          "No valid spark address found in invoice. Defaulting to lightning.",
        );
      } else {
        const receiverSparkAddress = encodeSparkAddress({
          identityPublicKey: sparkFallbackAddress,
          network: Network[invoiceNetwork] as NetworkType,
        });
        return await this.transfer({
          amountSats,
          receiverSparkAddress,
        });
      }
    }

    // Pay over Lightning
    return await this.withLeaves(async () => {
      const sspClient = this.getSspClient();

      // If 0 amount lightning invoice, use amountSatsToSend for fee estimate
      const feeEstimate = await this.getLightningSendFeeEstimate({
        encodedInvoice: invoice,
        amountSats: isZeroAmountInvoice ? amountSatsToSend! : undefined,
      });

      if (maxFeeSats < feeEstimate) {
        throw new ValidationError("maxFeeSats does not cover fee estimate", {
          field: "maxFeeSats",
          value: maxFeeSats,
          expected: `${feeEstimate} sats`,
        });
      }

      const totalAmount = amountSats + feeEstimate;

      const internalBalance = this.getInternalBalance();
      if (totalAmount > internalBalance) {
        throw new ValidationError("Insufficient balance", {
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
      leaves = await this.checkRefreshTimelockNodes(leaves);
      leaves = await this.checkExtendTimeLockNodes(leaves);

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
      });

      if (!swapResponse.transfer) {
        throw new Error("Failed to swap nodes for preimage");
      }

      await this.transferService.deliverTransferPackage(
        swapResponse.transfer,
        leavesToSend,
        new Map(),
        new Map(),
        new Map(),
      );

      const sspResponse = await sspClient.requestLightningSend({
        encodedInvoice: invoice,
        idempotencyKey: uuidv7(),
        amountSats: isZeroAmountInvoice ? amountSatsToSend! : undefined,
      });

      if (!sspResponse) {
        throw new Error("Failed to contact SSP");
      }
      // test
      const leavesToRemove = new Set(leavesToSend.map((leaf) => leaf.leaf.id));
      this.leaves = this.leaves.filter((leaf) => !leavesToRemove.has(leaf.id));

      return sspResponse;
    });
  }

  public async fulfillSparkInvoice(
    sparkInvoices: {
      invoice: SparkAddressFormat;
      amount?: bigint;
    }[],
  ): Promise<FulfillSparkInvoiceResponse> {
    if (!Array.isArray(sparkInvoices) || sparkInvoices.length === 0) {
      throw new ValidationError("No Spark invoices provided", {
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
      invoice: string;
      error: Error;
    }[] = [];
    const tokenTransactionSuccess: {
      tokenIdentifier: Bech32mTokenIdentifier;
      txid: string;
    }[] = [];
    const tokenTransactionErrors: {
      tokenIdentifier: Bech32mTokenIdentifier;
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
        | { ok: true; tokenIdentifier: Bech32mTokenIdentifier; txid: string }
        | { ok: false; tokenIdentifier: Bech32mTokenIdentifier; error: Error }
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

        tokenTransferTasks.push(
          this.tokenTransactionService
            .tokenTransfer({ tokenOutputs: this.tokenOutputs, receiverOutputs })
            .then((txid) => ({
              ok: true as const,
              tokenIdentifier: tokenIdB32,
              txid,
            }))
            .catch((e: any) => ({
              ok: false as const,
              tokenIdentifier: tokenIdB32,
              error: e instanceof Error ? e : new Error(String(e)),
            })),
        );
      }
      const results = await Promise.all(tokenTransferTasks);
      for (const r of results) {
        if (r.ok) {
          tokenTransactionSuccess.push({
            tokenIdentifier: r.tokenIdentifier,
            txid: r.txid,
          });
        } else {
          tokenTransactionErrors.push({
            tokenIdentifier: r.tokenIdentifier,
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
      const addressData = decodeSparkAddress(
        invoice,
        this.config.getNetworkType(),
      );
      if (!addressData.sparkInvoiceFields) {
        invalidInvoices.push({
          invoice,
          error: new ValidationError("Missing invoice fields", {
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
            error: new ValidationError("Invoice expired", {
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
          error: new ValidationError("Sender public key mismatch", {
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
            error: new ValidationError("Invalid amount", {
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
            error: new ValidationError(
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
            error: new ValidationError(
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
            error: new ValidationError(
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
          error: new ValidationError("Invalid payment type", {
            field: "invoice",
            expected: "sats or tokens invoice",
          }),
        });
      }
    });
    return { satsInvoices, tokenInvoices, invalidInvoices };
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

    if (!feeEstimate) {
      throw new Error("Failed to get lightning send fee estimate");
    }
    const satsFeeEstimate = mapCurrencyAmount(feeEstimate.feeEstimate);
    return Math.ceil(satsFeeEstimate.sats);
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
    deductFeeFromWithdrawalAmount = true,
  }: {
    onchainAddress: string;
    exitSpeed: ExitSpeed;
    feeQuote: CoopExitFeeQuote;
    amountSats?: number;
    deductFeeFromWithdrawalAmount?: boolean;
  }) {
    if (!Number.isSafeInteger(amountSats)) {
      throw new ValidationError("Sats amount must be less than 2^53", {
        field: "amountSats",
        value: amountSats,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }
    return await this.withLeaves(async () => {
      return await this.coopExit(
        onchainAddress,
        feeQuote,
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
    feeEstimate: CoopExitFeeQuote,
    exitSpeed: ExitSpeed,
    deductFeeFromWithdrawalAmount: boolean,
    targetAmountSats?: number,
  ) {
    if (!Number.isSafeInteger(targetAmountSats)) {
      throw new ValidationError("Sats amount must be less than 2^53", {
        field: "targetAmountSats",
        value: targetAmountSats,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    if (!targetAmountSats) {
      deductFeeFromWithdrawalAmount = true;
    }

    let fee: number | undefined;
    switch (exitSpeed) {
      case ExitSpeed.FAST:
        fee =
          (feeEstimate.l1BroadcastFeeFast?.originalValue || 0) +
          (feeEstimate.userFeeFast?.originalValue || 0);
        break;
      case ExitSpeed.MEDIUM:
        fee =
          (feeEstimate.l1BroadcastFeeMedium?.originalValue || 0) +
          (feeEstimate.userFeeMedium?.originalValue || 0);
        break;
      case ExitSpeed.SLOW:
        fee =
          (feeEstimate.l1BroadcastFeeSlow?.originalValue || 0) +
          (feeEstimate.userFeeSlow?.originalValue || 0);
        break;
      default:
        throw new ValidationError("Invalid exit speed", {
          field: "exitSpeed",
          value: exitSpeed,
          expected: "FAST, MEDIUM, or SLOW",
        });
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

      if (fee > leavesToSendToSsp.reduce((acc, leaf) => acc + leaf.value, 0)) {
        throw new ValidationError(
          "The fee for the withdrawal is greater than the target withdrawal amount",
          {
            field: "fee",
            value: fee,
            expected: "less than or equal to the target amount",
          },
        );
      }
    } else {
      if (!targetAmountSats) {
        throw new ValidationError(
          "targetAmountSats is required when deductFeeFromWithdrawalAmount is false",
          {
            field: "targetAmountSats",
            value: targetAmountSats,
            expected: "defined when deductFeeFromWithdrawalAmount is false",
          },
        );
      }

      const leaves = await this.selectLeaves([targetAmountSats, fee]);

      const leavesForTargetAmount = this.popOrThrow(
        leaves.get(targetAmountSats)!,
        `failed to get leaves leaves for targetAmount, val: ${targetAmountSats}`,
      );
      const leavesForFee = this.popOrThrow(
        leaves.get(fee)!,
        `failed to get leaves leaves for fee, val: ${fee}`,
      );

      if (!leavesForTargetAmount || !leavesForFee) {
        throw new Error("Failed to select leaves for target amount and fee");
      }

      leavesToSendToSsp = leavesForTargetAmount;
      leavesToSendToSE = leavesForFee;
    }

    leavesToSendToSsp = await this.checkRefreshTimelockNodes(leavesToSendToSsp);
    leavesToSendToSsp = await this.checkExtendTimeLockNodes(leavesToSendToSsp);
    leavesToSendToSE = await this.checkRefreshTimelockNodes(leavesToSendToSE);
    leavesToSendToSE = await this.checkExtendTimeLockNodes(leavesToSendToSE);

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

    const requestCoopExitParams: RequestCoopExitInput = {
      leafExternalIds: leavesToSendToSsp.map((leaf) => leaf.id),
      withdrawalAddress: onchainAddress,
      idempotencyKey: uuidv7(),
      exitSpeed,
      withdrawAll: deductFeeFromWithdrawalAmount,
    };

    if (!deductFeeFromWithdrawalAmount) {
      requestCoopExitParams.feeQuoteId = feeEstimate.id;
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

    const coopExitTxId = connectorTx.getInput(0).txid;
    const connectorTxId = getTxId(connectorTx);

    if (!coopExitTxId) {
      throw new Error("Failed to get coop exit tx id");
    }

    const connectorOutputs: TransactionInput[] = [];
    for (let i = 0; i < connectorTx.outputsLength - 1; i++) {
      connectorOutputs.push({
        txid: hexToBytes(connectorTxId),
        index: i,
      });
    }

    const sspPubIdentityKey = hexToBytes(this.config.getSspIdentityPublicKey());
    const transfer = await this.coopExitService.getConnectorRefundSignatures({
      leaves: leafKeyTweaks,
      exitTxId: coopExitTxId,
      connectorOutputs,
      receiverPubKey: sspPubIdentityKey,
    });

    const completeResponse = await sspClient.completeCoopExit({
      userOutboundTransferExternalId: transfer.transfer.id,
      coopExitRequestId: coopExitRequest.id,
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
      throw new ValidationError("Sats amount must be less than 2^53", {
        field: "amountSats",
        value: amountSats,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    let leaves = this.popOrThrow(
      (await this.selectLeaves([amountSats])).get(amountSats)!,
      `no leaves for ${amountSats}`,
    );

    leaves = await this.checkRefreshTimelockNodes(leaves);
    leaves = await this.checkExtendTimeLockNodes(leaves);

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
            TransferType.COUNTER_SWAP,
            TransferType.PREIMAGE_SWAP,
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
   * Gets all transfers for the wallet.
   *
   * @param {number} [limit=20] - Maximum number of transfers to return
   * @param {number} [offset=0] - Offset for pagination
   * @returns {Promise<QueryTransfersResponse>} Response containing the list of transfers
   */
  public async getTransfers(
    limit: number = 20,
    offset: number = 0,
  ): Promise<{
    transfers: WalletTransfer[];
    offset: number;
  }> {
    const transfers = await this.transferService.queryAllTransfers(
      limit,
      offset,
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
   * @returns {Promise<void>}
   * @private
   */
  protected async syncTokenOutputs() {
    this.tokenOutputs.clear();

    const unsortedTokenOutputs =
      await this.tokenTransactionService.fetchOwnedTokenOutputs({
        ownerPublicKeys: [await this.config.signer.getIdentityPublicKey()],
      });
    const filteredTokenOutputs = unsortedTokenOutputs.filter(
      (output) =>
        !this.pendingWithdrawnOutputIds.includes(output.output?.id || ""),
    );

    const fetchedOutputIds = new Set(
      unsortedTokenOutputs.map((output) => output.output?.id).filter(Boolean),
    );
    this.pendingWithdrawnOutputIds = this.pendingWithdrawnOutputIds.filter(
      (id) => fetchedOutputIds.has(id),
    );

    // Group outputs by hex representation of raw token identifier bytes
    const groupedOutputs: TokenOutputsMap = new Map();

    filteredTokenOutputs.forEach((output) => {
      const bech32mTokenIdentifier = encodeBech32mTokenIdentifier({
        tokenIdentifier: output.output!.tokenIdentifier!,
        network: this.config.getNetworkType(),
      });
      const index = output.previousTransactionVout!;

      if (!groupedOutputs.has(bech32mTokenIdentifier)) {
        groupedOutputs.set(bech32mTokenIdentifier, []);
      }

      groupedOutputs.get(bech32mTokenIdentifier)!.push({
        ...output,
        previousTransactionVout: index,
      });
    });

    this.tokenOutputs = groupedOutputs;
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
      throw new ValidationError(
        "Spark address is a Spark invoice. Use fulfillSparkInvoice instead.",
        {
          field: "receiverSparkAddress",
          value: receiverSparkAddress,
        },
      );
    }

    await this.syncTokenOutputs();

    return this.tokenTransactionService.tokenTransfer({
      tokenOutputs: this.tokenOutputs,
      receiverOutputs: [
        {
          tokenIdentifier,
          tokenAmount,
          receiverSparkAddress,
        },
      ],
      outputSelectionStrategy: outputSelectionStrategy ?? "SMALL_FIRST",
      selectedOutputs,
    });
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
      throw new ValidationError("At least one receiver output is required", {
        field: "receiverOutputs",
        value: receiverOutputs,
        expected: "Non-empty array",
      });
    }
    const firstBech32mTokenIdentifier = receiverOutputs[0]!.tokenIdentifier;
    for (const output of receiverOutputs) {
      if (output.tokenIdentifier !== firstBech32mTokenIdentifier) {
        throw new ValidationError(
          "All receiver outputs must have the same token public key",
          {
            field: "receiverOutputs",
            value: receiverOutputs,
            expected: "All outputs must have the same token public key",
          },
        );
      }
      if (output.tokenAmount <= 0n) {
        throw new ValidationError("Token amount must be greater than 0", {
          field: "receiverOutputs",
          value: receiverOutputs,
          expected: "All outputs must have tokenAmount > 0",
        });
      }
    }

    await this.syncTokenOutputs();

    // replace bech32m encoded token identifier with raw token identifier bytes
    const transferOutputs = receiverOutputs.map((output) => ({
      tokenIdentifier: firstBech32mTokenIdentifier,
      tokenAmount: output.tokenAmount,
      receiverSparkAddress: output.receiverSparkAddress,
    }));

    return this.tokenTransactionService.tokenTransfer({
      tokenOutputs: this.tokenOutputs,
      receiverOutputs: transferOutputs,
      outputSelectionStrategy,
      selectedOutputs,
    });
  }

  /**
   * Retrieves token transaction history for specified tokens
   * Can optionally filter by specific transaction hashes.
   *
   * @param ownerPublicKeys - Optional array of owner public keys to query transactions for
   * @param issuerPublicKeys - Optional array of issuer public keys to query transactions for
   * @param tokenTransactionHashes - Optional array of specific transaction hashes to filter by
   * @param tokenIdentifiers - Optional array of token identifiers to filter by
   * @param outputIds - Optional array of output IDs to filter by
   * @returns Promise resolving to array of token transactions with their current status
   */

  public async queryTokenTransactions({
    ownerPublicKeys,
    issuerPublicKeys,
    tokenTransactionHashes,
    tokenIdentifiers,
    outputIds,
    pageSize,
    offset,
  }: {
    ownerPublicKeys?: string[];
    issuerPublicKeys?: string[];
    tokenTransactionHashes?: string[];
    tokenIdentifiers?: string[];
    outputIds?: string[];
    pageSize?: number;
    offset?: number;
  }): Promise<QueryTokenTransactionsResponse> {
    return this.tokenTransactionService.queryTokenTransactions({
      ownerPublicKeys,
      issuerPublicKeys,
      tokenTransactionHashes,
      tokenIdentifiers,
      outputIds,
      pageSize: pageSize ?? 100,
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
            throw new ValidationError(`Failed to sign input ${i}: ${error}`, {
              field: "input",
              value: i,
            });
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
        throw new ValidationError("Node not found", {
          field: "nodeId",
          value: nodeId,
        });
      }

      // Check if this is a root node (no parent)
      const isRootNode = !node.parentNodeId;

      // Validate transaction data exists
      if (!node.nodeTx || node.nodeTx.length === 0) {
        throw new ValidationError(
          `Node transaction data is missing or empty for ${isRootNode ? "root" : "non-root"} node`,
          {
            field: "nodeTx",
            value: node.nodeTx?.length || 0,
          },
        );
      }

      if (!node.refundTx || node.refundTx.length === 0) {
        throw new ValidationError(
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
        throw new ValidationError(
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
        throw new ValidationError(
          `Failed to parse refund transaction for ${isRootNode ? "root" : "non-root"} node: ${error instanceof Error ? error.message : String(error)}`,
          {
            field: "refundTx",
            value: node.refundTx.length,
          },
        );
      }

      const nodeInput = nodeTx.getInput(0);
      if (!nodeInput) {
        throw new ValidationError(
          `Node transaction has no inputs for ${isRootNode ? "root" : "non-root"} node`,
          {
            field: "nodeInput",
            value: nodeTx.inputsLength,
          },
        );
      }

      if (!nodeInput.sequence) {
        throw new ValidationError(
          `Node transaction has no sequence for ${isRootNode ? "root" : "non-root"} node`,
          {
            field: "sequence",
            value: nodeInput.sequence,
          },
        );
      }

      const refundInput = refundTx.getInput(0);
      if (!refundInput) {
        throw new ValidationError(
          `Refund transaction has no inputs for ${isRootNode ? "root" : "non-root"} node`,
          {
            field: "refundInput",
            value: refundTx.inputsLength,
          },
        );
      }

      if (!refundInput.sequence) {
        throw new ValidationError(
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
      throw new NetworkError(
        `Failed to check timelock for node ${nodeId}`,
        {
          method: "query_nodes",
        },
        error as Error,
      );
    }
  }

  /**
   * Refresh the timelock of a specific node.
   *
   * @param {string} nodeId - The ID of the node to refresh
   * @returns {Promise<void>} Promise that resolves when the timelock is refreshed
   */
  public async testOnly_expireTimelock(nodeId: string): Promise<void> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    try {
      // First, get the node and its parent
      const response = await sparkClient.query_nodes({
        source: {
          $case: "nodeIds",
          nodeIds: {
            nodeIds: [nodeId],
          },
        },
        includeParents: true,
      });

      let leaf = response.nodes[nodeId];
      if (!leaf) {
        throw new ValidationError("Node not found", {
          field: "nodeId",
          value: nodeId,
        });
      }

      let parentNode;
      let hasParentNode = false;
      if (!leaf.parentNodeId) {
        // skip node timelock check
      } else {
        hasParentNode = true;
        parentNode = response.nodes[leaf.parentNodeId];
        if (!parentNode) {
          throw new ValidationError("Parent node not found", {
            field: "parentNodeId",
            value: leaf.parentNodeId,
          });
        }
      }

      const nodeTx = getTxFromRawTxBytes(leaf.nodeTx);
      const refundTx = getTxFromRawTxBytes(leaf.refundTx);

      if (hasParentNode) {
        const nodeTimelock = getCurrentTimelock(nodeTx.getInput(0).sequence);
        if (nodeTimelock > 100) {
          const expiredNodeTxLeaf =
            await this.transferService.testonly_expireTimeLockNodeTx(
              leaf,
              parentNode,
            );

          if (!expiredNodeTxLeaf.nodes[0]) {
            throw new ValidationError("No expired node tx leaf", {
              field: "expiredNodeTxLeaf",
              value: expiredNodeTxLeaf,
            });
          }
          leaf = expiredNodeTxLeaf.nodes[0];
        }
      }
      const refundTimelock = getCurrentTimelock(refundTx.getInput(0).sequence);

      if (refundTimelock > 100) {
        const expiredTxLeaf =
          await this.transferService.testonly_expireTimeLockRefundtx(leaf);

        if (!expiredTxLeaf.nodes[0]) {
          throw new ValidationError("No expired tx leaf", {
            field: "expiredTxLeaf",
            value: expiredTxLeaf,
          });
        }
        leaf = expiredTxLeaf.nodes[0];
      }

      // Update the local leaves if this node is in our wallet
      const leafIndex = this.leaves.findIndex((leaf) => leaf.id === leaf.id);
      if (leafIndex !== -1) {
        this.leaves[leafIndex] = leaf;
      }
    } catch (error) {
      throw new NetworkError(
        "Failed to refresh timelock",
        {
          method: "refresh_timelock",
        },
        error as Error,
      );
    }
  }

  private cleanup() {
    if (this.claimTransfersInterval) {
      clearInterval(this.claimTransfersInterval);
      this.claimTransfersInterval = null;
    }
    this.streamController?.abort();
    this.removeAllListeners();
  }

  public async cleanupConnections() {
    this.cleanup();
    await this.connectionManager.closeConnections();
  }

  // Add this new method to start periodic claiming
  private startPeriodicClaimTransfers() {
    // Clear any existing interval first
    if (this.claimTransfersInterval) {
      clearInterval(this.claimTransfersInterval);
    }

    // Set up new interval to claim transfers every 5 seconds
    // @ts-ignore
    this.claimTransfersInterval = setInterval(async () => {
      try {
        await this.claimTransfers(undefined, true);
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
      console.log("OpenTelemetry client logging enabled.");
      spanProcessors.push(new SimpleSpanProcessor(new ConsoleSpanExporter()));
    }
    const traceUrls = this.getOtelTraceUrls();
    wallet.initializeTracerEnv({ spanProcessors, traceUrls });
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

  protected wrapWithOtelSpan<A extends unknown[], R>(
    name: string,
    fn: (...args: A) => Promise<R>,
  ) {
    return async (...args: A) => {
      if (!this.tracer) {
        throw new Error("Tracer not initialized");
      }

      return await this.tracer.startActiveSpan(name, async (span) => {
        const traceId = span.spanContext().traceId;
        try {
          const result = await fn(...args);
          return result;
        } catch (error) {
          if (error instanceof Error) {
            error.message += ` [traceId: ${traceId}]`;
          } else if (isObject(error)) {
            error["traceId"] = traceId;
          }
          throw error;
        } finally {
          span.end();
        }
      });
    };
  }

  protected getTraceName(methodName: string) {
    return `SparkWallet.${methodName}`;
  }

  private wrapPublicSparkWalletMethodWithOtelSpan<M extends keyof SparkWallet>(
    methodName: M,
  ) {
    const original = this[methodName];

    if (typeof original !== "function") {
      throw new Error(`Method ${methodName} is not a function on SparkWallet.`);
    }

    const wrapped = this.wrapWithOtelSpan(
      this.getTraceName(methodName),
      original.bind(this) as (...args: unknown[]) => Promise<unknown>,
    ) as SparkWallet[M];

    (this as SparkWallet)[methodName] = wrapped;
  }

  private wrapSparkWalletMethodsWithTracing() {
    const methods = [
      "getLeaves",
      "getIdentityPublicKey",
      "getSparkAddress",
      "createSatsInvoice",
      "createTokensInvoice",
      "getSwapFeeEstimate",
      "getTransfers",
      "getBalance",
      "getSingleUseDepositAddress",
      "getStaticDepositAddress",
      "queryStaticDepositAddresses",
      "getClaimStaticDepositQuote",
      "claimStaticDeposit",
      "refundStaticDeposit",
      "getUnusedDepositAddresses",
      "getUtxosForDepositAddress",
      "claimDeposit",
      "advancedDeposit",
      "transfer",
      "createLightningInvoice",
      "payLightningInvoice",
      "getLightningSendFeeEstimate",
      "withdraw",
      "getWithdrawalFeeQuote",
      "getTransferFromSsp",
      "getTransfer",
      "transferTokens",
      "batchTransferTokens",
      "queryTokenTransactions",
      "getLightningReceiveRequest",
      "getLightningSendRequest",
      "getCoopExitRequest",
      "checkTimelock",
      "testOnly_expireTimelock",
    ] as const;

    methods.forEach((m) => this.wrapPublicSparkWalletMethodWithOtelSpan(m));

    /* Private methods can't be indexed on `this` and need to be wrapped individually: */
    this.initWallet = this.wrapWithOtelSpan(
      this.getTraceName("initWallet"),
      this.initWallet.bind(this),
    );
  }
}
