import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { Address, Transaction } from "@scure/btc-signer";
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
  LeafWithPreviousTransactionData,
  QueryAllTransfersResponse,
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

import { validateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { Mutex } from "async-mutex";
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
import bitcoin from "bitcoinjs-lib";

// Add this constant at the file level
const MAX_TOKEN_LEAVES = 100;

export type CreateLightningInvoiceParams = {
  amountSats: number;
  memo: string;
};

export type PayLightningInvoiceParams = {
  invoice: string;
};

export type SendTransferParams = {
  amount?: number;
  leaves?: TreeNode[];
  receiverPubKey: string;
  expiryTime?: Date;
};

type DepositParams = {
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

  private sendTransferMutex = new Mutex();
  private claimTransferMutex = new Mutex();

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

  private async initWasm() {
    try {
      this.wasmModule = await initWasm();
    } catch (e) {
      console.error("Failed to initialize Wasm module", e);
    }
  }

  private async initializeWallet(identityPublicKey: string) {
    this.sspClient = new SspClient(identityPublicKey);
    await Promise.all([
      this.initWasm(),
      this.config.signer.restoreSigningKeysFromLeafs(this.leaves),
      // Hacky but do this to store the deposit signing key in the signer
      this.config.signer.getDepositSigningKey(),
    ]);

    await this.syncWallet();
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
        "You don't have enough nodes to swap for the target amount",
      );
    }

    return nodes;
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

  private async optimizeLeaves() {
    if (this.leaves.length > 0) {
      await this.requestLeavesSwap({ leaves: this.leaves });
    }
    await new Promise((resolve) => setTimeout(resolve, 5000));
    this.leaves = [];
    this.leaves = await this.getLeaves();
  }

  private async syncWallet() {
    await this.claimTransfers();
    await this.claimDeposits();
    await this.syncTokenLeaves();
    this.leaves = await this.getLeaves();
    await this.optimizeLeaves();
  }

  private isInitialized(): boolean {
    return this.sspClient !== null && this.wasmModule !== null;
  }

  public async getIdentityPublicKey(): Promise<string> {
    return bytesToHex(await this.config.signer.getIdentityPublicKey());
  }

  public async getSparkAddress(): Promise<string> {
    return bytesToHex(await this.config.signer.getIdentityPublicKey());
  }

  public async initWallet(mnemonicOrSeed?: Uint8Array | string) {
    const returnMnemonic = !mnemonicOrSeed;
    if (!mnemonicOrSeed) {
      mnemonicOrSeed = await this.config.signer.generateMnemonic();
    }

    if (typeof mnemonicOrSeed !== "string") {
      mnemonicOrSeed = bytesToHex(mnemonicOrSeed);
    }

    let mnemonic: string | undefined;
    if (validateMnemonic(mnemonicOrSeed, wordlist)) {
      mnemonic = mnemonicOrSeed;
      await this.initWalletFromMnemonic(mnemonicOrSeed);
    } else {
      await this.initWalletFromSeed(mnemonicOrSeed);
    }

    const balance = this.leaves.reduce(
      (acc, leaf) => acc + BigInt(leaf.value),
      0n,
    );
    const tokenBalance = await this.getAllTokenBalances();

    if (returnMnemonic) {
      return {
        mnemonic,
        balance,
        tokenBalance,
      };
    }

    return {
      balance,
      tokenBalance,
    };
  }

  private async initWalletFromMnemonic(mnemonic: string) {
    const identityPublicKey =
      await this.config.signer.createSparkWalletFromMnemonic(mnemonic);
    await this.initializeWallet(identityPublicKey);
    return identityPublicKey;
  }

  private async initWalletFromSeed(seed: Uint8Array | string) {
    const identityPublicKey =
      await this.config.signer.createSparkWalletFromSeed(seed);
    await this.initializeWallet(identityPublicKey);
    return identityPublicKey;
  }

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
      await this.transferService.sendTransferSignRefund(
        leafKeyTweaks,
        await this.config.signer.getSspIdentityPublicKey(
          this.config.getNetwork(),
        ),
        new Date(Date.now() + 10 * 60 * 1000),
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

  public async getAllTransfers(
    limit: number = 20,
    offset: number = 0,
  ): Promise<QueryAllTransfersResponse> {
    return await this.transferService.queryAllTransfers(limit, offset);
  }

  public async getBalance(forceRefetch = false): Promise<{
    balance: bigint;
    tokenBalances: Map<string, { balance: bigint; leafCount: number }>;
  }> {
    if (forceRefetch) {
      await Promise.all([
        this.claimTransfers(),
        this.claimDeposits(),
        this.syncTokenLeaves(),
      ]);
      await this.syncTokenLeaves();
      this.leaves = await this.getLeaves();
    }

    const tokenBalances = new Map<
      string,
      { balance: bigint; leafCount: number }
    >();

    for (const [tokenPublicKey, leaves] of this.tokenLeaves.entries()) {
      tokenBalances.set(tokenPublicKey, {
        balance: calculateAvailableTokenAmount(leaves),
        leafCount: leaves.length,
      });
    }

    const balance = this.leaves.reduce(
      (acc, leaf) => acc + BigInt(leaf.value),
      0n,
    );

    return {
      balance,
      tokenBalances,
    };
  }

  // ***** Deposit Flow *****
  public async getDepositAddress(): Promise<string> {
    return await this.generateDepositAddress();
  }

  private async generateDepositAddress(): Promise<string> {
    const signingPubkey = await this.config.signer.getDepositSigningKey();
    const address = await this.depositService!.generateDepositAddress({
      signingPubkey,
    });
    if (!address.depositAddress) {
      throw new Error("Failed to generate deposit address");
    }
    return address.depositAddress.address;
  }

  private async finalizeDeposit({
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

  private async claimDeposits() {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
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
    const network = getNetworkFromAddress(address) || this.config.getNetwork();
    const baseUrl =
      network === BitcoinNetwork.REGTEST
        ? "https://regtest-mempool.dev.dev.sparkinfra.net/api"
        : "https://mempool.space/docs/api/rest";
    const auth = btoa("spark-sdk:mCMk1JqlBNtetUNy");

    const headers: Record<string, string> = {
      "Content-Type": "application/json",
    };

    if (network === BitcoinNetwork.REGTEST) {
      headers["Authorization"] = `Basic ${auth}`;
    }

    const response = await fetch(`${baseUrl}/address/${address}/txs`, { headers });

    const addressTxs = await response.json();

    if (addressTxs && addressTxs.length > 0) {
      const latestTx = addressTxs[0];

      const outputIndex: number = latestTx.vout.findIndex(
        (output: any) => output.scriptpubkey_address === address,
      );

      if (outputIndex === -1) {
        return null;
      }

      const txResponse = await fetch(`${baseUrl}/tx/${latestTx.txid}/hex`, { headers });
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
    signingPubKey: Uint8Array,
  ): Promise<TreeNode[] | undefined> {
    const leafKeyTweaks = await Promise.all(
      leaves.map(async (leaf) => ({
        leaf,
        signingPubKey,
        newSigningPubKey: await this.config.signer.generatePublicKey(),
      })),
    );

    await this.transferService.sendTransfer(
      leafKeyTweaks,
      await this.config.signer.getIdentityPublicKey(),
      new Date(Date.now() + 10 * 60 * 1000),
    );

    const pendingTransfers = await this.transferService.queryPendingTransfers();
    if (pendingTransfers.transfers.length > 0) {
      // @ts-ignore - We check the length, so the first element is guaranteed to exist
      return (await this.claimTransfer(pendingTransfers.transfers[0])).nodes;
    }

    return;
  }
  // ***** Transfer Flow *****

  public async sendSparkTransfer({
    receiverSparkAddress,
    amountSats,
  }: {
    receiverSparkAddress: string;
    amountSats: number;
  }) {
    return await this._sendTransfer({
      receiverPubKey: receiverSparkAddress,
      amount: amountSats,
    });
  }

  private async _sendTransfer({
    amount,
    receiverPubKey,
    leaves,
    expiryTime = new Date(Date.now() + 10 * 60 * 1000),
  }: SendTransferParams) {
    return await this.sendTransferMutex.runExclusive(async () => {
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
            sha256(leaf.id),
          ),
          newSigningPubKey: await this.config.signer.generatePublicKey(),
        })),
      );

      const transfer = await this.transferService.sendTransfer(
        leafKeyTweaks,
        hexToBytes(receiverPubKey),
        expiryTime,
      );

      const leavesToRemove = new Set(leavesToSend.map((leaf) => leaf.id));
      this.leaves = this.leaves.filter((leaf) => !leavesToRemove.has(leaf.id));

      return transfer;
    });
  }

  private async claimTransfer(transfer: Transfer) {
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

      return await this.transferService.claimTransfer(transfer, leavesToClaim);
    });
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

  private async cancelAllSenderInitiatedTransfers() {
    const transfers =
      await this.transferService.queryPendingTransfersBySender();
    for (const transfer of transfers.transfers) {
      if (transfer.status === TransferStatus.TRANSFER_STATUS_SENDER_INITIATED) {
        await this.transferService.cancelSendTransfer(transfer);
      }
    }
  }

  // ***** Lightning Flow *****
  public async createLightningInvoice({
    amountSats,
    memo,
  }: CreateLightningInvoiceParams) {
    const expirySeconds = 60 * 60 * 24 * 30;
    if (!this.sspClient) {
      throw new Error("SSP client not initialized");
    }

    const requestLightningInvoice = async (
      amountSats: number,
      paymentHash: Uint8Array,
      memo: string,
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

      return invoice?.invoice.encodedEnvoice;
    };

    return this.lightningService!.createLightningInvoice({
      amountSats,
      memo,
      invoiceCreator: requestLightningInvoice,
    });
  }

  public async payLightningInvoice({ invoice }: PayLightningInvoiceParams) {
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

    // fetch leaves for amount

    const leaves = await this.selectLeaves(amountSats);

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
      receiverIdentityPubkey: await this.config.signer.getSspIdentityPublicKey(
        this.config.getNetwork(),
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
      network,
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

  public async withdraw({
    onchainAddress,
    targetAmountSats,
  }: {
    onchainAddress: string;
    targetAmountSats?: number;
  }) {
    return this.coopExit(onchainAddress, targetAmountSats);
  }

  private async coopExit(onchainAddress: string, targetAmountSats?: number) {
    let leavesToSend: TreeNode[] = [];
    if (targetAmountSats) {
      leavesToSend = await this.selectLeaves(targetAmountSats);
    } else {
      leavesToSend = this.leaves.map((leaf) => ({
        ...leaf,
      }));
    }

    const pubkey = await this.config.signer.getIdentityPublicKey();

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
    });

    if (!coopExitRequest?.rawConnectorTransaction) {
      throw new Error("Failed to request coop exit");
    }

    const connectorTx = getTxFromRawTxHex(
      coopExitRequest.rawConnectorTransaction,
    );
    const coopExitTxId = getTxId(connectorTx);

    const connectorOutputs: TransactionInput[] = [];
    for (let i = 0; i < connectorTx.outputsLength - 1; i++) {
      connectorOutputs.push({
        txid: hexToBytes(coopExitTxId),
        index: i,
      });
    }

    const sspPubIdentityKey = await this.config.signer.getSspIdentityPublicKey(
      this.config.getNetwork(),
    );

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

  protected async syncTokenLeaves() {
    this.tokenLeaves.clear();

    const trackedPublicKeys = await this.config.signer.getTrackedPublicKeys();
    const unsortedTokenLeaves =
      await this.tokenTransactionService.fetchOwnedTokenLeaves(
        [...trackedPublicKeys, await this.config.signer.getIdentityPublicKey()],
        [],
      );

    // Group leaves by token key
    const groupedLeaves = new Map<string, LeafWithPreviousTransactionData[]>();

    unsortedTokenLeaves.forEach((leaf) => {
      const tokenKey = bytesToHex(leaf.leaf!.tokenPublicKey!);
      const index = leaf.previousTransactionVout!;

      if (!groupedLeaves.has(tokenKey)) {
        groupedLeaves.set(tokenKey, []);
      }

      groupedLeaves.get(tokenKey)!.push({
        ...leaf,
        previousTransactionVout: index,
      });
    });

    this.tokenLeaves = groupedLeaves;
  }

  private async getAllTokenBalances(): Promise<
    Map<
      string,
      {
        balance: bigint;
        leafCount: number;
      }
    >
  > {
    await this.syncTokenLeaves();

    const balances = new Map<string, { balance: bigint; leafCount: number }>();
    for (const [tokenPublicKey, leaves] of this.tokenLeaves.entries()) {
      balances.set(tokenPublicKey, {
        balance: calculateAvailableTokenAmount(leaves),
        leafCount: leaves.length,
      });
    }
    return balances;
  }

  public async transferTokens(
    tokenPublicKey: string,
    tokenAmount: bigint,
    recipientPublicKey: string,
    selectedLeaves?: LeafWithPreviousTransactionData[],
  ): Promise<string> {
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
          tokenPublicKeyBytes,
        )
      ) {
        throw new Error("One or more selected leaves are not available");
      }
    } else {
      selectedLeaves = this.selectTokenLeaves(tokenPublicKey, tokenAmount);
    }

    if (selectedLeaves!.length > MAX_TOKEN_LEAVES) {
      throw new Error("Too many leaves selected");
    }

    const tokenTransaction =
      await this.tokenTransactionService.constructTransferTokenTransaction(
        selectedLeaves,
        recipientPublicKeyBytes,
        tokenPublicKeyBytes,
        tokenAmount,
      );

    return await this.tokenTransactionService.broadcastTokenTransaction(
      tokenTransaction,
      selectedLeaves.map((leaf) => leaf.leaf!.ownerPublicKey),
      selectedLeaves.map((leaf) => leaf.leaf!.revocationPublicKey!),
    );
  }

  private selectTokenLeaves(
    tokenPublicKey: string,
    tokenAmount: bigint,
  ): LeafWithPreviousTransactionData[] {
    return this.tokenTransactionService.selectTokenLeaves(
      this.tokenLeaves.get(tokenPublicKey)!,
      tokenAmount,
    );
  }
}

function getNetworkFromAddress(address: string) {
  try {
    const decoded = bitcoin.address.fromBech32(address);
    // HRP (human-readable part) determines the network
    if (decoded.prefix === "bc") {
      return BitcoinNetwork.MAINNET;
    } else if (decoded.prefix === "bcrt") {
      return BitcoinNetwork.REGTEST;
    }
  } catch (err) {
    throw new Error("Invalid Bitcoin address");
  }
  return null;
}
