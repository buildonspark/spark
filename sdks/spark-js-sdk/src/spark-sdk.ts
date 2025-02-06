import { Transaction } from "@scure/btc-signer";
import {
  GenerateDepositAddressResponse,
  QueryPendingTransfersResponse,
  Transfer,
  TreeNode,
} from "./proto/spark";
import { initWasm } from "./utils/wasm-wrapper";
import { InitOutput } from "./wasm/spark_bindings";

import { WalletConfig, WalletConfigService } from "./services/config";
import { ConnectionManager } from "./services/connection";
import { DepositService } from "./services/deposit";
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

import { LightningService } from "./services/lightning";
import { SparkSigner } from "./signer/signer";
import { getP2TRAddressFromPublicKey } from "./utils/bitcoin";
import { Network } from "./utils/network";

type CreateLightningInvoiceParams = {
  amountSats: number;
  expirySeconds: number;
  memo: string;
  invoiceCreator?: () => Promise<string>;
};
export class SparkWallet {
  private config: WalletConfigService;

  private connectionManager: ConnectionManager;
  private depositService: DepositService;
  private transferService: TransferService;
  private treeCreationService: TreeCreationService;
  private lightningService: LightningService;

  private wasmModule: InitOutput | null = null;

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
    this.lightningService = new LightningService(
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

  getMasterPubKey(): Uint8Array {
    return this.config.signer.getIdentityPublicKey();
  }

  getP2trAddress(): string {
    const pubKey = this.config.signer.getIdentityPublicKey();
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

  generateMnemonic(): string {
    return this.config.signer.generateMnemonic();
  }

  // TODO: Update to use config based on options
  async createSparkWallet(mnemonic: string): Promise<string> {
    await this.initWasm();
    return this.config.signer.createSparkWalletFromMnemonic(mnemonic);
  }

  async createSparkWalletFromSeed(seed: Uint8Array | string): Promise<string> {
    await this.initWasm();
    return this.config.signer.createSparkWalletFromSeed(seed);
  }

  // Lightning
  async createLightningInvoice({
    amountSats,
    expirySeconds,
    memo,
    // TODO: This should default to lightspark ssp
    invoiceCreator = () => Promise.resolve(""),
  }: CreateLightningInvoiceParams) {
    return this.lightningService!.createLightningInvoice({
      amountSats,
      memo,
      invoiceCreator,
    });
  }

  async payLightningInvoice() {
    throw new Error("Not implemented");
  }

  async getLightningPaymentFees() {
    throw new Error("Not implemented");
  }

  async initiateDeposit() {
    throw new Error("Not implemented");
  }

  async completeDeposit() {
    throw new Error("Not implemented");
  }

  async _sendTransfer() {
    throw new Error("Not implemented");
  }

  async _claimTransfer() {
    throw new Error("Not implemented");
  }

  async coopExit() {
    throw new Error("Not implemented");
  }

  async sendTransfer(
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

  async claimTransfer(transfer: Transfer, leaves: LeafKeyTweak[]) {
    return await this.transferService!.claimTransfer(transfer, leaves);
  }

  async queryPendingTransfers(): Promise<QueryPendingTransfersResponse> {
    return await this.transferService!.queryPendingTransfers();
  }

  async verifyPendingTransfer(
    transfer: Transfer
  ): Promise<Map<string, Uint8Array>> {
    return await this.transferService!.verifyPendingTransfer(transfer);
  }

  async generateDepositAddress(
    signingPubkey: Uint8Array
  ): Promise<GenerateDepositAddressResponse> {
    return await this.depositService!.generateDepositAddress({ signingPubkey });
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
}
