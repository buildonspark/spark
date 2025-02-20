import mempoolJS from "@mempool/mempool.js";
import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { Transaction } from "@scure/btc-signer";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { sha256 } from "@scure/btc-signer/utils";
import { decode } from "light-bolt11-decoder";
import SspClient from "./graphql/client";
import {
  BitcoinNetwork,
  CoopExitFeeEstimateInput,
  CoopExitFeeEstimateOutput,
  LightningReceiveFeeEstimateInput,
  LightningReceiveFeeEstimateOutput,
  LightningSendFeeEstimateInput,
  LightningSendFeeEstimateOutput,
  UserLeafInput,
} from "./graphql/objects";
import {
  GenerateDepositAddressResponse,
  LeafWithPreviousTransactionData,
  QueryPendingTransfersResponse,
  Transfer,
  TransferStatus,
  TreeNode,
} from "./proto/spark";
import { WalletConfig, WalletConfigService } from "./services/config";
import { ConnectionManager } from "./services/connection";
import { CoopExitService } from "./services/coop-exit";
import { DepositService } from "./services/deposit";
import { LightningService } from "./services/lightning";
import { TokenFreezeService } from "./services/tokens-freeze";
import { TokenTransactionService } from "./services/tokens-transaction";
import { LeafKeyTweak, TransferService } from "./services/transfer";
import {
  DepositAddressTree,
  TreeCreationService,
} from "./services/tree-creation";
import { SparkSigner } from "./signer/signer";
import {
  applyAdaptorToSignature,
  generateAdaptorFromSignature,
  generateSignatureFromExistingAdaptor,
} from "./utils/adaptor-signature";
import {
  computeTaprootKeyNoScript,
  getP2TRAddressFromPublicKey,
  getSigHashFromTx,
  getTxFromRawTxBytes,
  getTxFromRawTxHex,
  getTxId,
} from "./utils/bitcoin";
import { Network } from "./utils/network";
import {
  calculateAvailableTokenAmount,
  checkIfSelectedLeavesAreAvailable,
} from "./utils/token-transactions";
import {
  aggregateFrost,
  AggregateFrostParams,
  signFrost,
  SignFrostParams,
} from "./utils/wasm";
import { initWasm } from "./utils/wasm-wrapper";
import { InitOutput } from "./wasm/spark_bindings";

type CreateLightningInvoiceParams = {
  amountSats: number;
  expirySeconds: number;
  memo: string;
  invoiceCreator?: () => Promise<string>;
};

type PayLightningInvoiceParams = {
  invoice: string;
  amountSats?: number;
};

type SendTransferParams = {
  amount?: number;
  leaves?: TreeNode[];
  receiverPubKey: Uint8Array;
  expiryTime?: Date;
};

type DepositParams = {
  signingPubKey: Uint8Array;
  verifyingKey: Uint8Array;
  depositTx: Transaction;
  vout: number;
};

export class SparkWallet {
  private config: WalletConfigService;

  private connectionManager: ConnectionManager;

  private depositService: DepositService;
  private transferService: TransferService;
  private treeCreationService: TreeCreationService;
  private lightningService: LightningService;
  private coopExitService: CoopExitService;
  private tokenTransactionService: TokenTransactionService;
  private tokenFreezeService: TokenFreezeService;

  private sspClient: SspClient | null = null;
  private wasmModule: InitOutput | null = null;

  private leaves: TreeNode[] = [];
  private tokenLeaves: Map<string, LeafWithPreviousTransactionData[]> =
    new Map();

  constructor(network: Network, signer?: SparkSigner) {
    this.config = new WalletConfigService(network, signer);

    this.connectionManager = new ConnectionManager(this.config);

    this.depositService = new DepositService(
      this.config,
      this.connectionManager
    );
    this.transferService = new TransferService(
      this.config,
      this.connectionManager
    );
    this.treeCreationService = new TreeCreationService(
      this.config,
      this.connectionManager
    );
    this.tokenTransactionService = new TokenTransactionService(
      this.config,
      this.connectionManager
    );
    this.tokenFreezeService = new TokenFreezeService(
      this.config,
      this.connectionManager
    );
    this.lightningService = new LightningService(
      this.config,
      this.connectionManager
    );
    this.coopExitService = new CoopExitService(
      this.config,
      this.connectionManager
    );
  }

  getSigner(): SparkSigner {
    return this.config.signer;
  }

  private async initWasm() {
    try {
      this.wasmModule = await initWasm();
    } catch (e) {
      console.error("Failed to initialize Wasm module", e);
    }
  }

  private async ensureInitialized() {
    if (!this.wasmModule) {
      await this.initWasm();
    }
  }

  // TODO: Probably remove this. Only used temporarily for tests
  getConfigService(): WalletConfigService {
    return this.config;
  }

  getConfig(): WalletConfig {
    return this.config.getConfig();
  }

  async getMasterPubKey(): Promise<Uint8Array> {
    return await this.config.signer.getIdentityPublicKey();
  }

  async getP2trAddress(): Promise<string> {
    const pubKey = await this.config.signer.getIdentityPublicKey();
    const network = this.config.getNetwork();

    return getP2TRAddressFromPublicKey(pubKey, network);
  }

  async signFrost(params: SignFrostParams): Promise<Uint8Array> {
    await this.ensureInitialized();
    return signFrost(params);
  }

  async aggregateFrost(params: AggregateFrostParams): Promise<Uint8Array> {
    await this.ensureInitialized();
    return aggregateFrost(params);
  }

  async generateMnemonic(): Promise<string> {
    return await this.config.signer.generateMnemonic();
  }

  isInitialized(): boolean {
    return this.sspClient !== null && this.wasmModule !== null;
  }

  // TODO: Update to use config based on options
  async createSparkWallet(mnemonic: string): Promise<string> {
    const identityPublicKey =
      await this.config.signer.createSparkWalletFromMnemonic(mnemonic);
    await this.initializeWallet(identityPublicKey);
    return identityPublicKey;
  }

  async createSparkWalletFromSeed(seed: Uint8Array | string): Promise<string> {
    const identityPublicKey =
      await this.config.signer.createSparkWalletFromSeed(seed);
    await this.initializeWallet(identityPublicKey);
    return identityPublicKey;
  }

  private async initializeWallet(identityPublicKey: string) {
    this.sspClient = new SspClient(identityPublicKey);
    await this.initWasm();
    // TODO: Better leaf management?
    this.leaves = await this.getLeaves();
    this.config.signer.restoreSigningKeysFromLeafs(this.leaves);

    await this.syncTokenLeaves();
  }

  private async selectLeaves(targetAmount: number): Promise<TreeNode[]> {
    if (targetAmount <= 0) {
      throw new Error("Target amount must be positive");
    }

    const leaves = await this.getLeaves();
    if (leaves.length === 0) {
      return [];
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

    if (amount === targetAmount) {
      throw new Error(
        "You're trying to swap for the exact amount you have, no need to swap"
      );
    }

    if (amount < targetAmount) {
      throw new Error(
        "You don't have enough nodes to swap for the target amount"
      );
    }

    return nodes;
  }

  async syncWallet() {
    await this.claimTransfers();
    await this.syncTokenLeaves();
    this.leaves = await this.getLeaves();
    await this.optimizeLeaves();
  }

  async optimizeLeaves() {
    await this.requestLeavesSwap({ leaves: this.leaves });
  }

  async requestLeavesSwap({
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
          sha256(leaf.id)
        ),
        newSigningPubKey: await this.config.signer.generatePublicKey(),
      }))
    );

    const { transfer, signatureMap } =
      await this.transferService.sendTransferSignRefund(
        leafKeyTweaks,
        await this.config.signer.getSspIdentityPublicKey(),
        new Date(Date.now() + 10 * 60 * 1000)
      );

    const refundSignature = signatureMap.get(leavesToSwap[0].id);
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
        transfer.leaves[0].intermediateRefundTx
      ),
      adaptor_added_signature: bytesToHex(adaptorSignature),
    });

    for (let i = 1; i < transfer.leaves.length; i++) {
      const leaf = transfer.leaves[i];
      if (!leaf.leaf) {
        throw new Error("Failed to get leaf");
      }

      const refundSignature = signatureMap.get(leaf.leaf.id);
      if (!refundSignature) {
        throw new Error("Failed to get refund signature");
      }

      const signature = generateSignatureFromExistingAdaptor(
        refundSignature,
        adaptorPrivateKey
      );

      userLeaves.push({
        leaf_id: leaf.leaf.id,
        raw_unsigned_refund_transaction: bytesToHex(leaf.intermediateRefundTx),
        adaptor_added_signature: bytesToHex(signature),
      });
    }

    const adaptorPubkey = bytesToHex(secp256k1.getPublicKey(adaptorPrivateKey));

    const request = await this.sspClient?.requestLeaveSwap({
      userLeaves,
      adaptorPubkey,
      targetAmountSats:
        targetAmount || leavesToSwap.reduce((acc, leaf) => acc + leaf.value, 0),
      totalAmountSats: leavesToSwap.reduce((acc, leaf) => acc + leaf.value, 0),
      // TODO: Request fee from SSP
      feeSats: 0,
      // TODO: Map config network to proto network
      network: BitcoinNetwork.REGTEST,
    });

    if (!request) {
      throw new Error("Failed to request leaves swap");
    }

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress()
    );
    for (const leaf of request.swapLeaves) {
      const response = await sparkClient.query_nodes({
        source: {
          $case: "nodeIds",
          nodeIds: {
            nodeIds: [leaf.leafId],
          },
        },
      });

      const nodesLength = Object.values(response.nodes).length;
      if (nodesLength !== 1) {
        throw new Error(`Expected 1 node, got ${nodesLength}`);
      }

      const nodeTx = getTxFromRawTxBytes(response.nodes[leaf.leafId].nodeTx);
      const refundTxBytes = hexToBytes(leaf.rawUnsignedRefundTransaction);
      const refundTx = getTxFromRawTxBytes(refundTxBytes);
      const sighash = getSigHashFromTx(refundTx, 0, nodeTx.getOutput(0));
      const nodePublicKey = response.nodes[leaf.leafId].verifyingPublicKey;

      const taprootKey = computeTaprootKeyNoScript(nodePublicKey.slice(1));
      const adaptorSignatureBytes = hexToBytes(leaf.adaptorSignedSignature);
      applyAdaptorToSignature(
        taprootKey.slice(1),
        sighash,
        adaptorSignatureBytes,
        adaptorPrivateKey
      );
    }

    sparkClient.close?.();

    await this.transferService.sendTransferTweakKey(
      transfer,
      leafKeyTweaks,
      signatureMap
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
  }

  // Lightning
  async createLightningInvoice({
    amountSats,
    memo,
    expirySeconds,
    // TODO: This should default to lightspark ssp
    invoiceCreator = () => Promise.resolve(""),
  }: CreateLightningInvoiceParams) {
    if (!this.sspClient) {
      throw new Error("SSP client not initialized");
    }

    const requestLightningInvoice = async (
      amountSats: number,
      paymentHash: Uint8Array,
      memo: string
    ) => {
      const invoice = await this.sspClient!.requestLightningReceive({
        amountSats,
        // TODO: Map config network to ssp network
        network: BitcoinNetwork.REGTEST,
        paymentHash: bytesToHex(paymentHash),
        expirySecs: expirySeconds,
        memo,
      });

      return invoice?.invoice.encodedEnvoice;
    };

    return this.lightningService!.createLightningInvoice({
      amountSats,
      memo,
      invoiceCreator: requestLightningInvoice,
    });
  }

  async payLightningInvoice({
    invoice,
    amountSats,
  }: PayLightningInvoiceParams) {
    if (!this.sspClient) {
      throw new Error("SSP client not initialized");
    }

    // TODO: Get fee

    const decodedInvoice = decode(invoice);
    amountSats =
      Number(
        decodedInvoice.sections.find((section) => section.name === "amount")
          ?.value
      ) / 1000;

    if (isNaN(amountSats) || amountSats <= 0) {
      throw new Error("Invalid amount");
    }

    const paymentHash = decodedInvoice.sections.find(
      (section) => section.name === "payment_hash"
    )?.value;

    if (!paymentHash) {
      throw new Error("No payment hash found in invoice");
    }

    // fetch leaves for amount

    const leaves = await this.selectLeaves(amountSats);

    const leavesToSend = await Promise.all(
      leaves.map(async (leaf) => ({
        leaf,
        signingPubKey: await this.config.signer.generatePublicKey(
          sha256(leaf.id)
        ),
        newSigningPubKey: await this.config.signer.generatePublicKey(),
      }))
    );

    const swapResponse = await this.lightningService.swapNodesForPreimage({
      leaves: leavesToSend,
      receiverIdentityPubkey:
        await this.config.signer.getSspIdentityPublicKey(),
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
      new Map()
    );

    const sspResponse = await this.sspClient.requestLightningSend({
      encodedInvoice: invoice,
      idempotencyKey: paymentHash,
    });

    if (!sspResponse) {
      throw new Error("Failed to contact SSP");
    }

    return sspResponse;
  }

  async getLightningReceiveFeeEstimate({
    amountSats,
    network,
  }: LightningReceiveFeeEstimateInput): Promise<LightningReceiveFeeEstimateOutput | null> {
    if (!this.sspClient) {
      throw new Error("SSP client not initialized");
    }

    return await this.sspClient.getLightningReceiveFeeEstimate(
      amountSats,
      network
    );
  }

  async getLightningSendFeeEstimate({
    encodedInvoice,
  }: LightningSendFeeEstimateInput): Promise<LightningSendFeeEstimateOutput | null> {
    if (!this.sspClient) {
      throw new Error("SSP client not initialized");
    }

    return await this.sspClient.getLightningSendFeeEstimate(encodedInvoice);
  }

  async getCoopExitFeeEstimate({
    leafExternalIds,
    withdrawalAddress,
  }: CoopExitFeeEstimateInput): Promise<CoopExitFeeEstimateOutput | null> {
    if (!this.sspClient) {
      throw new Error("SSP client not initialized");
    }

    return await this.sspClient.getCoopExitFeeEstimate({
      leafExternalIds,
      withdrawalAddress,
    });
  }

  async setLeaves(leaves: TreeNode[]) {
    this.leaves = leaves;
  }

  async transferDepositToSelf(leaves: TreeNode[], signingPubKey: Uint8Array) {
    const leafKeyTweaks = await Promise.all(
      leaves.map(async (leaf) => ({
        leaf,
        signingPubKey,
        newSigningPubKey: await this.config.signer.generatePublicKey(),
      }))
    );

    await this.transferService.sendTransfer(
      leafKeyTweaks,
      await this.config.signer.getIdentityPublicKey(),
      new Date(Date.now() + 10 * 60 * 1000)
    );

    const pendingTransfers = await this.queryPendingTransfers();
    if (pendingTransfers.transfers.length > 0) {
      await this.claimTransfer(pendingTransfers.transfers[0]);
    }
  }

  async sendTransfer({
    amount,
    receiverPubKey,
    leaves,
    expiryTime = new Date(Date.now() + 10 * 60 * 1000),
  }: SendTransferParams) {
    let leavesToSend: TreeNode[] = [];
    if (leaves) {
      leavesToSend = leaves.map((leaf) => ({
        ...leaf,
      }));
    } else if (amount) {
      leavesToSend = await this.selectLeaves(amount);
    } else {
      throw new Error("Must provide amount or leaves");
    }

    const leafKeyTweaks = await Promise.all(
      leavesToSend.map(async (leaf) => ({
        leaf,
        signingPubKey: await this.config.signer.generatePublicKey(
          sha256(leaf.id)
        ),
        newSigningPubKey: await this.config.signer.generatePublicKey(),
      }))
    );

    return await this.transferService.sendTransfer(
      leafKeyTweaks,
      receiverPubKey,
      expiryTime
    );
  }

  async queryPendingTransfers(): Promise<QueryPendingTransfersResponse> {
    return await this.transferService.queryPendingTransfers();
  }

  async claimTransfer(transfer: Transfer) {
    const leafPubKeyMap = await this.transferService.verifyPendingTransfer(
      transfer
    );

    let leavesToClaim: LeafKeyTweak[] = [];

    for (const leaf of transfer.leaves) {
      if (leaf.leaf) {
        const leafPubKey = leafPubKeyMap.get(leaf.leaf.id);
        if (leafPubKey) {
          leavesToClaim.push({
            leaf: leaf.leaf,
            signingPubKey: leafPubKey,
            newSigningPubKey: await this.config.signer.generatePublicKey(
              sha256(leaf.leaf.id)
            ),
          });
        }
      }
    }

    return await this.transferService.claimTransfer(transfer, leavesToClaim);
  }

  async claimTransfers() {
    const transfers = await this.queryPendingTransfers();
    for (const transfer of transfers.transfers) {
      if (
        transfer.status !== TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED
      ) {
        continue;
      }
      await this.claimTransfer(transfer);
    }
  }

  async coopExit(onchainAddress: string, targetAmountSats?: number) {
    let leavesToSend: TreeNode[] = [];
    if (targetAmountSats) {
      leavesToSend = await this.selectLeaves(targetAmountSats);
    } else {
      leavesToSend = this.leaves.map((leaf) => ({
        ...leaf,
      }));
    }

    const leafKeyTweaks = await Promise.all(
      leavesToSend.map(async (leaf) => ({
        leaf,
        signingPubKey: await this.config.signer.generatePublicKey(
          sha256(leaf.id)
        ),
        newSigningPubKey: await this.config.signer.generatePublicKey(),
      }))
    );

    const coopExitRequest = await this.sspClient?.requestCoopExit({
      leafExternalIds: leavesToSend.map((leaf) => leaf.id),
      withdrawalAddress: onchainAddress,
    });

    if (!coopExitRequest?.rawConnectorTransaction) {
      throw new Error("Failed to request coop exit");
    }

    const connectorTx = getTxFromRawTxHex(
      coopExitRequest.rawConnectorTransaction
    );
    const coopExitTxId = getTxId(connectorTx);

    const connectorOutputs: TransactionInput[] = [];
    for (let i = 0; i < connectorTx.outputsLength - 1; i++) {
      connectorOutputs.push({
        txid: hexToBytes(coopExitTxId),
        index: i,
      });
    }

    const sspPubIdentityKey =
      await this.config.signer.getSspIdentityPublicKey();

    const transfer = await this.coopExitService.getConnectorRefundSignatures({
      leaves: leafKeyTweaks,
      exitTxId: hexToBytes(coopExitTxId),
      connectorOutputs,
      receiverPubKey: sspPubIdentityKey,
    });

    const completeResponse = await this.sspClient?.completeCoopExit({
      userOutboundTransferExternalId: transfer.transfer.id,
      coopExitRequestId: coopExitRequest.id,
    });

    return completeResponse;
  }

  // TODO: Remove this
  async _sendTransfer(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    expiryTime: Date
  ): Promise<Transfer> {
    return await this.transferService!.sendTransfer(
      leaves,
      receiverIdentityPubkey,
      expiryTime
    );
  }

  // TODO: Remove this
  async _claimTransfer(transfer: Transfer, leaves: LeafKeyTweak[]) {
    return await this.transferService!.claimTransfer(transfer, leaves);
  }

  async getLeaves(): Promise<TreeNode[]> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress()
    );
    const leaves = await sparkClient.query_nodes({
      source: {
        $case: "ownerIdentityPubkey",
        ownerIdentityPubkey: await this.config.signer.getIdentityPublicKey(),
      },
      includeParents: true,
    });
    sparkClient.close?.();
    return Object.entries(leaves.nodes)
      .filter(([_, node]) => node.status === "AVAILABLE")
      .map(([_, node]) => node);
  }

  async getBalance(): Promise<BigInt> {
    const leaves = await this.getLeaves();
    return leaves.reduce((acc, leaf) => acc + BigInt(leaf.value), 0n);
  }

  async verifyPendingTransfer(
    transfer: Transfer
  ): Promise<Map<string, Uint8Array>> {
    return await this.transferService!.verifyPendingTransfer(transfer);
  }

  // **** Deposit Flow ****
  async generateDepositAddress(
    signingPubkey: Uint8Array
  ): Promise<GenerateDepositAddressResponse> {
    return await this.depositService!.generateDepositAddress({ signingPubkey });
  }

  async syncTokenLeaves() {
    await this.tokenTransactionService.syncTokenLeaves(this.tokenLeaves);
  }

  getTokenBalance(tokenPublicKey: Uint8Array) {
    return calculateAvailableTokenAmount(
      this.tokenLeaves.get(bytesToHex(tokenPublicKey))!
    );
  }

  async mintTokens(tokenPublicKey: Uint8Array, tokenAmount: bigint) {
    const tokenTransaction =
      this.tokenTransactionService.createMintTokenTransaction(
        tokenPublicKey,
        tokenAmount
      );

    const finalizedTokenTransaction =
      await this.tokenTransactionService.broadcastTokenTransaction(
        tokenTransaction
      );

    const tokenPubKeyHex = bytesToHex(tokenPublicKey);
    if (!this.tokenLeaves.has(tokenPubKeyHex)) {
      this.tokenLeaves.set(tokenPubKeyHex, []);
    }
    this.tokenTransactionService.updateTokenLeavesFromFinalizedTransaction(
      this.tokenLeaves.get(tokenPubKeyHex)!,
      finalizedTokenTransaction
    );
  }

  async transferTokens(
    tokenPublicKey: Uint8Array,
    tokenAmount: bigint,
    recipientPublicKey: Uint8Array,
    selectedLeaves?: LeafWithPreviousTransactionData[]
  ) {
    if (!this.tokenLeaves.has(bytesToHex(tokenPublicKey))) {
      throw new Error("No token leaves with the given tokenPublicKey");
    }

    if (selectedLeaves) {
      if (
        !checkIfSelectedLeavesAreAvailable(
          selectedLeaves,
          this.tokenLeaves,
          tokenPublicKey
        )
      ) {
        throw new Error("One or more selected leaves are not available");
      }
    } else {
      selectedLeaves = this.selectTokenLeaves(tokenPublicKey, tokenAmount);
    }

    const tokenTransaction =
      this.tokenTransactionService.createTransferTokenTransaction(
        selectedLeaves,
        recipientPublicKey,
        tokenPublicKey,
        tokenAmount
      );

    const finalizedTokenTransaction =
      await this.tokenTransactionService.broadcastTokenTransaction(
        tokenTransaction,
        selectedLeaves.map((leaf) => leaf.leaf!.ownerPublicKey),
        selectedLeaves.map((leaf) => leaf.leaf!.revocationPublicKey!)
      );

    const tokenPubKeyHex = bytesToHex(tokenPublicKey);
    if (!this.tokenLeaves.has(tokenPubKeyHex)) {
      this.tokenLeaves.set(tokenPubKeyHex, []);
    }
    this.tokenTransactionService.updateTokenLeavesFromFinalizedTransaction(
      this.tokenLeaves.get(tokenPubKeyHex)!,
      finalizedTokenTransaction
    );
  }

  async burnTokens(
    tokenPublicKey: Uint8Array,
    tokenAmount: bigint,
    selectedLeaves?: LeafWithPreviousTransactionData[]
  ) {
    if (!this.tokenLeaves.has(bytesToHex(tokenPublicKey))) {
      throw new Error("No token leaves with the given tokenPublicKey");
    }

    if (selectedLeaves) {
      if (
        !checkIfSelectedLeavesAreAvailable(
          selectedLeaves,
          this.tokenLeaves,
          tokenPublicKey
        )
      ) {
        throw new Error("One or more selected leaves are not available");
      }
    } else {
      selectedLeaves = this.selectTokenLeaves(tokenPublicKey, tokenAmount);
    }

    const partialTokenTransaction =
      await this.tokenTransactionService.constructBurnTokenTransaction(
        tokenPublicKey,
        tokenAmount,
        selectedLeaves
      );

    const finalizedTokenTransaction =
      await this.tokenTransactionService.broadcastTokenTransaction(
        partialTokenTransaction,
        selectedLeaves.map((leaf) => leaf.leaf!.ownerPublicKey),
        selectedLeaves.map((leaf) => leaf.leaf!.revocationPublicKey!)
      );

    const tokenPubKeyHex = bytesToHex(tokenPublicKey);
    if (!this.tokenLeaves.has(tokenPubKeyHex)) {
      this.tokenLeaves.set(tokenPubKeyHex, []);
    }
    this.tokenTransactionService.updateTokenLeavesFromFinalizedTransaction(
      this.tokenLeaves.get(tokenPubKeyHex)!,
      finalizedTokenTransaction
    );
  }

  async freezeTokens(ownerPublicKey: Uint8Array, tokenPublicKey: Uint8Array) {
    await this.tokenFreezeService!.freezeTokens(ownerPublicKey, tokenPublicKey);
  }

  async unfreezeTokens(ownerPublicKey: Uint8Array, tokenPublicKey: Uint8Array) {
    await this.tokenFreezeService!.unfreezeTokens(
      ownerPublicKey,
      tokenPublicKey
    );
  }

  selectTokenLeaves(
    tokenPublicKey: Uint8Array,
    tokenAmount: bigint
  ): LeafWithPreviousTransactionData[] {
    return this.tokenTransactionService.selectTokenLeaves(
      this.tokenLeaves.get(bytesToHex(tokenPublicKey))!,
      tokenPublicKey,
      tokenAmount
    );
  }

  // If no leaves are passed in, it will take all the leaves available for the given tokenPublicKey
  async consolidateTokenLeaves(
    tokenPublicKey: Uint8Array,
    selectedLeaves?: LeafWithPreviousTransactionData[]
  ) {
    if (!this.tokenLeaves.has(bytesToHex(tokenPublicKey))) {
      throw new Error("No token leaves with the given tokenPublicKey");
    }

    if (selectedLeaves) {
      if (
        !checkIfSelectedLeavesAreAvailable(
          selectedLeaves,
          this.tokenLeaves,
          tokenPublicKey
        )
      ) {
        throw new Error("One or more selected leaves are not available");
      }
    } else {
      // Get all available leaves
      selectedLeaves = this.tokenLeaves.get(bytesToHex(tokenPublicKey))!;
    }

    if (selectedLeaves!.length === 1) {
      return;
    }

    const partialTokenTransaction =
      await this.tokenTransactionService.constructConsolidateTokenTransaction(
        tokenPublicKey,
        selectedLeaves
      );

    const finalizedTokenTransaction =
      await this.tokenTransactionService.broadcastTokenTransaction(
        partialTokenTransaction,
        selectedLeaves.map((leaf) => leaf.leaf!.ownerPublicKey),
        selectedLeaves.map((leaf) => leaf.leaf!.revocationPublicKey!)
      );

    const tokenPubKeyHex = bytesToHex(tokenPublicKey);
    if (!this.tokenLeaves.has(tokenPubKeyHex)) {
      this.tokenLeaves.set(tokenPubKeyHex, []);
    }
    this.tokenTransactionService.updateTokenLeavesFromFinalizedTransaction(
      this.tokenLeaves.get(tokenPubKeyHex)!,
      finalizedTokenTransaction
    );
  }

  async queryPendingDepositTx(depositAddress: string) {
    const {
      bitcoin: { addresses, transactions },
    } = mempoolJS({
      hostname: "regtest-mempool.dev.dev.sparkinfra.net",
      protocol: "https",
      config: {
        auth: {
          username: "lightspark",
          password: "TFNR6ZeLdxF9HejW",
        },
        headers: {
          "Content-Type": "application/json",
        },
      },
      network: "regtest",
    });

    try {
      const addressTxs = await addresses.getAddressTxs({
        address: depositAddress,
      });

      if (addressTxs && addressTxs.length > 0) {
        const latestTx = addressTxs[0];

        // // Find our output
        const outputIndex = latestTx.vout.findIndex(
          (output: any) => output.scriptpubkey_address === depositAddress
        );

        if (outputIndex === -1) {
          return null;
        }

        const txHex = await transactions.getTxHex({ txid: latestTx.txid });
        const depositTx = getTxFromRawTxHex(txHex);

        return { depositTx, vout: outputIndex };
      }
      return null;
    } catch (error) {
      throw error;
    }
  }

  async createTreeRoot(
    signingPubKey: Uint8Array,
    verifyingKey: Uint8Array,
    depositTx: Transaction,
    vout: number
  ) {
    return await this.depositService!.createTreeRoot({
      signingPubKey,
      verifyingKey,
      depositTx,
      vout,
    });
  }
  // **********************

  // **** Tree Creation Flow ****
  async generateDepositAddressForTree(
    vout: number,
    parentSigningPubKey: Uint8Array,
    parentTx?: Transaction,
    parentNode?: TreeNode
  ) {
    return await this.treeCreationService!.generateDepositAddressForTree(
      vout,
      parentSigningPubKey,
      parentTx,
      parentNode
    );
  }

  async createTree(
    vout: number,
    root: DepositAddressTree,
    createLeaves: boolean,
    parentTx?: Transaction,
    parentNode?: TreeNode
  ) {
    return await this.treeCreationService!.createTree(
      vout,
      root,
      createLeaves,
      parentTx,
      parentNode
    );
  }
  // **********************
}
