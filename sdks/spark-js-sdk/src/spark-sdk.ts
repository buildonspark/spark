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

import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { HDKey } from "@scure/bip32";
import * as bip39 from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import assert from "assert";
import { TEST_WALLET_CONFIG } from "./tests/test-util";
import { Network } from "./utils/network";

export class SparkWallet {
  private config: WalletConfigService | null = null;
  private connectionManager: ConnectionManager | null = null;
  private wasmModule: InitOutput | null = null;

  private depositService: DepositService | null = null;
  private transferService: TransferService | null = null;
  private treeCreationService: TreeCreationService | null = null;

  constructor() {}

  private async initWallet(config: WalletConfig) {
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
    this.initWasm();
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

  getConfig(): WalletConfig {
    if (!this.config) {
      throw new Error("Wallet not initialized. Call createSparkWallet first.");
    }
    return this.config.getConfig();
  }

  getMasterPubKey(): Uint8Array {
    if (!this.config) {
      throw new Error("Wallet not initialized. Call createSparkWallet first.");
    }
    return this.config.getIdentityPublicKey();
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
    return bip39.generateMnemonic(wordlist);
  }

  // TODO: Update to use config based on options
  async createSparkWallet(
    mnemonic: string,
    options: { network: Network } = { network: "regtest" }
  ): Promise<string> {
    const seed = bip39.mnemonicToSeedSync(mnemonic);

    return await this.createSparkWalletFromSeed(seed);
  }

  async createSparkWalletFromSeed(seed: Uint8Array | string): Promise<string> {
    if (typeof seed === "string") {
      seed = hexToBytes(seed);
    }

    const hdkey = HDKey.fromMasterSeed(seed);

    assert(hdkey.privateKey, "Private key is not set");

    const config: WalletConfig = {
      ...TEST_WALLET_CONFIG,
      identityPrivateKey: hdkey.privateKey,
    } as WalletConfig;

    await this.initWallet(config);

    return bytesToHex(secp256k1.getPublicKey(hdkey.privateKey, true));
  }

  private async ensureWalletInitialized() {
    if (
      !this.config ||
      !this.connectionManager ||
      !this.transferService ||
      !this.depositService ||
      !this.treeCreationService
    ) {
      throw new Error("Wallet not initialized. Call createSparkWallet first.");
    }
    await this.ensureInitialized();
  }

  async sendTransfer(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    expiryTime: Date
  ): Promise<Transfer> {
    await this.ensureWalletInitialized();
    return await this.transferService!.sendTransfer(
      leaves,
      receiverIdentityPubkey,
      expiryTime
    );
  }

  async claimTransfer(transfer: Transfer, leaves: LeafKeyTweak[]) {
    await this.ensureWalletInitialized();
    return await this.transferService!.claimTransfer(transfer, leaves);
  }

  async queryPendingTransfers(): Promise<QueryPendingTransfersResponse> {
    await this.ensureWalletInitialized();
    return await this.transferService!.queryPendingTransfers();
  }

  async verifyPendingTransfer(
    transfer: Transfer
  ): Promise<Map<string, Uint8Array>> {
    await this.ensureWalletInitialized();
    return await this.transferService!.verifyPendingTransfer(transfer);
  }

  async generateDepositAddress(
    signingPubkey: Uint8Array
  ): Promise<GenerateDepositAddressResponse> {
    await this.ensureWalletInitialized();
    return await this.depositService!.generateDepositAddress({ signingPubkey });
  }

  async createTreeRoot(
    signingPrivkey: Uint8Array,
    verifyingKey: Uint8Array,
    depositTx: Transaction,
    vout: number
  ) {
    await this.ensureWalletInitialized();
    return await this.depositService!.createTreeRoot({
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
    await this.ensureWalletInitialized();
    return await this.treeCreationService!.generateDepositAddressForTree(
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
    await this.ensureWalletInitialized();
    return await this.treeCreationService!.createTree(
      vout,
      root,
      createLeaves,
      parentTx,
      parentNode
    );
  }
}
