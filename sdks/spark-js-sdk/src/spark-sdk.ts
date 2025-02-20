import { Transaction } from "@scure/btc-signer";
import {
  GenerateDepositAddressResponse,
  QueryPendingTransfersResponse,
  Transfer,
  TreeNode,
  LeafWithPreviousTransactionData,
  FreezeTokensResponse,
} from "./proto/spark";
import { initWasm } from "./utils/wasm-wrapper";
import { InitOutput } from "./wasm/spark_bindings";

import { TokenTransaction } from "./proto/spark";
import { WalletConfig, WalletConfigService } from "./services/config";
import { ConnectionManager } from "./services/connection";
import { DepositService } from "./services/deposit";
import { TokenTransactionService } from "./services/tokens-transaction";
import { LeafKeyTweak, TransferService } from "./services/transfer";
import {
  DepositAddressTree,
  TreeCreationService,
} from "./services/tree-creation";
import {
  aggregateFrost,
  AggregateFrostParams,
  signFrost,
  SignFrostParams,
} from "./utils/wasm";

import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
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
} from "./graphql/objects";
import { CoopExitService } from "./services/coop-exit";
import { LightningService } from "./services/lightning";
import { SparkSigner } from "./signer/signer";
import { generateAdaptorFromSignature } from "./utils/adaptor-signature";
import {
  getP2TRAddressFromPublicKey,
  getTxFromRawTxHex,
} from "./utils/bitcoin";
import { LeafNode, selectLeaves } from "./utils/leaf-selection";
import { Network } from "./utils/network";
import {
  calculateAvailableTokenAmount,
  checkIfSelectedLeavesAreAvailable,
} from "./utils/token-transactions";
import { TokenFreezeService } from "./services/tokens-freeze";

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

  async requestLeavesSwap(targetAmount: number) {
    await this.claimTransfers();
    const leaves = await this.getLeaves();

    const leavesToSwap = selectLeaves(
      leaves.map((leaf) => ({ ...leaf, isUsed: false })),
      targetAmount
    );

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

    const request = await this.sspClient?.requestLeaveSwap({
      adaptorPubkey: bytesToHex(secp256k1.getPublicKey(adaptorPrivateKey)),
      targetAmountSats: targetAmount,
      totalAmountSats: leavesToSwap.reduce((acc, leaf) => acc + leaf.value, 0),
      // TODO: Request fee from SSP
      feeSats: 0,
      // TODO: Map config network to proto network
      network: BitcoinNetwork.REGTEST,
    });

    if (!request) {
      throw new Error("Failed to request leaves swap");
    }

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
    const leaves = selectLeaves(
      this.leaves.map((leaf) => ({ ...leaf, isUsed: false })),
      amountSats
    );

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

  async sendTransfer({
    amount,
    receiverPubKey,
    leaves,
    expiryTime = new Date(Date.now() + 10 * 60 * 1000),
  }: SendTransferParams) {
    let leavesToSend: LeafNode[] = [];
    if (leaves) {
      leavesToSend = leaves.map((leaf) => ({
        ...leaf,
        isUsed: true,
      }));
    } else if (amount) {
      leavesToSend = selectLeaves(
        this.leaves.map((leaf) => ({ ...leaf, isUsed: false })),
        amount
      );
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
      await this.claimTransfer(transfer);
    }
  }

  async coopExit(onchainAddress: string, targetAmountSats?: number) {
    let leavesToSend: LeafNode[] = [];
    if (targetAmountSats) {
      leavesToSend = selectLeaves(
        this.leaves.map((leaf) => ({ ...leaf, isUsed: false })),
        targetAmountSats
      );
    } else {
      leavesToSend = this.leaves.map((leaf) => ({
        ...leaf,
        isUsed: true,
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
    const coopExitTxId = connectorTx.id;

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
