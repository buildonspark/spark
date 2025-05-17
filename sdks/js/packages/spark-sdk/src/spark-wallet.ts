import { createLrc20ConnectionManager } from "@buildonspark/lrc20-sdk/grpc";
import { ILrc20ConnectionManager } from "@buildonspark/lrc20-sdk/grpc/types";
import { mapCurrencyAmount } from "@lightsparkdev/core";
import {
  bytesToHex,
  bytesToNumberBE,
  equalBytes,
  hexToBytes,
} from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { validateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { sha256 } from "@scure/btc-signer/utils";
import { Mutex } from "async-mutex";
import { decode } from "light-bolt11-decoder";
import { uuidv7 } from "uuidv7";
import {
  ConfigurationError,
  NetworkError,
  RPCError,
  ValidationError,
} from "./errors/types.js";

import SspClient from "./graphql/client.js";
import {
  BitcoinNetwork,
  CoopExitFeeEstimatesOutput,
  CoopExitRequest,
  ExitSpeed,
  LeavesSwapFeeEstimateOutput,
  LeavesSwapRequest,
  LightningReceiveRequest,
  LightningSendFeeEstimateInput,
  LightningSendRequest,
  UserLeafInput,
} from "./graphql/objects/index.js";
import {
  DepositAddressQueryResult,
  OutputWithPreviousTransactionData,
  QueryTransfersResponse,
  SubscribeToEventsResponse,
  TokenTransactionWithStatus,
  Transfer,
  TransferStatus,
  TransferType,
  TreeNode,
} from "./proto/spark.js";
import { WalletConfigService } from "./services/config.js";
import { ConnectionManager } from "./services/connection.js";
import { CoopExitService } from "./services/coop-exit.js";
import { DepositService } from "./services/deposit.js";
import { LightningService } from "./services/lightning.js";
import { TokenTransactionService } from "./services/token-transactions.js";
import { LeafKeyTweak, TransferService } from "./services/transfer.js";
import {
  DepositAddressTree,
  TreeCreationService,
} from "./services/tree-creation.js";
import {
  ConfigOptions,
  ELECTRS_CREDENTIALS,
} from "./services/wallet-config.js";
import {
  applyAdaptorToSignature,
  generateAdaptorFromSignature,
  generateSignatureFromExistingAdaptor,
} from "./utils/adaptor-signature.js";
import {
  computeTaprootKeyNoScript,
  getP2WPKHAddressFromPublicKey,
  getSigHashFromTx,
  getTxFromRawTxBytes,
  getTxFromRawTxHex,
  getTxId,
} from "./utils/bitcoin.js";
import {
  getNetwork,
  LRC_WALLET_NETWORK,
  LRC_WALLET_NETWORK_TYPE,
  Network,
} from "./utils/network.js";
import {
  calculateAvailableTokenAmount,
  checkIfSelectedOutputsAreAvailable,
} from "./utils/token-transactions.js";
import { getNextTransactionSequence } from "./utils/transaction.js";

import { LRCWallet } from "@buildonspark/lrc20-sdk";
import { EventEmitter } from "eventemitter3";
import {
  decodeSparkAddress,
  encodeSparkAddress,
  SparkAddressFormat,
} from "./address/index.js";
import { isReactNative } from "./constants.js";
import { SparkSigner } from "./signer/signer.js";
import { BitcoinFaucet } from "./tests/utils/test-faucet.js";
import {
  mapTransferToWalletTransfer,
  mapTreeNodeToWalletLeaf,
  WalletLeaf,
  WalletTransfer,
} from "./types/sdk-types.js";
import { getCrypto } from "./utils/crypto.js";
import { getMasterHDKeyFromSeed } from "./utils/index.js";
const crypto = getCrypto();

// Add this constant at the file level
const MAX_TOKEN_OUTPUTS = 100;

export type CreateLightningInvoiceParams = {
  amountSats: number;
  memo?: string;
  expirySeconds?: number;
};

export type PayLightningInvoiceParams = {
  invoice: string;
  maxFeeSats: number;
};

export type TransferParams = {
  amountSats: number;
  receiverSparkAddress: string;
};

type DepositParams = {
  signingPubKey: Uint8Array;
  verifyingKey: Uint8Array;
  depositTx: Transaction;
  vout: number;
};

export type TokenInfo = {
  tokenPublicKey: string;
  tokenName: string;
  tokenSymbol: string;
  tokenDecimals: number;
  maxSupply: bigint;
};

export type InitWalletResponse = {
  mnemonic?: string | undefined;
};
export interface SparkWalletProps {
  mnemonicOrSeed?: Uint8Array | string;
  accountNumber?: number;
  signer?: SparkSigner;
  options?: ConfigOptions;
}

export interface SparkWalletEvents {
  /** Emitted when an incoming transfer is successfully claimed. Includes the transfer ID and new total balance. */
  "transfer:claimed": (transferId: string, updatedBalance: number) => void;
  /** Emitted when a deposit is marked as available. Includes the deposit ID and new total balance. */
  "deposit:confirmed": (depositId: string, updatedBalance: number) => void;
  /** Emitted when the stream is connected */
  "stream:connected": () => void;
  /** Emitted when the stream disconnects and fails to reconnect after max attempts */
  "stream:disconnected": (reason: string) => void;
  /** Emitted when attempting to reconnect the stream */
  "stream:reconnecting": (
    attempt: number,
    maxAttempts: number,
    delayMs: number,
    error: string,
  ) => void;
}

/**
 * The SparkWallet class is the primary interface for interacting with the Spark network.
 * It provides methods for creating and managing wallets, handling deposits, executing transfers,
 * and interacting with the Lightning Network.
 */
export class SparkWallet extends EventEmitter {
  protected config: WalletConfigService;

  protected connectionManager: ConnectionManager;
  protected lrc20ConnectionManager: ILrc20ConnectionManager;
  protected lrc20Wallet: LRCWallet | undefined;

  private depositService: DepositService;
  protected transferService: TransferService;
  private treeCreationService: TreeCreationService;
  private lightningService: LightningService;
  private coopExitService: CoopExitService;
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
  protected tokenOuputs: Map<string, OutputWithPreviousTransactionData[]> =
    new Map();

  // Add this property near the top of the class with other private properties
  private claimTransfersInterval: NodeJS.Timeout | null = null;

  protected constructor(options?: ConfigOptions, signer?: SparkSigner) {
    super();

    this.config = new WalletConfigService(options, signer);
    this.connectionManager = new ConnectionManager(this.config);
    this.lrc20ConnectionManager = createLrc20ConnectionManager(
      this.config.getLrc20Address(),
    );
    this.depositService = new DepositService(
      this.config,
      this.connectionManager,
    );
    this.transferService = new TransferService(
      this.config,
      this.connectionManager,
    );
    this.treeCreationService = new TreeCreationService(
      this.config,
      this.connectionManager,
    );
    this.tokenTransactionService = new TokenTransactionService(
      this.config,
      this.connectionManager,
    );
    this.lightningService = new LightningService(
      this.config,
      this.connectionManager,
    );
    this.coopExitService = new CoopExitService(
      this.config,
      this.connectionManager,
    );
  }

  public static async initialize({
    mnemonicOrSeed,
    accountNumber,
    signer,
    options,
  }: SparkWalletProps) {
    const wallet = new SparkWallet(options, signer);
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
          await this.claimTransfer(event.transfer.transfer, true);
        }
      } else if (event?.$case === "deposit" && event.deposit.deposit) {
        const deposit = event.deposit.deposit;
        const signingKey = await this.config.signer.generatePublicKey(
          sha256(deposit.id),
        );

        const newLeaf = await this.transferService.extendTimelock(
          deposit,
          signingKey,
        );
        await this.transferLeavesToSelf(newLeaf.nodes, signingKey);
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
        const sparkClient = await this.connectionManager.createSparkClient(
          this.config.getCoordinatorAddress(),
        );

        const stream = sparkClient.subscribe_to_events(
          {
            identityPublicKey: await this.config.signer.getIdentityPublicKey(),
          },
          {
            signal: this.streamController?.signal,
          },
        );

        const claimedTransfersIds = await this.claimTransfers();

        try {
          for await (const data of stream) {
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

  private async getLeaves(): Promise<TreeNode[]> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );
    const leaves = await sparkClient.query_nodes({
      source: {
        $case: "ownerIdentityPubkey",
        ownerIdentityPubkey: await this.config.signer.getIdentityPublicKey(),
      },
      includeParents: false,
    });

    return Object.entries(leaves.nodes)
      .filter(([_, node]) => node.status === "AVAILABLE")
      .map(([_, node]) => node);
  }

  private async selectLeaves(targetAmount: number): Promise<TreeNode[]> {
    if (targetAmount <= 0) {
      throw new ValidationError("Target amount must be positive", {
        field: "targetAmount",
        value: targetAmount,
      });
    }

    const leaves = await this.getLeaves();
    if (leaves.length === 0) {
      throw new ValidationError("No owned leaves found", {
        field: "leaves",
      });
    }

    leaves.sort((a, b) => b.value - a.value);

    let amount = 0;
    let nodes: TreeNode[] = [];
    for (const leaf of leaves) {
      if (targetAmount - amount >= leaf.value) {
        amount += leaf.value;
        nodes.push(leaf);
      }
    }

    if (amount !== targetAmount) {
      await this.requestLeavesSwap({ targetAmount });

      amount = 0;
      nodes = [];
      const newLeaves = await this.getLeaves();
      newLeaves.sort((a, b) => b.value - a.value);
      for (const leaf of newLeaves) {
        if (targetAmount - amount >= leaf.value) {
          amount += leaf.value;
          nodes.push(leaf);
        }
      }
    }

    if (nodes.reduce((acc, leaf) => acc + leaf.value, 0) !== targetAmount) {
      throw new Error(
        `Failed to select leaves for target amount ${targetAmount}`,
      );
    }

    return nodes;
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
    this.leaves = await this.getLeaves();
    await this.config.signer.restoreSigningKeysFromLeafs(this.leaves);
    await this.checkRefreshTimelockNodes();
    await this.checkExtendTimeLockNodes();
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
   * Initializes the wallet using either a mnemonic phrase or a raw seed.
   * initWallet will also claim any pending incoming lightning payment, spark transfer,
   * or bitcoin deposit.
   *
   * @param {Uint8Array | string} [mnemonicOrSeed] - (Optional) Either:
   *   - A BIP-39 mnemonic phrase as string
   *   - A raw seed as Uint8Array or hex string
   *   If not provided, generates a new mnemonic and uses it to create a new wallet
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
    if (accountNumber === 0 || accountNumber === 1) {
      // Reserved values for the case where no account number is provided
      throw new ValidationError(
        "If an account number is provided, it must not be be 0 or 1",
        {
          field: "accountNumber",
          value: accountNumber,
          expected: "values that do not equal 0 or 1",
        },
      );
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

    const network = this.config.getNetwork();
    // TODO: remove this once we move it back to the signer
    if (typeof seed === "string") {
      seed = hexToBytes(seed);
    }

    const hdkey = getMasterHDKeyFromSeed(seed);

    if (!hdkey.privateKey || !hdkey.publicKey) {
      throw new ValidationError("Failed to derive keys from seed", {
        field: "hdkey",
        value: seed,
      });
    }
    const accountNetwork = network === Network.REGTEST ? 0 : 1;
    const identityKey = hdkey.derive(
      `m/8797555'/${accountNumber ?? accountNetwork}'/0'`, // When an accountNumber is not provided, set a value based on the network
    );
    this.lrc20Wallet = new LRCWallet(
      bytesToHex(identityKey.privateKey!),
      LRC_WALLET_NETWORK[network],
      LRC_WALLET_NETWORK_TYPE[network],
      this.config.lrc20ApiConfig,
    );

    return {
      mnemonic,
    };
  }

  /**
   * Initializes a wallet from a seed.
   *
   * @param {Uint8Array | string} seed - The seed to initialize the wallet from
   * @returns {Promise<string>} The identity public key
   * @private
   */
  private async initWalletFromSeed(
    seed: Uint8Array | string,
    accountNumber?: number,
  ) {
    const identityPublicKey =
      await this.config.signer.createSparkWalletFromSeed(
        seed,
        this.config.getNetwork(),
        accountNumber,
      );
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
    if (!this.sspClient) {
      throw new ConfigurationError("SSP client not initialized", {
        configKey: "sspClient",
      });
    }

    const feeEstimate = await this.sspClient.getSwapFeeEstimate(amountSats);
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
    targetAmount,
    leaves,
  }: {
    targetAmount?: number;
    leaves?: TreeNode[];
  }) {
    if (targetAmount && targetAmount <= 0) {
      throw new Error("targetAmount must be positive");
    }

    if (targetAmount && !Number.isSafeInteger(targetAmount)) {
      throw new ValidationError("targetAmount must be less than 2^53", {
        field: "targetAmount",
        value: targetAmount,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    try {
      await this.claimTransfers();
    } catch (e) {
      console.warn("Unabled to claim transfers.");
    }

    let leavesToSwap: TreeNode[];
    if (targetAmount && leaves && leaves.length > 0) {
      if (targetAmount < leaves.reduce((acc, leaf) => acc + leaf.value, 0)) {
        throw new Error("targetAmount is less than the sum of leaves");
      }
      leavesToSwap = leaves;
    } else if (targetAmount) {
      leavesToSwap = await this.selectLeavesForSwap(targetAmount);
    } else if (leaves && leaves.length > 0) {
      leavesToSwap = leaves;
    } else {
      throw new Error("targetAmount or leaves must be provided");
    }

    const leafKeyTweaks = await Promise.all(
      leavesToSwap.map(async (leaf) => ({
        leaf,
        signingPubKey: await this.config.signer.generatePublicKey(
          sha256(leaf.id),
        ),
        newSigningPubKey: await this.config.signer.generatePublicKey(),
      })),
    );

    const { transfer, signatureMap } =
      await this.transferService.startSwapSignRefund(
        leafKeyTweaks,
        hexToBytes(this.config.getSspIdentityPublicKey()),
        new Date(Date.now() + 2 * 60 * 1000),
      );
    try {
      if (!transfer.leaves[0]?.leaf) {
        throw new Error("Failed to get leaf");
      }

      const refundSignature = signatureMap.get(transfer.leaves[0].leaf.id);
      if (!refundSignature) {
        throw new Error("Failed to get refund signature");
      }

      const { adaptorPrivateKey, adaptorSignature } =
        generateAdaptorFromSignature(refundSignature);

      if (!transfer.leaves[0].leaf) {
        throw new Error("Failed to get leaf");
      }

      const userLeaves: UserLeafInput[] = [];
      userLeaves.push({
        leaf_id: transfer.leaves[0].leaf.id,
        raw_unsigned_refund_transaction: bytesToHex(
          transfer.leaves[0].intermediateRefundTx,
        ),
        adaptor_added_signature: bytesToHex(adaptorSignature),
      });

      for (let i = 1; i < transfer.leaves.length; i++) {
        const leaf = transfer.leaves[i];
        if (!leaf?.leaf) {
          throw new Error("Failed to get leaf");
        }

        const refundSignature = signatureMap.get(leaf.leaf.id);
        if (!refundSignature) {
          throw new Error("Failed to get refund signature");
        }

        const signature = generateSignatureFromExistingAdaptor(
          refundSignature,
          adaptorPrivateKey,
        );

        userLeaves.push({
          leaf_id: leaf.leaf.id,
          raw_unsigned_refund_transaction: bytesToHex(
            leaf.intermediateRefundTx,
          ),
          adaptor_added_signature: bytesToHex(signature),
        });
      }

      const adaptorPubkey = bytesToHex(
        secp256k1.getPublicKey(adaptorPrivateKey),
      );
      let request: LeavesSwapRequest | null | undefined = null;
      request = await this.sspClient?.requestLeaveSwap({
        userLeaves,
        adaptorPubkey,
        targetAmountSats:
          targetAmount ||
          leavesToSwap.reduce((acc, leaf) => acc + leaf.value, 0),
        totalAmountSats: leavesToSwap.reduce(
          (acc, leaf) => acc + leaf.value,
          0,
        ),
        // TODO: Request fee from SSP
        feeSats: 0,
        idempotencyKey: uuidv7(),
      });

      if (!request) {
        throw new Error("Failed to request leaves swap. No response returned.");
      }

      const sparkClient = await this.connectionManager.createSparkClient(
        this.config.getCoordinatorAddress(),
      );

      const nodes = await sparkClient.query_nodes({
        source: {
          $case: "nodeIds",
          nodeIds: {
            nodeIds: request.swapLeaves.map((leaf) => leaf.leafId),
          },
        },
        includeParents: false,
      });

      if (Object.values(nodes.nodes).length !== request.swapLeaves.length) {
        throw new Error("Expected same number of nodes as swapLeaves");
      }

      for (const [nodeId, node] of Object.entries(nodes.nodes)) {
        if (!node.nodeTx) {
          throw new Error(`Node tx not found for leaf ${nodeId}`);
        }

        if (!node.verifyingPublicKey) {
          throw new Error(`Node public key not found for leaf ${nodeId}`);
        }

        const leaf = request.swapLeaves.find((leaf) => leaf.leafId === nodeId);
        if (!leaf) {
          throw new Error(`Leaf not found for node ${nodeId}`);
        }

        // @ts-ignore - We do a null check above
        const nodeTx = getTxFromRawTxBytes(node.nodeTx);
        const refundTxBytes = hexToBytes(leaf.rawUnsignedRefundTransaction);
        const refundTx = getTxFromRawTxBytes(refundTxBytes);
        const sighash = getSigHashFromTx(refundTx, 0, nodeTx.getOutput(0));

        const nodePublicKey = node.verifyingPublicKey;

        const taprootKey = computeTaprootKeyNoScript(nodePublicKey.slice(1));
        const adaptorSignatureBytes = hexToBytes(leaf.adaptorSignedSignature);
        applyAdaptorToSignature(
          taprootKey.slice(1),
          sighash,
          adaptorSignatureBytes,
          adaptorPrivateKey,
        );
      }

      await this.transferService.sendTransferTweakKey(
        transfer,
        leafKeyTweaks,
        signatureMap,
      );

      const completeResponse = await this.sspClient?.completeLeaveSwap({
        adaptorSecretKey: bytesToHex(adaptorPrivateKey),
        userOutboundTransferExternalId: transfer.id,
        leavesSwapRequestId: request.id,
      });

      if (!completeResponse) {
        throw new Error("Failed to complete leaves swap");
      }

      await this.claimTransfers(TransferType.COUNTER_SWAP);

      return completeResponse;
    } catch (e) {
      await this.cancelAllSenderInitiatedTransfers();
      throw new Error(`Failed to request leaves swap: ${e}`);
    }
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
    const identityPublicKey = bytesToHex(
      await this.config.signer.getIdentityPublicKey(),
    );
    return {
      transfers: transfers.transfers.map((transfer) =>
        mapTransferToWalletTransfer(transfer, identityPublicKey),
      ),
      offset: transfers.offset,
    };
  }

  /**
   * Gets the held token info for the wallet.
   *
   * @deprecated The information is returned in getBalance
   */
  public async getTokenInfo(): Promise<TokenInfo[]> {
    console.warn("getTokenInfo is deprecated. Use getBalance instead.");

    await this.syncTokenOutputs();

    const lrc20Client = await this.lrc20ConnectionManager.createLrc20Client();
    const { balance, tokenBalances } = await this.getBalance();

    const tokenInfo = await lrc20Client.getTokenPubkeyInfo({
      publicKeys: Array.from(tokenBalances.keys()).map(hexToBytes),
    });

    return tokenInfo.tokenPubkeyInfos.map((info) => ({
      tokenPublicKey: bytesToHex(info.announcement!.publicKey!.publicKey),
      tokenName: info.announcement!.name,
      tokenSymbol: info.announcement!.symbol,
      tokenDecimals: Number(bytesToNumberBE(info.announcement!.decimal)),
      maxSupply: bytesToNumberBE(info.announcement!.maxSupply),
    }));
  }

  /**
   * Gets the current balance of the wallet.
   * You can use the forceRefetch option to synchronize your wallet and claim any
   * pending incoming lightning payment, spark transfer, or bitcoin deposit before returning the balance.
   *
   * @returns {Promise<Object>} Object containing:
   *   - balance: The wallet's current balance in satoshis
   *   - tokenBalances: Map of token public keys to token balances and token info
   */
  public async getBalance(): Promise<{
    balance: bigint;
    tokenBalances: Map<string, { balance: bigint; tokenInfo: TokenInfo }>;
  }> {
    this.leaves = await this.getLeaves();
    await this.syncTokenOutputs();

    let tokenBalances: Map<string, { balance: bigint; tokenInfo: TokenInfo }>;

    if (this.tokenOuputs.size !== 0) {
      tokenBalances = await this.getTokenBalance();
    } else {
      tokenBalances = new Map<
        string,
        { balance: bigint; tokenInfo: TokenInfo }
      >();
    }

    return {
      balance: BigInt(this.getInternalBalance()),
      tokenBalances,
    };
  }

  private async getTokenBalance(): Promise<
    Map<string, { balance: bigint; tokenInfo: TokenInfo }>
  > {
    const lrc20Client = await this.lrc20ConnectionManager.createLrc20Client();

    // Get token info for all tokens
    const tokenInfo = await lrc20Client.getTokenPubkeyInfo({
      publicKeys: Array.from(this.tokenOuputs.keys()).map(hexToBytes),
    });

    const result = new Map<string, { balance: bigint; tokenInfo: TokenInfo }>();

    for (const info of tokenInfo.tokenPubkeyInfos) {
      const tokenPublicKey = bytesToHex(
        info.announcement!.publicKey!.publicKey,
      );
      const leaves = this.tokenOuputs.get(tokenPublicKey);

      result.set(tokenPublicKey, {
        balance: leaves ? calculateAvailableTokenAmount(leaves) : BigInt(0),
        tokenInfo: {
          tokenPublicKey,
          tokenName: info.announcement!.name,
          tokenSymbol: info.announcement!.symbol,
          tokenDecimals: Number(bytesToNumberBE(info.announcement!.decimal)),
          maxSupply: bytesToNumberBE(info.announcement!.maxSupply),
        },
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
   * Generates a deposit address for receiving funds.
   *
   * @returns {Promise<string>} A deposit address
   * @private
   */
  private async generateDepositAddress(): Promise<string> {
    const leafId = uuidv7();
    const signingPubkey = await this.config.signer.generatePublicKey(
      sha256(leafId),
    );
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

  /**
   * Finalizes a deposit to the wallet.
   *
   * @param {DepositParams} params - Parameters for finalizing the deposit
   * @returns {Promise<void>} The nodes created from the deposit
   * @private
   */
  private async finalizeDeposit({
    signingPubKey,
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
      signingPubKey,
      verifyingKey,
      depositTx,
      vout,
    });

    const resultingNodes: TreeNode[] = [];
    for (const node of res.nodes) {
      if (node.status === "AVAILABLE") {
        const { nodes } = await this.transferService.extendTimelock(
          node,
          signingPubKey,
        );

        for (const n of nodes) {
          if (n.status === "AVAILABLE") {
            const transfer = await this.transferLeavesToSelf(
              [n],
              signingPubKey,
            );
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
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );
    return (
      await sparkClient.query_unused_deposit_addresses({
        identityPublicKey: await this.config.signer.getIdentityPublicKey(),
      })
    ).depositAddresses.map((addr) => addr.depositAddress);
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
      const baseUrl = this.config.getElectrsUrl();
      const headers: Record<string, string> = {
        "Content-Type": "application/json",
      };

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
          headers["Authorization"] = `Basic ${auth}`;
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

      const sparkClient = await this.connectionManager.createSparkClient(
        this.config.getCoordinatorAddress(),
      );

      const unusedDepositAddresses: Map<string, DepositAddressQueryResult> =
        new Map(
          (
            await sparkClient.query_unused_deposit_addresses({
              identityPublicKey:
                await this.config.signer.getIdentityPublicKey(),
            })
          ).depositAddresses.map((addr) => [addr.depositAddress, addr]),
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

      let signingPubKey: Uint8Array;
      if (!depositAddress.leafId) {
        signingPubKey = depositAddress.userSigningPublicKey;
      } else {
        signingPubKey = await this.config.signer.generatePublicKey(
          sha256(depositAddress.leafId),
        );
      }

      const nodes = await this.finalizeDeposit({
        signingPubKey,
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
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );
    const unusedDepositAddresses: Map<string, DepositAddressQueryResult> =
      new Map(
        (
          await sparkClient.query_unused_deposit_addresses({
            identityPublicKey: await this.config.signer.getIdentityPublicKey(),
          })
        ).depositAddresses.map((addr) => [addr.depositAddress, addr]),
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
        const response = await this.depositService!.createTreeRoot({
          signingPubKey: unusedDepositAddress.userSigningPublicKey,
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
    signingPubKey: Uint8Array,
  ): Promise<TreeNode[]> {
    const leafKeyTweaks = await Promise.all(
      leaves.map(async (leaf) => ({
        leaf,
        signingPubKey,
        newSigningPubKey: await this.config.signer.generatePublicKey(),
      })),
    );

    const transfer = await this.transferService.sendTransfer(
      leafKeyTweaks,
      await this.config.signer.getIdentityPublicKey(),
    );

    const resultNodes = await this.claimTransfer(transfer);

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

    const receiverAddress = decodeSparkAddress(
      receiverSparkAddress,
      this.config.getNetworkType(),
    );

    const isSelfTransfer = equalBytes(
      await this.config.signer.getIdentityPublicKey(),
      hexToBytes(receiverAddress),
    );

    return await this.withLeaves(async () => {
      let leavesToSend = await this.selectLeaves(amountSats);

      await this.checkRefreshTimelockNodes(leavesToSend);
      leavesToSend = await this.checkExtendTimeLockNodes(leavesToSend);

      const leafKeyTweaks = await Promise.all(
        leavesToSend.map(async (leaf) => ({
          leaf,
          signingPubKey: await this.config.signer.generatePublicKey(
            sha256(leaf.id),
          ),
          newSigningPubKey: await this.config.signer.generatePublicKey(),
        })),
      );

      const transfer = await this.transferService.sendTransfer(
        leafKeyTweaks,
        hexToBytes(receiverAddress),
      );

      const leavesToRemove = new Set(leavesToSend.map((leaf) => leaf.id));
      this.leaves = this.leaves.filter((leaf) => !leavesToRemove.has(leaf.id));

      // If this is a self-transfer, lets claim it immediately
      if (isSelfTransfer) {
        await this.claimTransfer(transfer);
      }

      return mapTransferToWalletTransfer(
        transfer,
        bytesToHex(await this.config.signer.getIdentityPublicKey()),
      );
    });
  }

  private async checkExtendTimeLockNodes(
    nodes?: TreeNode[],
  ): Promise<TreeNode[]> {
    const nodesToCheck = nodes ?? this.leaves;
    const nodesToExtend: TreeNode[] = [];
    const nodeIds: string[] = [];
    let resultNodes = [...nodesToCheck];

    for (const node of nodesToCheck) {
      const nodeTx = getTxFromRawTxBytes(node.nodeTx);
      const { needRefresh } = getNextTransactionSequence(
        nodeTx.getInput(0).sequence,
      );
      if (needRefresh) {
        nodesToExtend.push(node);
        nodeIds.push(node.id);
      }
    }

    resultNodes = resultNodes.filter((node) => !nodesToExtend.includes(node));

    for (const node of nodesToExtend) {
      const signingPubKey = await this.config.signer.generatePublicKey(
        sha256(node.id),
      );
      const { nodes } = await this.transferService.extendTimelock(
        node,
        signingPubKey,
      );
      this.leaves = this.leaves.filter((leaf) => leaf.id !== node.id);
      const newNodes = await this.transferLeavesToSelf(nodes, signingPubKey);
      resultNodes.push(...newNodes);
    }

    return resultNodes;
  }

  /**
   * Internal method to refresh timelock nodes.
   *
   * @param {string} nodeId - The optional ID of the node to refresh. If not provided, all nodes will be checked.
   * @returns {Promise<void>}
   * @private
   */
  private async checkRefreshTimelockNodes(nodes?: TreeNode[]) {
    const nodesToRefresh: TreeNode[] = [];
    const nodeIds: string[] = [];

    for (const node of nodes ?? this.leaves) {
      const refundTx = getTxFromRawTxBytes(node.refundTx);
      const { needRefresh } = getNextTransactionSequence(
        refundTx.getInput(0).sequence,
        true,
      );
      if (needRefresh) {
        nodesToRefresh.push(node);
        nodeIds.push(node.id);
      }
    }

    if (nodesToRefresh.length === 0) {
      return;
    }

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    const nodesResp = await sparkClient.query_nodes({
      source: {
        $case: "nodeIds",
        nodeIds: {
          nodeIds,
        },
      },
      includeParents: true,
    });

    const nodesMap = new Map<string, TreeNode>();
    for (const node of Object.values(nodesResp.nodes)) {
      nodesMap.set(node.id, node);
    }

    for (const node of nodesToRefresh) {
      if (!node.parentNodeId) {
        throw new Error(`node ${node.id} has no parent`);
      }

      const parentNode = nodesMap.get(node.parentNodeId);
      if (!parentNode) {
        throw new Error(`parent node ${node.parentNodeId} not found`);
      }

      const { nodes } = await this.transferService.refreshTimelockNodes(
        [node],
        parentNode,
        await this.config.signer.generatePublicKey(sha256(node.id)),
      );

      if (nodes.length !== 1) {
        throw new Error(`expected 1 node, got ${nodes.length}`);
      }

      const newNode = nodes[0];
      if (!newNode) {
        throw new Error("Failed to refresh timelock node");
      }

      this.leaves = this.leaves.filter((leaf) => leaf.id !== node.id);
      this.leaves.push(newNode);
    }
  }

  /**
   * Claims a specific transfer.
   *
   * @param {Transfer} transfer - The transfer to claim
   * @returns {Promise<Object>} The claim result
   */
  private async claimTransfer(
    transfer: Transfer,
    emit: boolean = false,
    retryCount: number = 0,
  ) {
    const MAX_RETRIES = 5;
    const BASE_DELAY_MS = 1000;
    const MAX_DELAY_MS = 10000;

    if (retryCount > 0) {
      const delayMs = Math.min(
        BASE_DELAY_MS * Math.pow(2, retryCount - 1),
        MAX_DELAY_MS,
      );
      await new Promise((resolve) => setTimeout(resolve, delayMs));
    }
    try {
      let result = await this.claimTransferMutex.runExclusive(async () => {
        const leafPubKeyMap =
          await this.transferService.verifyPendingTransfer(transfer);

        let leavesToClaim: LeafKeyTweak[] = [];

        for (const leaf of transfer.leaves) {
          if (leaf.leaf) {
            const leafPubKey = leafPubKeyMap.get(leaf.leaf.id);
            if (leafPubKey) {
              leavesToClaim.push({
                leaf: leaf.leaf,
                signingPubKey: leafPubKey,
                newSigningPubKey: await this.config.signer.generatePublicKey(
                  sha256(leaf.leaf.id),
                ),
              });
            }
          }
        }

        const response = await this.transferService.claimTransfer(
          transfer,
          leavesToClaim,
        );

        this.leaves.push(...response.nodes);

        if (emit) {
          this.emit(
            "transfer:claimed",
            transfer.id,
            (await this.getBalance()).balance,
          );
        }

        return response.nodes;
      });

      await this.checkRefreshTimelockNodes(result);
      result = await this.checkExtendTimeLockNodes(result);

      return result;
    } catch (error) {
      if (retryCount < MAX_RETRIES) {
        console.error("Failed to claim transfer, retrying...", error);
        this.claimTransfer(transfer, emit, retryCount + 1);
        return [];
      } else if (retryCount > 0) {
        console.error("Failed to claim transfer", error);
        return [];
      } else {
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
          TransferStatus.TRANSFER_STATUSR_RECEIVER_REFUND_SIGNED
      ) {
        continue;
      }
      promises.push(
        this.claimTransfer(transfer, emit)
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

  /**
   * Cancels all sender-initiated transfers.
   *
   * @returns {Promise<void>}
   * @private
   */
  private async cancelAllSenderInitiatedTransfers() {
    for (const operator of Object.values(this.config.getSigningOperators())) {
      const transfers =
        await this.transferService.queryPendingTransfersBySender(
          operator.address,
        );

      for (const transfer of transfers.transfers) {
        if (
          transfer.status === TransferStatus.TRANSFER_STATUS_SENDER_INITIATED
        ) {
          await this.transferService.cancelTransfer(transfer, operator.address);
        }
      }
    }
  }

  // ***** Lightning Flow *****

  /**
   * Creates a Lightning invoice for receiving payments.
   *
   * @param {Object} params - Parameters for the lightning invoice
   * @param {number} params.amountSats - Amount in satoshis
   * @param {string} params.memo - Description for the invoice
   * @param {number} [params.expirySeconds] - Optional expiry time in seconds
   * @returns {Promise<LightningReceiveRequest>} BOLT11 encoded invoice
   */
  public async createLightningInvoice({
    amountSats,
    memo,
    expirySeconds = 60 * 60 * 24 * 30,
  }: CreateLightningInvoiceParams): Promise<LightningReceiveRequest> {
    if (!this.sspClient) {
      throw new ConfigurationError("SSP client not initialized", {
        configKey: "sspClient",
      });
    }

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

    const requestLightningInvoice = async (
      amountSats: number,
      paymentHash: Uint8Array,
      memo?: string,
    ) => {
      const network = this.config.getNetwork();
      let bitcoinNetwork: BitcoinNetwork = BitcoinNetwork.REGTEST;
      if (network === Network.MAINNET) {
        bitcoinNetwork = BitcoinNetwork.MAINNET;
      } else if (network === Network.REGTEST) {
        bitcoinNetwork = BitcoinNetwork.REGTEST;
      }

      const invoice = await this.sspClient!.requestLightningReceive({
        amountSats,
        network: bitcoinNetwork,
        paymentHash: bytesToHex(paymentHash),
        expirySecs: expirySeconds,
        memo,
      });

      return invoice;
    };

    const invoice = await this.lightningService!.createLightningInvoice({
      amountSats,
      memo,
      invoiceCreator: requestLightningInvoice,
    });

    return invoice;
  }

  /**
   * Pays a Lightning invoice.
   *
   * @param {Object} params - Parameters for paying the invoice
   * @param {string} params.invoice - The BOLT11-encoded Lightning invoice to pay
   * @returns {Promise<LightningSendRequest>} The Lightning payment request details
   */
  public async payLightningInvoice({
    invoice,
    maxFeeSats,
  }: PayLightningInvoiceParams) {
    return await this.withLeaves(async () => {
      if (!this.sspClient) {
        throw new ConfigurationError("SSP client not initialized", {
          configKey: "sspClient",
        });
      }

      const decodedInvoice = decode(invoice);
      const amountSats =
        Number(
          decodedInvoice.sections.find((section) => section.name === "amount")
            ?.value,
        ) / 1000;

      if (isNaN(amountSats) || amountSats <= 0) {
        throw new ValidationError("Invalid amount", {
          field: "amountSats",
          value: amountSats,
          expected: "positive number",
        });
      }

      const paymentHash = decodedInvoice.sections.find(
        (section) => section.name === "payment_hash",
      )?.value;

      if (!paymentHash) {
        throw new ValidationError("No payment hash found in invoice", {
          field: "paymentHash",
        });
      }

      const feeEstimate = await this.getLightningSendFeeEstimate({
        encodedInvoice: invoice,
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

      let leaves = await this.selectLeaves(totalAmount);

      await this.checkRefreshTimelockNodes(leaves);
      leaves = await this.checkExtendTimeLockNodes(leaves);

      const leavesToSend = await Promise.all(
        leaves.map(async (leaf) => ({
          leaf,
          signingPubKey: await this.config.signer.generatePublicKey(
            sha256(leaf.id),
          ),
          newSigningPubKey: await this.config.signer.generatePublicKey(),
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
      });

      if (!swapResponse.transfer) {
        throw new Error("Failed to swap nodes for preimage");
      }

      const transfer = await this.transferService.sendTransferTweakKey(
        swapResponse.transfer,
        leavesToSend,
        new Map(),
      );

      const sspResponse = await this.sspClient.requestLightningSend({
        encodedInvoice: invoice,
        idempotencyKey: paymentHash,
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

  /**
   * Gets fee estimate for sending Lightning payments.
   *
   * @param {LightningSendFeeEstimateInput} params - Input parameters for fee estimation
   * @returns {Promise<number>} Fee estimate for sending Lightning payments
   */
  public async getLightningSendFeeEstimate({
    encodedInvoice,
  }: LightningSendFeeEstimateInput): Promise<number> {
    if (!this.sspClient) {
      throw new ConfigurationError("SSP client not initialized", {
        configKey: "sspClient",
      });
    }

    const feeEstimate =
      await this.sspClient.getLightningSendFeeEstimate(encodedInvoice);

    if (!feeEstimate) {
      throw new Error("Failed to get lightning send fee estimate");
    }
    const satsFeeEstimate = mapCurrencyAmount(feeEstimate.feeEstimate);
    return Math.ceil(satsFeeEstimate.sats);
  }

  // ***** Tree Creation Flow *****

  /**
   * Generates a deposit address for a tree.
   *
   * @param {number} vout - The vout index
   * @param {Uint8Array} parentSigningPubKey - The parent signing public key
   * @param {Transaction} [parentTx] - Optional parent transaction
   * @param {TreeNode} [parentNode] - Optional parent node
   * @returns {Promise<Object>} Deposit address information
   * @private
   */
  private async generateDepositAddressForTree(
    vout: number,
    parentSigningPubKey: Uint8Array,
    parentTx?: Transaction,
    parentNode?: TreeNode,
  ) {
    return await this.treeCreationService!.generateDepositAddressForTree(
      vout,
      parentSigningPubKey,
      parentTx,
      parentNode,
    );
  }

  /**
   * Creates a tree structure.
   *
   * @param {number} vout - The vout index
   * @param {DepositAddressTree} root - The root of the tree
   * @param {boolean} createLeaves - Whether to create leaves
   * @param {Transaction} [parentTx] - Optional parent transaction
   * @param {TreeNode} [parentNode] - Optional parent node
   * @returns {Promise<Object>} The created tree
   * @private
   */
  private async createTree(
    vout: number,
    root: DepositAddressTree,
    createLeaves: boolean,
    parentTx?: Transaction,
    parentNode?: TreeNode,
  ) {
    return await this.treeCreationService!.createTree(
      vout,
      root,
      createLeaves,
      parentTx,
      parentNode,
    );
  }

  // ***** Cooperative Exit Flow *****

  /**
   * Initiates a withdrawal to move funds from the Spark network to an on-chain Bitcoin address.
   *
   * @param {Object} params - Parameters for the withdrawal
   * @param {string} params.onchainAddress - The Bitcoin address where the funds should be sent
   * @param {number} [params.amountSats] - The amount in satoshis to withdraw. If not specified, attempts to withdraw all available funds
   * @returns {Promise<CoopExitRequest | null | undefined>} The withdrawal request details, or null/undefined if the request cannot be completed
   */
  public async withdraw({
    onchainAddress,
    exitSpeed,
    amountSats,
  }: {
    onchainAddress: string;
    exitSpeed: ExitSpeed;
    amountSats?: number;
  }) {
    if (!Number.isSafeInteger(amountSats)) {
      throw new ValidationError("Sats amount must be less than 2^53", {
        field: "amountSats",
        value: amountSats,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }
    return await this.withLeaves(async () => {
      return await this.coopExit(onchainAddress, exitSpeed, amountSats);
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
    exitSpeed: ExitSpeed,
    targetAmountSats?: number,
  ) {
    if (!Number.isSafeInteger(targetAmountSats)) {
      throw new ValidationError("Sats amount must be less than 2^53", {
        field: "targetAmountSats",
        value: targetAmountSats,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    let leavesToSend: TreeNode[] = [];
    if (targetAmountSats) {
      leavesToSend = await this.selectLeaves(targetAmountSats);
    } else {
      leavesToSend = this.leaves.map((leaf) => ({
        ...leaf,
      }));
    }

    const feeEstimate = await this.sspClient?.getCoopExitFeeEstimate({
      leafExternalIds: leavesToSend.map((leaf) => leaf.id),
      withdrawalAddress: onchainAddress,
    });

    if (feeEstimate) {
      let fee: number | undefined;
      switch (exitSpeed) {
        case ExitSpeed.FAST:
          fee =
            (feeEstimate.speedFast?.l1BroadcastFee.originalValue || 0) +
            (feeEstimate.speedFast?.userFee.originalValue || 0);
          break;
        case ExitSpeed.MEDIUM:
          fee =
            (feeEstimate.speedMedium?.l1BroadcastFee.originalValue || 0) +
            (feeEstimate.speedMedium?.userFee.originalValue || 0);
          break;
        case ExitSpeed.SLOW:
          fee =
            (feeEstimate.speedSlow?.l1BroadcastFee.originalValue || 0) +
            (feeEstimate.speedSlow?.userFee.originalValue || 0);
          break;
        default:
          throw new ValidationError("Invalid exit speed", {
            field: "exitSpeed",
            value: exitSpeed,
            expected: "FAST, MEDIUM, or SLOW",
          });
      }

      if (
        fee !== undefined &&
        fee > leavesToSend.reduce((acc, leaf) => acc + leaf.value, 0)
      ) {
        throw new ValidationError(
          "The fee for the withdrawal is greater than the target amount",
          {
            field: "fee",
            value: fee,
            expected: "less than or equal to the target amount",
          },
        );
      }
    }
    await this.checkRefreshTimelockNodes(leavesToSend);
    leavesToSend = await this.checkExtendTimeLockNodes(leavesToSend);

    const leafKeyTweaks = await Promise.all(
      leavesToSend.map(async (leaf) => ({
        leaf,
        signingPubKey: await this.config.signer.generatePublicKey(
          sha256(leaf.id),
        ),
        newSigningPubKey: await this.config.signer.generatePublicKey(),
      })),
    );

    const coopExitRequest = await this.sspClient?.requestCoopExit({
      leafExternalIds: leavesToSend.map((leaf) => leaf.id),
      withdrawalAddress: onchainAddress,
      idempotencyKey: uuidv7(),
      exitSpeed,
    });

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

    const completeResponse = await this.sspClient?.completeCoopExit({
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
   * @returns {Promise<CoopExitFeeEstimatesOutput | null>} Fee estimate for the withdrawal
   */
  public async getWithdrawalFeeEstimate({
    amountSats,
    withdrawalAddress,
  }: {
    amountSats: number;
    withdrawalAddress: string;
  }): Promise<CoopExitFeeEstimatesOutput | null> {
    if (!this.sspClient) {
      throw new ConfigurationError("SSP client not initialized", {
        configKey: "sspClient",
      });
    }

    if (!Number.isSafeInteger(amountSats)) {
      throw new ValidationError("Sats amount must be less than 2^53", {
        field: "amountSats",
        value: amountSats,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    let leaves = await this.selectLeaves(amountSats);

    await this.checkRefreshTimelockNodes(leaves);
    leaves = await this.checkExtendTimeLockNodes(leaves);

    const feeEstimate = await this.sspClient.getCoopExitFeeEstimate({
      leafExternalIds: leaves.map((leaf) => leaf.id),
      withdrawalAddress,
    });

    return feeEstimate;
  }

  // ***** Token Flow *****

  /**
   * Synchronizes token outputs for the wallet.
   *
   * @returns {Promise<void>}
   * @private
   */
  protected async syncTokenOutputs() {
    this.tokenOuputs.clear();

    const unsortedTokenOutputs =
      await this.tokenTransactionService.fetchOwnedTokenOutputs(
        [await this.config.signer.getIdentityPublicKey()],
        [],
      );

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

    // Group leaves by token key
    const groupedOutputs = new Map<
      string,
      OutputWithPreviousTransactionData[]
    >();

    filteredTokenOutputs.forEach((output) => {
      const tokenKey = bytesToHex(output.output!.tokenPublicKey!);
      const index = output.previousTransactionVout!;

      if (!groupedOutputs.has(tokenKey)) {
        groupedOutputs.set(tokenKey, []);
      }

      groupedOutputs.get(tokenKey)!.push({
        ...output,
        previousTransactionVout: index,
      });
    });

    this.tokenOuputs = groupedOutputs;
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
    tokenPublicKey,
    tokenAmount,
    receiverSparkAddress,
    selectedOutputs,
  }: {
    tokenPublicKey: string;
    tokenAmount: bigint;
    receiverSparkAddress: string;
    selectedOutputs?: OutputWithPreviousTransactionData[];
  }): Promise<string> {
    const receiverAddress = decodeSparkAddress(
      receiverSparkAddress,
      this.config.getNetworkType(),
    );

    await this.syncTokenOutputs();
    if (!this.tokenOuputs.has(tokenPublicKey)) {
      throw new Error("No TTXOs with the given tokenPublicKey");
    }

    const tokenPublicKeyBytes = hexToBytes(tokenPublicKey);
    const receiverSparkAddressBytes = hexToBytes(receiverAddress);

    if (selectedOutputs) {
      if (
        !checkIfSelectedOutputsAreAvailable(
          selectedOutputs,
          this.tokenOuputs,
          tokenPublicKeyBytes,
        )
      ) {
        throw new Error("One or more selected TTXOs are not available");
      }
    } else {
      selectedOutputs = this.selectTokenOutputs(tokenPublicKey, tokenAmount);
    }

    if (selectedOutputs!.length > MAX_TOKEN_OUTPUTS) {
      throw new Error("Too many TTXOs selected");
    }

    const tokenTransaction =
      await this.tokenTransactionService.constructTransferTokenTransaction(
        selectedOutputs,
        receiverSparkAddressBytes,
        tokenPublicKeyBytes,
        tokenAmount,
      );

    return await this.tokenTransactionService.broadcastTokenTransaction(
      tokenTransaction,
      selectedOutputs.map((output) => output.output!.ownerPublicKey),
      selectedOutputs.map((output) => output.output!.revocationCommitment!),
    );
  }

  public async queryTokenTransactions(
    tokenPublicKeys: string[],
    tokenTransactionHashes?: string[],
  ): Promise<TokenTransactionWithStatus[]> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    let queryParams;
    if (tokenTransactionHashes?.length) {
      queryParams = {
        tokenPublicKeys: tokenPublicKeys?.map(hexToBytes)!,
        ownerPublicKeys: [hexToBytes(await this.getIdentityPublicKey())],
        tokenTransactionHashes: tokenTransactionHashes.map(hexToBytes),
      };
    } else {
      queryParams = {
        tokenPublicKeys: tokenPublicKeys?.map(hexToBytes)!,
        ownerPublicKeys: [hexToBytes(await this.getIdentityPublicKey())],
      };
    }

    const response = await sparkClient.query_token_transactions(queryParams);
    return response.tokenTransactionsWithStatus;
  }

  public async getTokenL1Address(): Promise<string> {
    return getP2WPKHAddressFromPublicKey(
      await this.config.signer.getIdentityPublicKey(),
      this.config.getNetwork(),
    );
  }

  /**
   * Selects TTXOs for a transfer.
   *
   * @param {string} tokenPublicKey - The public key of the token
   * @param {bigint} tokenAmount - The amount of tokens to select TTXOs for
   * @returns {OutputWithPreviousTransactionData[]} The selected TTXOs
   * @private
   */
  private selectTokenOutputs(
    tokenPublicKey: string,
    tokenAmount: bigint,
  ): OutputWithPreviousTransactionData[] {
    return this.tokenTransactionService.selectTokenOutputs(
      this.tokenOuputs.get(tokenPublicKey)!,
      tokenAmount,
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
   * Get a Lightning receive request by ID.
   *
   * @param {string} id - The ID of the Lightning receive request
   * @returns {Promise<LightningReceiveRequest | null>} The Lightning receive request
   */
  public async getLightningReceiveRequest(
    id: string,
  ): Promise<LightningReceiveRequest | null> {
    if (!this.sspClient) {
      throw new ConfigurationError("SSP client not initialized", {
        configKey: "sspClient",
      });
    }

    return await this.sspClient.getLightningReceiveRequest(id);
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
    if (!this.sspClient) {
      throw new ConfigurationError("SSP client not initialized", {
        configKey: "sspClient",
      });
    }

    return await this.sspClient.getLightningSendRequest(id);
  }

  /**
   * Get a coop exit request by ID.
   *
   * @param {string} id - The ID of the coop exit request
   * @returns {Promise<CoopExitRequest | null>} The coop exit request
   */
  public async getCoopExitRequest(id: string): Promise<CoopExitRequest | null> {
    if (!this.sspClient) {
      throw new ConfigurationError("SSP client not initialized", {
        configKey: "sspClient",
      });
    }

    return await this.sspClient.getCoopExitRequest(id);
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
}
