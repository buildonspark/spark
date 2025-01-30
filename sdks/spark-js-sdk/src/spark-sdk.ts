import {
  GenerateDepositAddressResponse,
  QueryPendingTransfersResponse,
  Transfer,
  TreeNode,
} from "./proto/spark";
import { initWasm } from "./utils/wasm-wrapper";
import { InitOutput } from "./wasm/spark_bindings";
import { Transaction } from "@scure/btc-signer";

import { WalletConfig, WalletConfigService } from "./services/config";
import { DepositService } from "./services/deposit";
import {
  DepositAddressTree,
  TreeCreationService,
} from "./services/tree-creation";
import { LeafKeyTweak, TransferService } from "./services/transfer";
import { ConnectionManager } from "./services/connection";
import {
  AggregateFrostParams,
  SignFrostParams,
  aggregateFrost,
  signFrost,
} from "./utils/wasm";

export class SparkWallet {
  readonly config: WalletConfigService;
  private connectionManager: ConnectionManager;
  private wasmModule: InitOutput | null = null;

  private depositService: DepositService;
  private transferService: TransferService;
  private treeCreationService: TreeCreationService;

  constructor(config: WalletConfig) {
    this.config = new WalletConfigService(config);
    this.connectionManager = new ConnectionManager();

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
    this.init();
  }

  private async init() {
    this.wasmModule = await initWasm();
  }

  private async ensureInitialized() {
    if (!this.wasmModule) {
      await this.init();
    }
  }

  async signFrost(params: SignFrostParams): Promise<Uint8Array> {
    await this.ensureInitialized();
    return signFrost(params);
  }

  async aggregateFrost(params: AggregateFrostParams): Promise<Uint8Array> {
    await this.ensureInitialized();
    return aggregateFrost(params);
  }

  async sendTransfer(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    expiryTime: Date
  ): Promise<Transfer> {
    return await this.transferService.sendTransfer(
      leaves,
      receiverIdentityPubkey,
      expiryTime
    );
  }

  async claimTransfer(transfer: Transfer, leaves: LeafKeyTweak[]) {
    return await this.transferService.claimTransfer(transfer, leaves);
  }

  async queryPendingTransfers(): Promise<QueryPendingTransfersResponse> {
    return await this.transferService.queryPendingTransfers();
  }

  async verifyPendingTransfer(
    transfer: Transfer
  ): Promise<Map<string, Uint8Array>> {
    return await this.transferService.verifyPendingTransfer(transfer);
  }

  async generateDepositAddress(
    signingPubkey: Uint8Array
  ): Promise<GenerateDepositAddressResponse> {
    return await this.depositService.generateDepositAddress({ signingPubkey });
  }

  async createTreeRoot(
    signingPrivkey: Uint8Array,
    verifyingKey: Uint8Array,
    depositTx: Transaction,
    vout: number
  ) {
    await this.ensureInitialized();
    return await this.depositService.createTreeRoot({
      signingPrivkey,
      verifyingKey,
      depositTx,
      vout,
    });
  }

  async generateDepositAddressForTree(
    vout: number,
    parentSigningPrivKey: Uint8Array,
    parentTx?: Transaction,
    parentNode?: TreeNode
  ) {
    return await this.treeCreationService.generateDepositAddressForTree(
      vout,
      parentSigningPrivKey,
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
    return await this.treeCreationService.createTree(
      vout,
      root,
      createLeaves,
      parentTx,
      parentNode
    );
  }
}
