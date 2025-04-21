import { createLrc20ConnectionManager } from "@buildonspark/lrc20-sdk/grpc";
import { ILrc20ConnectionManager } from "@buildonspark/lrc20-sdk/grpc/types";
import {
  bytesToHex,
  bytesToNumberBE,
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
import SspClient from "./graphql/client.js";
import {
  BitcoinNetwork,
  CoopExitFeeEstimateOutput,
  CoopExitRequest,
  LeavesSwapFeeEstimateOutput,
  LeavesSwapRequest,
  LightningReceiveFeeEstimateOutput,
  LightningReceiveRequest,
  LightningSendFeeEstimateInput,
  LightningSendFeeEstimateOutput,
  LightningSendRequest,
  UserLeafInput,
} from "./graphql/objects/index.js";
import {
  DepositAddressQueryResult,
  OutputWithPreviousTransactionData,
  QueryTransfersResponse,
  TokenTransactionWithStatus,
  Transfer,
  TransferStatus,
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
import {
  decodeSparkAddress,
  encodeSparkAddress,
  SparkAddressFormat,
} from "./address/index.js";
import { SparkSigner } from "./signer/signer.js";
import { getCrypto } from "./utils/crypto.js";
import { getMasterHDKeyFromSeed } from "./utils/index.js";
const crypto = getCrypto();

// Add this constant at the file level
const MAX_TOKEN_LEAVES = 100;

export type CreateLightningInvoiceParams = {
  amountSats: number;
  memo?: string;
  expirySeconds?: number;
};

export type PayLightningInvoiceParams = {
  invoice: string;
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
  signer?: SparkSigner;
  options?: ConfigOptions;
}

/**
 * The SparkWallet class is the primary interface for interacting with the Spark network.
 * It provides methods for creating and managing wallets, handling deposits, executing transfers,
 * and interacting with the Lightning Network.
 */
export class SparkWallet {
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

  protected leaves: TreeNode[] = [];
  protected tokenOuputs: Map<string, OutputWithPreviousTransactionData[]> =
    new Map();

  protected constructor(options?: ConfigOptions, signer?: SparkSigner) {
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
    signer,
    options,
  }: SparkWalletProps) {
    const wallet = new SparkWallet(options, signer);
    const initResponse = await wallet.initWallet(mnemonicOrSeed);
    return {
      wallet,
      ...initResponse,
    };
  }

  private async initializeWallet() {
    this.sspClient = new SspClient(this.config);
    await this.connectionManager.createClients();

    await this.syncWallet();
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
      throw new Error("Target amount must be positive");
    }

    const leaves = await this.getLeaves();
    if (leaves.length === 0) {
      throw new Error("No owned leaves found");
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
    const totalAmount = this.leaves.reduce((acc, leaf) => acc + leaf.value, 0);

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
    await this.refreshTimelockNodes();
    await this.extendTimeLockNodes();
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
   *   - tokenBalance: Map of token balances and leaf counts
   * @private
   */
  protected async initWallet(
    mnemonicOrSeed?: Uint8Array | string,
  ): Promise<InitWalletResponse | undefined> {
    const returnMnemonic = !mnemonicOrSeed;
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

    await this.initWalletFromSeed(seed);

    const network = this.config.getNetwork();
    // TODO: remove this once we move it back to the signer
    const masterPrivateKey = getMasterHDKeyFromSeed(seed).privateKey!;
    this.lrc20Wallet = new LRCWallet(
      bytesToHex(masterPrivateKey),
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
  private async initWalletFromSeed(seed: Uint8Array | string) {
    const identityPublicKey =
      await this.config.signer.createSparkWalletFromSeed(
        seed,
        this.config.getNetwork(),
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
      throw new Error("SSP client not initialized");
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

    await this.claimTransfers();

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
        idempotencyKey: crypto.randomUUID(),
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

      await this.claimTransfers();

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
  ): Promise<QueryTransfersResponse> {
    return await this.transferService.queryAllTransfers(limit, offset);
  }

  public async getTokenInfo(): Promise<TokenInfo[]> {
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
      maxSupply: bytesToNumberBE(info.totalSupply),
    }));
  }

  /**
   * Gets the current balance of the wallet.
   * You can use the forceRefetch option to synchronize your wallet and claim any
   * pending incoming lightning payment, spark transfer, or bitcoin deposit before returning the balance.
   *
   * @returns {Promise<Object>} Object containing:
   *   - balance: The wallet's current balance in satoshis
   *   - tokenBalances: Map of token public keys to token balances
   */
  public async getBalance(): Promise<{
    balance: bigint;
    tokenBalances: Map<string, { balance: bigint }>;
  }> {
    this.leaves = await this.getLeaves();
    await this.syncTokenOutputs();

    const tokenBalances = new Map<string, { balance: bigint }>();

    for (const [tokenPublicKey, leaves] of this.tokenOuputs.entries()) {
      tokenBalances.set(tokenPublicKey, {
        balance: calculateAvailableTokenAmount(leaves),
      });
    }

    return {
      balance: this.leaves.reduce((acc, leaf) => acc + BigInt(leaf.value), 0n),
      tokenBalances,
    };
  }

  // ***** Deposit Flow *****

  /**
   * Generates a new deposit address for receiving bitcoin funds.
   * Note that this function returns a bitcoin address, not a spark address.
   * For Layer 1 Bitcoin deposits, Spark generates Pay to Taproot (P2TR) addresses.
   * These addresses start with "bc1p" and can be used to receive Bitcoin from any wallet.
   *
   * @returns {Promise<string>} A Bitcoin address for depositing funds
   */
  public async getDepositAddress(): Promise<string> {
    return await this.generateDepositAddress();
  }

  /**
   * Generates a deposit address for receiving funds.
   *
   * @returns {Promise<string>} A deposit address
   * @private
   */
  private async generateDepositAddress(): Promise<string> {
    const leafId = crypto.randomUUID();
    const signingPubkey = await this.config.signer.generatePublicKey(
      sha256(leafId),
    );
    const address = await this.depositService!.generateDepositAddress({
      signingPubkey,
      leafId,
    });
    if (!address.depositAddress) {
      throw new Error("Failed to generate deposit address");
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
    const res = await this.depositService!.createTreeRoot({
      signingPubKey,
      verifyingKey,
      depositTx,
      vout,
    });

    for (const node of res.nodes) {
      const { nodes } = await this.transferService.extendTimelock(
        node,
        signingPubKey,
      );

      await this.transferDepositToSelf(nodes, signingPubKey);
    }
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
   *
   * @param {string} txid - The transaction ID of the deposit
   * @returns {Promise<TreeNode[] | undefined>} The nodes resulting from the deposit
   */
  public async claimDeposit(txid: string) {
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

      if (this.config.getNetwork() === Network.REGTEST) {
        const auth = btoa(
          `${ELECTRS_CREDENTIALS.username}:${ELECTRS_CREDENTIALS.password}`,
        );
        headers["Authorization"] = `Basic ${auth}`;
      }

      const response = await fetch(`${baseUrl}/tx/${txid}/hex`, {
        headers,
      });

      const txHex = await response.text();
      if (!/^[0-9A-Fa-f]+$/.test(txHex)) {
        throw new Error("Transaction not found");
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
        return [];
      }

      const nodes = await this.finalizeDeposit({
        signingPubKey: depositAddress.userSigningPublicKey,
        verifyingKey: depositAddress.verifyingPublicKey,
        depositTx,
        vout,
      });

      return nodes;
    });

    this.mutexes.delete(txid);

    return nodes;
  }

  /**
   * Transfers deposit to self to claim ownership.
   *
   * @param {TreeNode[]} leaves - The leaves to transfer
   * @param {Uint8Array} signingPubKey - The signing public key
   * @returns {Promise<TreeNode[] | undefined>} The nodes resulting from the transfer
   * @private
   */
  private async transferDepositToSelf(
    leaves: TreeNode[],
    signingPubKey: Uint8Array,
  ): Promise<TreeNode[] | undefined> {
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

    return await this.claimTransfer(transfer);
  }
  // ***** Transfer Flow *****

  /**
   * Sends a transfer to another Spark user.
   *
   * @param {TransferParams} params - Parameters for the transfer
   * @param {string} params.receiverSparkAddress - The recipient's Spark address
   * @param {number} params.amountSats - Amount to send in satoshis
   * @returns {Promise<Transfer>} The completed transfer details
   */
  public async transfer({ amountSats, receiverSparkAddress }: TransferParams) {
    const receiverAddress = decodeSparkAddress(
      receiverSparkAddress,
      this.config.getNetworkType(),
    );

    return await this.withLeaves(async () => {
      const leavesToSend = await this.selectLeaves(amountSats);

      await this.refreshTimelockNodes();
      await this.extendTimeLockNodes();

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

      return transfer;
    });
  }

  private async extendTimeLockNodes() {
    const nodesToExtend: TreeNode[] = [];
    const nodeIds: string[] = [];

    for (const node of this.leaves) {
      const nodeTx = getTxFromRawTxBytes(node.nodeTx);
      const { nextSequence } = getNextTransactionSequence(
        nodeTx.getInput(0).sequence,
      );
      if (nextSequence <= 0) {
        nodesToExtend.push(node);
        nodeIds.push(node.id);
      }
    }

    for (const node of nodesToExtend) {
      const signingPubKey = await this.config.signer.generatePublicKey(
        sha256(node.id),
      );
      const { nodes } = await this.transferService.extendTimelock(
        node,
        signingPubKey,
      );
      await this.transferDepositToSelf(nodes, signingPubKey);
    }
  }

  /**
   * Internal method to refresh timelock nodes.
   *
   * @param {string} nodeId - The optional ID of the node to refresh. If not provided, all nodes will be checked.
   * @returns {Promise<void>}
   * @private
   */
  private async refreshTimelockNodes(nodeId?: string) {
    const nodesToRefresh: TreeNode[] = [];
    const nodeIds: string[] = [];

    if (nodeId) {
      for (const node of this.leaves) {
        if (node.id === nodeId) {
          nodesToRefresh.push(node);
          nodeIds.push(node.id);
          break;
        }
      }
      if (nodesToRefresh.length === 0) {
        throw new Error(`node ${nodeId} not found`);
      }
    } else {
      for (const node of this.leaves) {
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
   * Gets all pending transfers.
   *
   * @returns {Promise<Transfer[]>} The pending transfers
   */
  public async getPendingTransfers() {
    return (await this.transferService.queryPendingTransfers()).transfers;
  }

  /**
   * Claims a specific transfer.
   *
   * @param {Transfer} transfer - The transfer to claim
   * @returns {Promise<Object>} The claim result
   */
  public async claimTransfer(transfer: Transfer) {
    return await this.claimTransferMutex.runExclusive(async () => {
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
      await this.refreshTimelockNodes();
      await this.extendTimeLockNodes();

      return response.nodes;
    });
  }

  /**
   * Claims all pending transfers.
   *
   * @returns {Promise<boolean>} True if any transfers were claimed
   */
  public async claimTransfers(): Promise<boolean> {
    const transfers = await this.transferService.queryPendingTransfers();
    let claimed = false;
    for (const transfer of transfers.transfers) {
      if (
        transfer.status !== TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED &&
        transfer.status !==
          TransferStatus.TRANSFER_STATUS_RECEIVER_KEY_TWEAKED &&
        transfer.status !==
          TransferStatus.TRANSFER_STATUSR_RECEIVER_REFUND_SIGNED
      ) {
        continue;
      }
      await this.claimTransfer(transfer);
      claimed = true;
    }
    return claimed;
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
          await this.transferService.cancelSendTransfer(
            transfer,
            operator.address,
          );
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
      throw new Error("SSP client not initialized");
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
  public async payLightningInvoice({ invoice }: PayLightningInvoiceParams) {
    return await this.withLeaves(async () => {
      if (!this.sspClient) {
        throw new Error("SSP client not initialized");
      }

      // TODO: Get fee

      const decodedInvoice = decode(invoice);
      const amountSats =
        Number(
          decodedInvoice.sections.find((section) => section.name === "amount")
            ?.value,
        ) / 1000;

      if (isNaN(amountSats) || amountSats <= 0) {
        throw new Error("Invalid amount");
      }

      const paymentHash = decodedInvoice.sections.find(
        (section) => section.name === "payment_hash",
      )?.value;

      if (!paymentHash) {
        throw new Error("No payment hash found in invoice");
      }

      const leaves = await this.selectLeaves(amountSats);

      await this.refreshTimelockNodes();
      await this.extendTimeLockNodes();

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
   * Gets fee estimate for receiving Lightning payments.
   *
   * @param {Object} params - Input parameters for fee estimation
   * @param {number} params.amountSats - Amount in satoshis
   * @returns {Promise<LightningReceiveFeeEstimateOutput | null>} Fee estimate for receiving Lightning payments
   */
  public async getLightningReceiveFeeEstimate({
    amountSats,
  }: {
    amountSats: number;
  }): Promise<LightningReceiveFeeEstimateOutput | null> {
    if (!this.sspClient) {
      throw new Error("SSP client not initialized");
    }

    const network = this.config.getSspNetwork();

    const feeEstimate = await this.sspClient.getLightningReceiveFeeEstimate(
      amountSats,
      network,
    );

    if (!feeEstimate) {
      throw new Error("Failed to get lightning receive fee estimate");
    }

    return feeEstimate;
  }

  /**
   * Gets fee estimate for sending Lightning payments.
   *
   * @param {LightningSendFeeEstimateInput} params - Input parameters for fee estimation
   * @returns {Promise<LightningSendFeeEstimateOutput | null>} Fee estimate for sending Lightning payments
   */
  public async getLightningSendFeeEstimate({
    encodedInvoice,
  }: LightningSendFeeEstimateInput): Promise<LightningSendFeeEstimateOutput | null> {
    if (!this.sspClient) {
      throw new Error("SSP client not initialized");
    }

    const feeEstimate =
      await this.sspClient.getLightningSendFeeEstimate(encodedInvoice);

    if (!feeEstimate) {
      throw new Error("Failed to get lightning send fee estimate");
    }
    return feeEstimate;
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
    amountSats,
  }: {
    onchainAddress: string;
    amountSats?: number;
  }) {
    if (amountSats && amountSats < 10000) {
      throw new Error("The minimum amount for a withdrawal is 10000 sats");
    }
    return await this.withLeaves(async () => {
      return await this.coopExit(onchainAddress, amountSats);
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
  private async coopExit(onchainAddress: string, targetAmountSats?: number) {
    let leavesToSend: TreeNode[] = [];
    if (targetAmountSats) {
      leavesToSend = await this.selectLeaves(targetAmountSats);
    } else {
      leavesToSend = this.leaves.map((leaf) => ({
        ...leaf,
      }));
    }

    if (leavesToSend.reduce((acc, leaf) => acc + leaf.value, 0) < 10000) {
      throw new Error("The minimum amount for a withdrawal is 10000 sats");
    }

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
      idempotencyKey: crypto.randomUUID(),
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
   * @returns {Promise<CoopExitFeeEstimateOutput | null>} Fee estimate for the withdrawal
   */
  public async getCoopExitFeeEstimate({
    amountSats,
    withdrawalAddress,
  }: {
    amountSats: number;
    withdrawalAddress: string;
  }): Promise<CoopExitFeeEstimateOutput | null> {
    if (!this.sspClient) {
      throw new Error("SSP client not initialized");
    }

    if (amountSats < 10000) {
      throw new Error("The minimum amount for a withdrawal is 10000 sats");
    }

    const leaves = await this.selectLeaves(amountSats);

    return await this.sspClient.getCoopExitFeeEstimate({
      leafExternalIds: leaves.map((leaf) => leaf.id),
      withdrawalAddress,
    });
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

    const trackedPublicKeys = await this.config.signer.getTrackedPublicKeys();
    const unsortedTokenOutputs =
      await this.tokenTransactionService.fetchOwnedTokenOutputs(
        [...trackedPublicKeys, await this.config.signer.getIdentityPublicKey()],
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
      throw new Error("No token leaves with the given tokenPublicKey");
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
        throw new Error("One or more selected leaves are not available");
      }
    } else {
      selectedOutputs = this.selectTokenOutputs(tokenPublicKey, tokenAmount);
    }

    if (selectedOutputs!.length > MAX_TOKEN_LEAVES) {
      throw new Error("Too many leaves selected");
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
      await this.config.signer.getMasterPublicKey(),
      this.config.getNetwork(),
    );
  }

  /**
   * Selects token leaves for a transfer.
   *
   * @param {string} tokenPublicKey - The public key of the token
   * @param {bigint} tokenAmount - The amount of tokens to select leaves for
   * @returns {OutputWithPreviousTransactionData[]} The selected leaves
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
   * @param {string} message - Unhashed message to sign
   * @param {boolean} [compact] - Whether to use compact encoding. If false, the message will be encoded as DER.
   * @returns {Promise<string>} The signed message
   */
  public async signMessage(
    message: string,
    compact?: boolean,
  ): Promise<string> {
    const hash = sha256(message);
    return bytesToHex(
      await this.config.signer.signMessageWithIdentityKey(hash, compact),
    );
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
      throw new Error("SSP client not initialized");
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
      throw new Error("SSP client not initialized");
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
      throw new Error("SSP client not initialized");
    }

    return await this.sspClient.getCoopExitRequest(id);
  }
}
