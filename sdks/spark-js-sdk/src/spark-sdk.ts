import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { Transaction } from "@scure/btc-signer";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { sha256 } from "@scure/btc-signer/utils";
import { decode } from "light-bolt11-decoder";
import SspClient from "./graphql/client.js";
import {
  BitcoinNetwork,
  CoopExitFeeEstimateInput,
  CoopExitFeeEstimateOutput,
  LeavesSwapRequest,
  LightningReceiveFeeEstimateInput,
  LightningReceiveFeeEstimateOutput,
  LightningSendFeeEstimateInput,
  LightningSendFeeEstimateOutput,
  UserLeafInput,
} from "./graphql/objects/index.js";
import {
  GenerateDepositAddressResponse,
  LeafWithPreviousTransactionData,
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
import { SparkSigner } from "./signer/signer.js";
import {
  applyAdaptorToSignature,
  generateAdaptorFromSignature,
  generateSignatureFromExistingAdaptor,
} from "./utils/adaptor-signature.js";
import {
  computeTaprootKeyNoScript,
  getSigHashFromTx,
  getTxFromRawTxBytes,
  getTxFromRawTxHex,
  getTxId,
} from "./utils/bitcoin.js";
import { Network } from "./utils/network.js";
import {
  calculateAvailableTokenAmount,
  checkIfSelectedLeavesAreAvailable,
} from "./utils/token-transactions.js";
import { initWasm } from "./utils/wasm-wrapper.js";
import { InitOutput } from "./wasm/spark_bindings.js";

export type CreateLightningInvoiceParams = {
  amountSats: number;
  expirySeconds: number;
  memo: string;
  invoiceCreator?: () => Promise<string>;
};

export type PayLightningInvoiceParams = {
  invoice: string;
  amountSats?: number;
};

export type SendTransferParams = {
  amount?: number;
  leaves?: TreeNode[];
  receiverPubKey: Uint8Array;
  expiryTime?: Date;
};

export type DepositParams = {
  signingPubKey: Uint8Array;
  verifyingKey: Uint8Array;
  depositTx: Transaction;
  vout: number;
};

export class SparkWallet {
  protected config: WalletConfigService;

  protected connectionManager: ConnectionManager;

  private depositService: DepositService;
  protected transferService: TransferService;
  private treeCreationService: TreeCreationService;
  private lightningService: LightningService;
  private coopExitService: CoopExitService;
  private tokenTransactionService: TokenTransactionService;

  private sspClient: SspClient | null = null;
  private wasmModule: InitOutput | null = null;

  protected leaves: TreeNode[] = [];
  protected tokenLeaves: Map<string, LeafWithPreviousTransactionData[]> =
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
    this.lightningService = new LightningService(
      this.config,
      this.connectionManager
    );
    this.coopExitService = new CoopExitService(
      this.config,
      this.connectionManager
    );
  }

  private async initWasm() {
    try {
      this.wasmModule = await initWasm();
    } catch (e) {
      console.error("Failed to initialize Wasm module", e);
    }
  }

  private async initializeWallet(identityPublicKey: string) {
    this.sspClient = new SspClient(identityPublicKey);
    await this.initWasm();
    // TODO: Better leaf management?
    this.leaves = await this.getLeaves();
    this.config.signer.restoreSigningKeysFromLeafs(this.leaves);

    // await this.syncTokenLeaves();
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
      throw new Error(
        "You don't have enough nodes to swap for the target amount"
      );
    }

    return nodes;
  }

  private async getLeaves(): Promise<TreeNode[]> {
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
    return Object.entries(leaves.nodes)
      .filter(([_, node]) => node.status === "AVAILABLE")
      .map(([_, node]) => node);
  }

  private async optimizeLeaves() {
    if (this.leaves.length > 0) {
      await this.requestLeavesSwap({ leaves: this.leaves });
    }
  }

  private async syncWallet() {
    await this.claimTransfers();
    // TODO: This is broken. Uncomment when fixed
    // await this.syncTokenLeaves();
    this.leaves = await this.getLeaves();
    await this.optimizeLeaves();
  }

  public isInitialized(): boolean {
    return this.sspClient !== null && this.wasmModule !== null;
  }

  public async getIdentityPublicKey(): Promise<string> {
    return bytesToHex(await this.config.signer.getIdentityPublicKey());
  }

  public async initWalletFromMnemonic(mnemonic?: string) {
    if (!mnemonic) {
      mnemonic = await this.config.signer.generateMnemonic();
    }
    const identityPublicKey =
      await this.config.signer.createSparkWalletFromMnemonic(mnemonic);
    await this.initializeWallet(identityPublicKey);
    return mnemonic;
  }

  public async initWallet(seed: Uint8Array | string) {
    const identityPublicKey =
      await this.config.signer.createSparkWalletFromSeed(seed);
    await this.initializeWallet(identityPublicKey);
    return identityPublicKey;
  }

  public async requestLeavesSwap({
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
        transfer.leaves[0].intermediateRefundTx
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
        adaptorPrivateKey
      );

      userLeaves.push({
        leaf_id: leaf.leaf.id,
        raw_unsigned_refund_transaction: bytesToHex(leaf.intermediateRefundTx),
        adaptor_added_signature: bytesToHex(signature),
      });
    }

    const adaptorPubkey = bytesToHex(secp256k1.getPublicKey(adaptorPrivateKey));

    let request: LeavesSwapRequest | null | undefined = null;
    try {
      request = await this.sspClient?.requestLeaveSwap({
        userLeaves,
        adaptorPubkey,
        targetAmountSats:
          targetAmount ||
          leavesToSwap.reduce((acc, leaf) => acc + leaf.value, 0),
        totalAmountSats: leavesToSwap.reduce(
          (acc, leaf) => acc + leaf.value,
          0
        ),
        // TODO: Request fee from SSP
        feeSats: 0,
        // TODO: Map config network to proto network
        network: BitcoinNetwork.REGTEST,
      });
    } catch (e) {
      await this.transferService.cancelSendTransfer(transfer);

      console.log("Cancelled send transfer", transfer.id);
      throw new Error(`Failed to request leaves swap: ${e}`);
    }

    if (!request) {
      throw new Error("Failed to request leaves swap. No response returned.");
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

      if (!response.nodes[leaf.leafId]?.nodeTx) {
        throw new Error(`Node tx not found for leaf ${leaf.leafId}`);
      }

      if (!response.nodes[leaf.leafId]?.verifyingPublicKey) {
        throw new Error(`Node public key not found for leaf ${leaf.leafId}`);
      }

      // @ts-ignore - We do a null check above
      const nodeTx = getTxFromRawTxBytes(response.nodes[leaf.leafId].nodeTx);
      const refundTxBytes = hexToBytes(leaf.rawUnsignedRefundTransaction);
      const refundTx = getTxFromRawTxBytes(refundTxBytes);
      const sighash = getSigHashFromTx(refundTx, 0, nodeTx.getOutput(0));

      // @ts-ignore - We do a null check above
      const nodePublicKey = response.nodes[leaf.leafId]!.verifyingPublicKey;

      const taprootKey = computeTaprootKeyNoScript(nodePublicKey.slice(1));
      const adaptorSignatureBytes = hexToBytes(leaf.adaptorSignedSignature);
      applyAdaptorToSignature(
        taprootKey.slice(1),
        sighash,
        adaptorSignatureBytes,
        adaptorPrivateKey
      );
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

  public async getBalance(): Promise<BigInt> {
    await this.claimTransfers();
    // await this.syncTokenLeaves();
    const leaves = await this.getLeaves();
    return leaves.reduce((acc, leaf) => acc + BigInt(leaf.value), 0n);
  }

  public async generatePublicKey(): Promise<string> {
    return bytesToHex(await this.config.signer.generatePublicKey());
  }

  // ***** Deposit Flow *****
  public async generateDepositAddress(
    signingPubkey: Uint8Array
  ): Promise<GenerateDepositAddressResponse> {
    return await this.depositService!.generateDepositAddress({ signingPubkey });
  }

  public async finalizeDeposit({
    signingPubKey,
    verifyingKey,
    depositTx,
    vout,
  }: DepositParams) {
    const response = await this.depositService!.createTreeRoot({
      signingPubKey,
      verifyingKey,
      depositTx,
      vout,
    });

    return await this.transferDepositToSelf(response.nodes, signingPubKey);
  }

  public async claimDeposits() {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress()
    );

    const identityPublicKey = await this.config.signer.getIdentityPublicKey();
    const deposits = await sparkClient.query_unused_deposit_addresses({
      identityPublicKey,
    });

    const depositNodes: TreeNode[] = [];
    for (const deposit of deposits.depositAddresses) {
      const tx = await this.queryMempoolTxs(deposit.depositAddress);

      if (!tx) {
        continue;
      }

      const { depositTx, vout } = tx;

      const nodes = await this.finalizeDeposit({
        signingPubKey: deposit.userSigningPublicKey,
        verifyingKey: deposit.verifyingPublicKey,
        depositTx,
        vout,
      });

      if (nodes) {
        depositNodes.push(...nodes);
      }
    }

    return depositNodes;
  }

  private async queryMempoolTxs(address: string) {
    const baseUrl = "https://regtest-mempool.dev.dev.sparkinfra.net/api";
    const auth = btoa("lightspark:TFNR6ZeLdxF9HejW");

    const response = await fetch(`${baseUrl}/address/${address}/txs`, {
      headers: {
        Authorization: `Basic ${auth}`,
        "Content-Type": "application/json",
      },
    });

    const addressTxs = await response.json();

    if (addressTxs && addressTxs.length > 0) {
      console.log("Found transaction");
      const latestTx = addressTxs[0];

      const outputIndex: number = latestTx.vout.findIndex(
        (output: any) => output.scriptpubkey_address === address
      );

      if (outputIndex === -1) {
        return null;
      }

      const txResponse = await fetch(`${baseUrl}/tx/${latestTx.txid}/hex`, {
        headers: {
          Authorization: `Basic ${auth}`,
          "Content-Type": "application/json",
        },
      });
      const txHex = await txResponse.text();
      const depositTx = getTxFromRawTxHex(txHex);

      return {
        depositTx,
        vout: outputIndex,
      };
    }
    return null;
  }

  private async transferDepositToSelf(
    leaves: TreeNode[],
    signingPubKey: Uint8Array
  ): Promise<TreeNode[] | undefined> {
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

    const pendingTransfers = await this.transferService.queryPendingTransfers();
    if (pendingTransfers.transfers.length > 0) {
      // @ts-ignore - We check the length, so the first element is guaranteed to exist
      return (await this.claimTransfer(pendingTransfers.transfers[0])).nodes;
    }

    return;
  }
  // ***** Transfer Flow *****

  public async sendTransfer({
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

    const transfer = await this.transferService.sendTransfer(
      leafKeyTweaks,
      receiverPubKey,
      expiryTime
    );

    const leavesToRemove = new Set(leavesToSend.map((leaf) => leaf.id));
    this.leaves = this.leaves.filter((leaf) => !leavesToRemove.has(leaf.id));

    return transfer;
  }

  private async claimTransfer(transfer: Transfer) {
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

  private async claimTransfers(): Promise<boolean> {
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

  public async cancelAllSenderInitiatedTransfers() {
    const transfers =
      await this.transferService.queryPendingTransfersBySender();
    for (const transfer of transfers.transfers) {
      if (
        transfer.status === TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED
      ) {
        await this.transferService.cancelSendTransfer(transfer);
      }
    }
  }

  // ***** Lightning Flow *****
  public async createLightningInvoice({
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

  public async payLightningInvoice({
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

    const leavesToRemove = new Set(leavesToSend.map((leaf) => leaf.leaf.id));
    this.leaves = this.leaves.filter((leaf) => !leavesToRemove.has(leaf.id));

    return sspResponse;
  }

  private async getLightningReceiveFeeEstimate({
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

  private async getLightningSendFeeEstimate({
    encodedInvoice,
  }: LightningSendFeeEstimateInput): Promise<LightningSendFeeEstimateOutput | null> {
    if (!this.sspClient) {
      throw new Error("SSP client not initialized");
    }

    return await this.sspClient.getLightningSendFeeEstimate(encodedInvoice);
  }

  // ***** Tree Creation Flow *****
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
  // ***** Cooperative Exit Flow *****

  public async coopExit(onchainAddress: string, targetAmountSats?: number) {
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

  private async getCoopExitFeeEstimate({
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

  // ***** Token Flow *****

  public async syncTokenLeaves() {
    const trackedPublicKeys = await this.config.signer.getTrackedPublicKeys();

    const unsortedTokenLeaves =
      await this.tokenTransactionService.fetchOwnedTokenLeaves(
        [...trackedPublicKeys, await this.config.signer.getIdentityPublicKey()],
        []
      );

    unsortedTokenLeaves.forEach((leaf) => {
      const tokenKey = bytesToHex(leaf.leaf!.tokenPublicKey!);
      const index = leaf.previousTransactionVout!;

      this.tokenLeaves.set(tokenKey, [
        { ...leaf, previousTransactionVout: index },
      ]);
    });
  }

  async getAllTokenBalances(): Promise<Map<string, bigint>> {
    await this.syncTokenLeaves();

    const balances = new Map<string, bigint>();
    for (const [tokenPublicKey, leaves] of this.tokenLeaves.entries()) {
      balances.set(tokenPublicKey, calculateAvailableTokenAmount(leaves));
    }
    return balances;
  }

  async getTokenBalance(tokenPublicKey: string) {
    await this.syncTokenLeaves();

    if (!this.tokenLeaves.has(tokenPublicKey)) {
      throw new Error("No token leaves with the given tokenPublicKey");
    }
    return calculateAvailableTokenAmount(this.tokenLeaves.get(tokenPublicKey)!);
  }

  async transferTokens(
    tokenPublicKey: string,
    tokenAmount: bigint,
    recipientPublicKey: string,
    selectedLeaves?: LeafWithPreviousTransactionData[]
  ) {
    await this.syncTokenLeaves();
    if (!this.tokenLeaves.has(tokenPublicKey)) {
      throw new Error("No token leaves with the given tokenPublicKey");
    }

    const tokenPublicKeyBytes = hexToBytes(tokenPublicKey);
    const recipientPublicKeyBytes = hexToBytes(recipientPublicKey);

    if (selectedLeaves) {
      if (
        !checkIfSelectedLeavesAreAvailable(
          selectedLeaves,
          this.tokenLeaves,
          tokenPublicKeyBytes
        )
      ) {
        throw new Error("One or more selected leaves are not available");
      }
    } else {
      selectedLeaves = this.selectTokenLeaves(tokenPublicKey, tokenAmount);
    }

    const tokenTransaction =
      await this.tokenTransactionService.constructTransferTokenTransaction(
        selectedLeaves,
        recipientPublicKeyBytes,
        tokenPublicKeyBytes,
        tokenAmount
      );

    const finalizedTokenTransaction =
      await this.tokenTransactionService.broadcastTokenTransaction(
        tokenTransaction,
        selectedLeaves.map((leaf) => leaf.leaf!.ownerPublicKey),
        selectedLeaves.map((leaf) => leaf.leaf!.revocationPublicKey!)
      );

    if (!this.tokenLeaves.has(tokenPublicKey)) {
      this.tokenLeaves.set(tokenPublicKey, []);
    }
  }

  selectTokenLeaves(
    tokenPublicKey: string,
    tokenAmount: bigint
  ): LeafWithPreviousTransactionData[] {
    return this.tokenTransactionService.selectTokenLeaves(
      this.tokenLeaves.get(tokenPublicKey)!,
      tokenAmount
    );
  }

  // If no leaves are passed in, it will take all the leaves available for the given tokenPublicKey
  async consolidateTokenLeaves(
    tokenPublicKey: string,
    selectedLeaves?: LeafWithPreviousTransactionData[],
    transferBackToIdentityPublicKey: boolean = false
  ) {
    await this.syncTokenLeaves();
    const tokenPublicKeyBytes = hexToBytes(tokenPublicKey);

    if (!this.tokenLeaves.has(tokenPublicKey)) {
      throw new Error("No token leaves with the given tokenPublicKey");
    }

    if (selectedLeaves) {
      if (
        !checkIfSelectedLeavesAreAvailable(
          selectedLeaves,
          this.tokenLeaves,
          tokenPublicKeyBytes
        )
      ) {
        throw new Error("One or more selected leaves are not available");
      }
    } else {
      // Get all available leaves
      selectedLeaves = this.tokenLeaves.get(tokenPublicKey)!;
    }

    if (selectedLeaves!.length === 1) {
      return;
    }

    const partialTokenTransaction =
      await this.tokenTransactionService.constructConsolidateTokenTransaction(
        selectedLeaves,
        tokenPublicKeyBytes,
        transferBackToIdentityPublicKey
      );

    const finalizedTokenTransaction =
      await this.tokenTransactionService.broadcastTokenTransaction(
        partialTokenTransaction,
        selectedLeaves.map((leaf) => leaf.leaf!.ownerPublicKey),
        selectedLeaves.map((leaf) => leaf.leaf!.revocationPublicKey!)
      );

    if (!this.tokenLeaves.has(tokenPublicKey)) {
      this.tokenLeaves.set(tokenPublicKey, []);
    }
  }
}
