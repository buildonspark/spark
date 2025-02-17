import { Transaction } from "@scure/btc-signer";
import {
  GenerateDepositAddressResponse,
  QueryPendingTransfersResponse,
  Transfer,
  TreeNode,
} from "./proto/spark";
import { initWasm } from "./utils/wasm-wrapper";
import { InitOutput } from "./wasm/spark_bindings";

import { TokenTransaction } from "./proto/spark";
import { WalletConfig, WalletConfigService } from "./services/config";
import { ConnectionManager } from "./services/connection";
import { DepositService } from "./services/deposit";
import { TokenTransactionService } from "./services/tokens";
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

import { bytesToHex } from "@noble/curves/abstract/utils";
import { sha256 } from "@scure/btc-signer/utils";
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
import { getP2TRAddressFromPublicKey } from "./utils/bitcoin";
import { selectLeaves } from "./utils/leaf-selection";
import { Network } from "./utils/network";

type CreateLightningInvoiceParams = {
  amountSats: number;
  expirySeconds: number;
  memo: string;
  invoiceCreator?: () => Promise<string>;
};

type PayLightningInvoiceParams = {
  invoice: string;
  idempotencyKey: string;
  amountSats: number;
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

  private sspClient: SspClient | null = null;
  private wasmModule: InitOutput | null = null;

  private leaves: TreeNode[] = [];

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
      this.config.signer.createSparkWalletFromSeed(seed);
    await this.initializeWallet(identityPublicKey);
    return identityPublicKey;
  }

  private async initializeWallet(identityPublicKey: string) {
    this.sspClient = new SspClient(identityPublicKey);
    await this.initWasm();
    // TODO: Better leaf management?
    this.leaves = await this.getLeaves();
    this.config.signer.restoreSigningKeysFromLeafs(this.leaves);
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
    ) =>
      (
        await this.sspClient!.requestLightningReceive({
          amountSats,
          // TODO: Map config network to ssp network
          network: BitcoinNetwork.REGTEST,
          paymentHash: bytesToHex(paymentHash),
          expirySecs: expirySeconds,
          memo,
        })
      )?.invoice.encodedEnvoice;

    return this.lightningService!.createLightningInvoice({
      amountSats,
      memo,
      invoiceCreator: requestLightningInvoice,
    });
  }

  async payLightningInvoice({
    invoice,
    idempotencyKey,
    amountSats,
  }: PayLightningInvoiceParams) {
    if (!this.sspClient) {
      throw new Error("SSP client not initialized");
    }

    const sspResponse = await this.sspClient.requestLightningSend({
      encodedInvoice: invoice,
      idempotencyKey,
    });

    if (!sspResponse) {
      throw new Error("Failed to contact SSP");
    }

    // fetch leaves for amount
    const leaves = selectLeaves(
      this.leaves.map((leaf) => ({ ...leaf, isUsed: false })),
      amountSats
    );

    const leavesToSend = leaves.map((leaf) => ({
      leaf,
      signingPubKey: this.config.signer.generatePublicKey(sha256(leaf.id)),
      newSigningPubKey: this.config.signer.generatePublicKey(),
    }));

    const swapResponse = await this.lightningService.swapNodesForPreimage({
      leaves: leavesToSend,
      receiverIdentityPubkey: this.config.signer.getSspIdentityPublicKey(),
      paymentHash: this.config.signer.hashRandomPrivateKey(),
      isInboundPayment: false,
      invoiceString: sspResponse.encodedInvoice,
    });

    if (!swapResponse.transfer) {
      throw new Error("Failed to swap nodes for preimage");
    }

    const transfer = await this.transferService.sendTransferTweakKey(
      swapResponse.transfer,
      leavesToSend,
      new Map()
    );

    return transfer;
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

  async sendTransfer(
    amount: number,
    receiverPubKey: Uint8Array,
    expiryTime: Date = new Date(Date.now() + 10 * 60 * 1000)
  ) {
    const leaves = selectLeaves(
      this.leaves.map((leaf) => ({ ...leaf, isUsed: false })),
      amount
    );

    const leavesToSend = leaves.map((leaf) => ({
      leaf,
      signingPubKey: this.config.signer.generatePublicKey(sha256(leaf.id)),
      newSigningPubKey: this.config.signer.generatePublicKey(),
    }));

    await this.transferService.sendTransfer(
      leavesToSend,
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

    const leavesToClaim = transfer.leaves.flatMap((leaf) => {
      if (leaf.leaf) {
        const leafPubKey = leafPubKeyMap.get(leaf.leaf.id);
        if (leafPubKey) {
          return [
            {
              leaf: leaf.leaf,
              signingPubKey: leafPubKey,
              newSigningPubKey: this.config.signer.generatePublicKey(
                sha256(leaf.leaf.id)
              ),
            },
          ];
        }
      }
      return [];
    });

    return await this.transferService.claimTransfer(transfer, leavesToClaim);
  }

  async claimTransfers() {
    const transfers = await this.queryPendingTransfers();
    for (const transfer of transfers.transfers) {
      await this.claimTransfer(transfer);
    }
  }

  async coopExit() {
    const leavesToSend = this.leaves.map((leaf) => ({
      leaf,
      signingPubKey: this.config.signer.generatePublicKey(sha256(leaf.id)),
      newSigningPubKey: this.config.signer.generatePublicKey(),
    }));

    // const leaves = this.config.signer.getLeafKeyTweaks(this.leaves);

    // TODO: Might need a differnet ID for this
    const withdrawPubKey = this.config.signer.generatePublicKey(
      sha256(leavesToSend[0].leaf.treeId)
    );

    const withdrawAddress = getP2TRAddressFromPublicKey(
      withdrawPubKey,
      this.config.getNetwork()
    );

    const amountSats = leavesToSend.reduce(
      (acc, leaf) => acc + BigInt(leaf.leaf.value),
      0n
    );

    // // get/create exit tx from where?
    // const dummyTx = createDummyTx({
    //   address: withdrawAddress,
    //   amountSats,
    // });
    // const exitTx = getTxFromRawTxBytes(dummyTx.tx);

    // const dustAmountSats = 354;
    // const intermediateAmountSats = (leaves.length + 1) * dustAmountSats;

    // const sspIntermediateAddressScript = getP2TRScriptFromPublicKey(
    //   withdrawPubKey, // Should be ssp pubkey
    //   this.config.getNetwork()
    // );

    // exitTx.addOutput({
    //   script: sspIntermediateAddressScript,
    //   amount: BigInt(intermediateAmountSats),
    // });
    // // end

    // const intermediateInput: TransactionInput = {
    //   txid: hexToBytes(getTxId(exitTx)),
    //   index: 1,
    // };

    // let connectorP2trAddrs: string[] = [];
    // for (let i = 0; i < leaves.length + 1; i++) {
    //   const connectorPubKey = this.config.signer.generatePublicKey(
    //     sha256(leaves[i].leaf.id)
    //   );
    //   const connectorP2trAddr = getP2TRAddressFromPublicKey(
    //     connectorPubKey,
    //     this.config.getNetwork()
    //   );
    //   connectorP2trAddrs.push(connectorP2trAddr);
    // }

    // const feeBumpAddr = connectorP2trAddrs[connectorP2trAddrs.length - 1];
    // connectorP2trAddrs = connectorP2trAddrs.slice(0, -1);
    // const transaction = new Transaction();
    // transaction.addInput(intermediateInput);

    // for (const addr of [...connectorP2trAddrs, feeBumpAddr]) {
    //   transaction.addOutput({
    //     script: OutScript.encode(
    //       Address(getNetwork(this.config.getNetwork())).decode(addr)
    //     ),
    //     amount: BigInt(
    //       intermediateAmountSats / (connectorP2trAddrs.length + 1)
    //     ),
    //   });
    // }

    // const connectorOutputs = [];
    // for (let i = 0; i < transaction.outputsLength - 1; i++) {
    //   connectorOutputs.push({
    //     txid: hexToBytes(getTxId(transaction)),
    //     index: i,
    //   });
    // }

    // await this.coopExitService.getConnectorRefundSignatures({
    //   leaves,
    //   exitTxId: hexToBytes(getTxId(exitTx)),
    //   connectorOutputs,
    //   receiverPubKey: withdrawPubKey,
    // });

    throw new Error("Not implemented");
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
    const leaves = await sparkClient.get_tree_nodes_by_public_key({
      ownerIdentityPubkey: this.config.signer.getIdentityPublicKey(),
    });
    sparkClient.close?.();
    return leaves.nodes.filter((node) => node.status === "AVAILABLE");
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

  async broadcastTokenTransaction(
    tokenTransaction: TokenTransaction,
    leafToSpendPrivateKeys?: Uint8Array[],
    leafToSpendRevocationPublicKeys?: Uint8Array[]
  ): Promise<TokenTransaction> {
    return await this.tokenTransactionService!.broadcastTokenTransaction(
      tokenTransaction,
      leafToSpendPrivateKeys,
      leafToSpendRevocationPublicKeys
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
