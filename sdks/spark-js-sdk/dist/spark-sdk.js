import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { sha256 } from "@scure/btc-signer/utils";
import { decode } from "light-bolt11-decoder";
import SspClient from "./graphql/client.js";
import { BitcoinNetwork, } from "./graphql/objects/index.js";
import { TransferStatus, } from "./proto/spark.js";
import { WalletConfigService } from "./services/config.js";
import { ConnectionManager } from "./services/connection.js";
import { CoopExitService } from "./services/coop-exit.js";
import { DepositService } from "./services/deposit.js";
import { LightningService } from "./services/lightning.js";
import { TokenTransactionService } from "./services/token-transactions.js";
import { TransferService } from "./services/transfer.js";
import { TreeCreationService, } from "./services/tree-creation.js";
import { applyAdaptorToSignature, generateAdaptorFromSignature, generateSignatureFromExistingAdaptor, } from "./utils/adaptor-signature.js";
import { computeTaprootKeyNoScript, getSigHashFromTx, getTxFromRawTxBytes, getTxFromRawTxHex, getTxId, } from "./utils/bitcoin.js";
import { calculateAvailableTokenAmount, checkIfSelectedLeavesAreAvailable, } from "./utils/token-transactions.js";
import { initWasm } from "./utils/wasm-wrapper.js";
import { aggregateFrost, signFrost, } from "./utils/wasm.js";
export class SparkWallet {
    config;
    connectionManager;
    depositService;
    transferService;
    treeCreationService;
    lightningService;
    coopExitService;
    tokenTransactionService;
    sspClient = null;
    wasmModule = null;
    leaves = [];
    tokenLeaves = new Map();
    constructor(network, signer) {
        this.config = new WalletConfigService(network, signer);
        this.connectionManager = new ConnectionManager(this.config);
        this.depositService = new DepositService(this.config, this.connectionManager);
        this.transferService = new TransferService(this.config, this.connectionManager);
        this.treeCreationService = new TreeCreationService(this.config, this.connectionManager);
        this.tokenTransactionService = new TokenTransactionService(this.config, this.connectionManager);
        this.lightningService = new LightningService(this.config, this.connectionManager);
        this.coopExitService = new CoopExitService(this.config, this.connectionManager);
    }
    getSigner() {
        return this.config.signer;
    }
    async initWasm() {
        try {
            this.wasmModule = await initWasm();
        }
        catch (e) {
            console.error("Failed to initialize Wasm module", e);
        }
    }
    async ensureInitialized() {
        if (!this.wasmModule) {
            await this.initWasm();
        }
    }
    async getMasterPubKey() {
        return await this.config.signer.getIdentityPublicKey();
    }
    async signFrost(params) {
        await this.ensureInitialized();
        return signFrost(params);
    }
    async aggregateFrost(params) {
        await this.ensureInitialized();
        return aggregateFrost(params);
    }
    async generateMnemonic() {
        return await this.config.signer.generateMnemonic();
    }
    isInitialized() {
        return this.sspClient !== null && this.wasmModule !== null;
    }
    // TODO: Update to use config based on options
    async createSparkWallet(mnemonic) {
        const identityPublicKey = await this.config.signer.createSparkWalletFromMnemonic(mnemonic);
        await this.initializeWallet(identityPublicKey);
        return identityPublicKey;
    }
    async createSparkWalletFromSeed(seed) {
        const identityPublicKey = await this.config.signer.createSparkWalletFromSeed(seed);
        await this.initializeWallet(identityPublicKey);
        return identityPublicKey;
    }
    async initializeWallet(identityPublicKey) {
        this.sspClient = new SspClient(identityPublicKey);
        await this.initWasm();
        // TODO: Better leaf management?
        this.leaves = await this.getLeaves();
        this.config.signer.restoreSigningKeysFromLeafs(this.leaves);
        // await this.syncTokenLeaves();
    }
    async selectLeaves(targetAmount) {
        if (targetAmount <= 0) {
            throw new Error("Target amount must be positive");
        }
        const leaves = await this.getLeaves();
        if (leaves.length === 0) {
            return [];
        }
        leaves.sort((a, b) => b.value - a.value);
        let amount = 0;
        let nodes = [];
        for (const leaf of leaves) {
            if (targetAmount - amount >= leaf.value) {
                amount += leaf.value;
                nodes.push(leaf);
            }
        }
        if (amount < targetAmount) {
            throw new Error("Not enough leaves to cover target amount");
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
    async selectLeavesForSwap(targetAmount) {
        if (targetAmount == 0) {
            throw new Error("Target amount needs to > 0");
        }
        const leaves = await this.getLeaves();
        leaves.sort((a, b) => a.value - b.value);
        let amount = 0;
        const nodes = [];
        for (const leaf of leaves) {
            if (amount < targetAmount) {
                amount += leaf.value;
                nodes.push(leaf);
            }
        }
        if (amount < targetAmount) {
            throw new Error("You don't have enough nodes to swap for the target amount");
        }
        return nodes;
    }
    async syncWallet() {
        await this.claimTransfers();
        // TODO: This is broken. Uncomment when fixed
        // await this.syncTokenLeaves();
        this.leaves = await this.getLeaves();
        await this.optimizeLeaves();
    }
    async optimizeLeaves() {
        if (this.leaves.length > 0) {
            await this.requestLeavesSwap({ leaves: this.leaves });
        }
    }
    async requestLeavesSwap({ targetAmount, leaves, }) {
        if (targetAmount && targetAmount <= 0) {
            throw new Error("targetAmount must be positive");
        }
        await this.claimTransfers();
        let leavesToSwap;
        if (targetAmount && leaves && leaves.length > 0) {
            if (targetAmount < leaves.reduce((acc, leaf) => acc + leaf.value, 0)) {
                throw new Error("targetAmount is less than the sum of leaves");
            }
            leavesToSwap = leaves;
        }
        else if (targetAmount) {
            leavesToSwap = await this.selectLeavesForSwap(targetAmount);
        }
        else if (leaves && leaves.length > 0) {
            leavesToSwap = leaves;
        }
        else {
            throw new Error("targetAmount or leaves must be provided");
        }
        const leafKeyTweaks = await Promise.all(leavesToSwap.map(async (leaf) => ({
            leaf,
            signingPubKey: await this.config.signer.generatePublicKey(sha256(leaf.id)),
            newSigningPubKey: await this.config.signer.generatePublicKey(),
        })));
        const { transfer, signatureMap } = await this.transferService.sendTransferSignRefund(leafKeyTweaks, await this.config.signer.getSspIdentityPublicKey(), new Date(Date.now() + 10 * 60 * 1000));
        if (!transfer.leaves[0].leaf) {
            throw new Error("Failed to get leaf");
        }
        const refundSignature = signatureMap.get(transfer.leaves[0].leaf.id);
        if (!refundSignature) {
            throw new Error("Failed to get refund signature");
        }
        const { adaptorPrivateKey, adaptorSignature } = generateAdaptorFromSignature(refundSignature);
        if (!transfer.leaves[0].leaf) {
            throw new Error("Failed to get leaf");
        }
        const userLeaves = [];
        userLeaves.push({
            leaf_id: transfer.leaves[0].leaf.id,
            raw_unsigned_refund_transaction: bytesToHex(transfer.leaves[0].intermediateRefundTx),
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
            const signature = generateSignatureFromExistingAdaptor(refundSignature, adaptorPrivateKey);
            userLeaves.push({
                leaf_id: leaf.leaf.id,
                raw_unsigned_refund_transaction: bytesToHex(leaf.intermediateRefundTx),
                adaptor_added_signature: bytesToHex(signature),
            });
        }
        const adaptorPubkey = bytesToHex(secp256k1.getPublicKey(adaptorPrivateKey));
        let request = null;
        try {
            request = await this.sspClient?.requestLeaveSwap({
                userLeaves,
                adaptorPubkey,
                targetAmountSats: targetAmount ||
                    leavesToSwap.reduce((acc, leaf) => acc + leaf.value, 0),
                totalAmountSats: leavesToSwap.reduce((acc, leaf) => acc + leaf.value, 0),
                // TODO: Request fee from SSP
                feeSats: 0,
                // TODO: Map config network to proto network
                network: BitcoinNetwork.REGTEST,
            });
        }
        catch (e) {
            console.error("Failed to request leaves swap", e);
            throw e;
        }
        if (!request) {
            throw new Error("Failed to request leaves swap. No response returned.");
        }
        const sparkClient = await this.connectionManager.createSparkClient(this.config.getCoordinatorAddress());
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
            applyAdaptorToSignature(taprootKey.slice(1), sighash, adaptorSignatureBytes, adaptorPrivateKey);
        }
        sparkClient.close?.();
        await this.transferService.sendTransferTweakKey(transfer, leafKeyTweaks, signatureMap);
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
    async createLightningInvoice({ amountSats, memo, expirySeconds, 
    // TODO: This should default to lightspark ssp
    invoiceCreator = () => Promise.resolve(""), }) {
        if (!this.sspClient) {
            throw new Error("SSP client not initialized");
        }
        const requestLightningInvoice = async (amountSats, paymentHash, memo) => {
            const invoice = await this.sspClient.requestLightningReceive({
                amountSats,
                // TODO: Map config network to ssp network
                network: BitcoinNetwork.REGTEST,
                paymentHash: bytesToHex(paymentHash),
                expirySecs: expirySeconds,
                memo,
            });
            return invoice?.invoice.encodedEnvoice;
        };
        return this.lightningService.createLightningInvoice({
            amountSats,
            memo,
            invoiceCreator: requestLightningInvoice,
        });
    }
    async payLightningInvoice({ invoice, amountSats, }) {
        if (!this.sspClient) {
            throw new Error("SSP client not initialized");
        }
        // TODO: Get fee
        const decodedInvoice = decode(invoice);
        amountSats =
            Number(decodedInvoice.sections.find((section) => section.name === "amount")
                ?.value) / 1000;
        if (isNaN(amountSats) || amountSats <= 0) {
            throw new Error("Invalid amount");
        }
        const paymentHash = decodedInvoice.sections.find((section) => section.name === "payment_hash")?.value;
        if (!paymentHash) {
            throw new Error("No payment hash found in invoice");
        }
        // fetch leaves for amount
        const leaves = await this.selectLeaves(amountSats);
        const leavesToSend = await Promise.all(leaves.map(async (leaf) => ({
            leaf,
            signingPubKey: await this.config.signer.generatePublicKey(sha256(leaf.id)),
            newSigningPubKey: await this.config.signer.generatePublicKey(),
        })));
        const swapResponse = await this.lightningService.swapNodesForPreimage({
            leaves: leavesToSend,
            receiverIdentityPubkey: await this.config.signer.getSspIdentityPublicKey(),
            paymentHash: hexToBytes(paymentHash),
            isInboundPayment: false,
            invoiceString: invoice,
        });
        if (!swapResponse.transfer) {
            throw new Error("Failed to swap nodes for preimage");
        }
        const transfer = await this.transferService.sendTransferTweakKey(swapResponse.transfer, leavesToSend, new Map());
        const sspResponse = await this.sspClient.requestLightningSend({
            encodedInvoice: invoice,
            idempotencyKey: paymentHash,
        });
        if (!sspResponse) {
            throw new Error("Failed to contact SSP");
        }
        return sspResponse;
    }
    async getLightningReceiveFeeEstimate({ amountSats, network, }) {
        if (!this.sspClient) {
            throw new Error("SSP client not initialized");
        }
        return await this.sspClient.getLightningReceiveFeeEstimate(amountSats, network);
    }
    async getLightningSendFeeEstimate({ encodedInvoice, }) {
        if (!this.sspClient) {
            throw new Error("SSP client not initialized");
        }
        return await this.sspClient.getLightningSendFeeEstimate(encodedInvoice);
    }
    async getCoopExitFeeEstimate({ leafExternalIds, withdrawalAddress, }) {
        if (!this.sspClient) {
            throw new Error("SSP client not initialized");
        }
        return await this.sspClient.getCoopExitFeeEstimate({
            leafExternalIds,
            withdrawalAddress,
        });
    }
    async transferDepositToSelf(leaves, signingPubKey) {
        const leafKeyTweaks = await Promise.all(leaves.map(async (leaf) => ({
            leaf,
            signingPubKey,
            newSigningPubKey: await this.config.signer.generatePublicKey(),
        })));
        await this.transferService.sendTransfer(leafKeyTweaks, await this.config.signer.getIdentityPublicKey(), new Date(Date.now() + 10 * 60 * 1000));
        const pendingTransfers = await this.queryPendingTransfers();
        if (pendingTransfers.transfers.length > 0) {
            return (await this.claimTransfer(pendingTransfers.transfers[0])).nodes;
        }
        return;
    }
    async sendTransfer({ amount, receiverPubKey, leaves, expiryTime = new Date(Date.now() + 10 * 60 * 1000), }) {
        let leavesToSend = [];
        if (leaves) {
            leavesToSend = leaves.map((leaf) => ({
                ...leaf,
            }));
        }
        else if (amount) {
            leavesToSend = await this.selectLeaves(amount);
        }
        else {
            throw new Error("Must provide amount or leaves");
        }
        const leafKeyTweaks = await Promise.all(leavesToSend.map(async (leaf) => ({
            leaf,
            signingPubKey: await this.config.signer.generatePublicKey(sha256(leaf.id)),
            newSigningPubKey: await this.config.signer.generatePublicKey(),
        })));
        return await this.transferService.sendTransfer(leafKeyTweaks, receiverPubKey, expiryTime);
    }
    async queryPendingTransfers() {
        return await this.transferService.queryPendingTransfers();
    }
    async claimTransfer(transfer) {
        const leafPubKeyMap = await this.transferService.verifyPendingTransfer(transfer);
        let leavesToClaim = [];
        for (const leaf of transfer.leaves) {
            if (leaf.leaf) {
                const leafPubKey = leafPubKeyMap.get(leaf.leaf.id);
                if (leafPubKey) {
                    leavesToClaim.push({
                        leaf: leaf.leaf,
                        signingPubKey: leafPubKey,
                        newSigningPubKey: await this.config.signer.generatePublicKey(sha256(leaf.leaf.id)),
                    });
                }
            }
        }
        return await this.transferService.claimTransfer(transfer, leavesToClaim);
    }
    async claimTransfers() {
        const transfers = await this.queryPendingTransfers();
        let claimed = false;
        for (const transfer of transfers.transfers) {
            if (transfer.status !== TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED &&
                transfer.status !==
                    TransferStatus.TRANSFER_STATUS_RECEIVER_KEY_TWEAKED &&
                transfer.status !==
                    TransferStatus.TRANSFER_STATUSR_RECEIVER_REFUND_SIGNED) {
                continue;
            }
            await this.claimTransfer(transfer);
            claimed = true;
        }
        return claimed;
    }
    async coopExit(onchainAddress, targetAmountSats) {
        let leavesToSend = [];
        if (targetAmountSats) {
            leavesToSend = await this.selectLeaves(targetAmountSats);
        }
        else {
            leavesToSend = this.leaves.map((leaf) => ({
                ...leaf,
            }));
        }
        const leafKeyTweaks = await Promise.all(leavesToSend.map(async (leaf) => ({
            leaf,
            signingPubKey: await this.config.signer.generatePublicKey(sha256(leaf.id)),
            newSigningPubKey: await this.config.signer.generatePublicKey(),
        })));
        const coopExitRequest = await this.sspClient?.requestCoopExit({
            leafExternalIds: leavesToSend.map((leaf) => leaf.id),
            withdrawalAddress: onchainAddress,
        });
        if (!coopExitRequest?.rawConnectorTransaction) {
            throw new Error("Failed to request coop exit");
        }
        const connectorTx = getTxFromRawTxHex(coopExitRequest.rawConnectorTransaction);
        const coopExitTxId = getTxId(connectorTx);
        const connectorOutputs = [];
        for (let i = 0; i < connectorTx.outputsLength - 1; i++) {
            connectorOutputs.push({
                txid: hexToBytes(coopExitTxId),
                index: i,
            });
        }
        const sspPubIdentityKey = await this.config.signer.getSspIdentityPublicKey();
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
    async getLeaves() {
        const sparkClient = await this.connectionManager.createSparkClient(this.config.getCoordinatorAddress());
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
    async getBalance() {
        const leaves = await this.getLeaves();
        return leaves.reduce((acc, leaf) => acc + BigInt(leaf.value), 0n);
    }
    async verifyPendingTransfer(transfer) {
        return await this.transferService.verifyPendingTransfer(transfer);
    }
    // **** Deposit Flow ****
    async generateDepositAddress(signingPubkey) {
        return await this.depositService.generateDepositAddress({ signingPubkey });
    }
    async syncTokenLeaves() {
        await this.tokenTransactionService.syncTokenLeaves(this.tokenLeaves);
    }
    getTokenBalance(tokenPublicKey) {
        return calculateAvailableTokenAmount(this.tokenLeaves.get(bytesToHex(tokenPublicKey)));
    }
    async transferTokens(tokenPublicKey, tokenAmount, recipientPublicKey, selectedLeaves) {
        if (!this.tokenLeaves.has(bytesToHex(tokenPublicKey))) {
            throw new Error("No token leaves with the given tokenPublicKey");
        }
        if (selectedLeaves) {
            if (!checkIfSelectedLeavesAreAvailable(selectedLeaves, this.tokenLeaves, tokenPublicKey)) {
                throw new Error("One or more selected leaves are not available");
            }
        }
        else {
            await this.syncTokenLeaves();
            selectedLeaves = this.selectTokenLeaves(tokenPublicKey, tokenAmount);
        }
        const tokenTransaction = await this.tokenTransactionService.constructTransferTokenTransaction(selectedLeaves, recipientPublicKey, tokenPublicKey, tokenAmount);
        const finalizedTokenTransaction = await this.tokenTransactionService.broadcastTokenTransaction(tokenTransaction, selectedLeaves.map((leaf) => leaf.leaf.ownerPublicKey), selectedLeaves.map((leaf) => leaf.leaf.revocationPublicKey));
        const tokenPubKeyHex = bytesToHex(tokenPublicKey);
        if (!this.tokenLeaves.has(tokenPubKeyHex)) {
            this.tokenLeaves.set(tokenPubKeyHex, []);
        }
        this.tokenTransactionService.updateTokenLeavesFromFinalizedTransaction(this.tokenLeaves.get(tokenPubKeyHex), finalizedTokenTransaction);
    }
    selectTokenLeaves(tokenPublicKey, tokenAmount) {
        return this.tokenTransactionService.selectTokenLeaves(this.tokenLeaves.get(bytesToHex(tokenPublicKey)), tokenPublicKey, tokenAmount);
    }
    // If no leaves are passed in, it will take all the leaves available for the given tokenPublicKey
    async consolidateTokenLeaves(tokenPublicKey, selectedLeaves, transferBackToIdentityPublicKey = false) {
        if (!this.tokenLeaves.has(bytesToHex(tokenPublicKey))) {
            throw new Error("No token leaves with the given tokenPublicKey");
        }
        if (selectedLeaves) {
            if (!checkIfSelectedLeavesAreAvailable(selectedLeaves, this.tokenLeaves, tokenPublicKey)) {
                throw new Error("One or more selected leaves are not available");
            }
        }
        else {
            // Get all available leaves
            selectedLeaves = this.tokenLeaves.get(bytesToHex(tokenPublicKey));
        }
        if (selectedLeaves.length === 1) {
            return;
        }
        const partialTokenTransaction = await this.tokenTransactionService.constructConsolidateTokenTransaction(selectedLeaves, tokenPublicKey, transferBackToIdentityPublicKey);
        const finalizedTokenTransaction = await this.tokenTransactionService.broadcastTokenTransaction(partialTokenTransaction, selectedLeaves.map((leaf) => leaf.leaf.ownerPublicKey), selectedLeaves.map((leaf) => leaf.leaf.revocationPublicKey));
        const tokenPubKeyHex = bytesToHex(tokenPublicKey);
        if (!this.tokenLeaves.has(tokenPubKeyHex)) {
            this.tokenLeaves.set(tokenPubKeyHex, []);
        }
        this.tokenTransactionService.updateTokenLeavesFromFinalizedTransaction(this.tokenLeaves.get(tokenPubKeyHex), finalizedTokenTransaction);
    }
    async queryPendingDepositTx(depositAddress) {
        try {
            const baseUrl = "https://regtest-mempool.dev.dev.sparkinfra.net/api";
            const auth = btoa("lightspark:TFNR6ZeLdxF9HejW");
            const response = await fetch(`${baseUrl}/address/${depositAddress}/txs`, {
                headers: {
                    Authorization: `Basic ${auth}`,
                    "Content-Type": "application/json",
                },
            });
            const addressTxs = await response.json();
            if (addressTxs && addressTxs.length > 0) {
                const latestTx = addressTxs[0];
                // // Find our output
                const outputIndex = latestTx.vout.findIndex((output) => output.scriptpubkey_address === depositAddress);
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
                return { depositTx, vout: outputIndex };
            }
            return null;
        }
        catch (error) {
            throw error;
        }
    }
    async createTreeRoot(signingPubKey, verifyingKey, depositTx, vout) {
        const response = await this.depositService.createTreeRoot({
            signingPubKey,
            verifyingKey,
            depositTx,
            vout,
        });
        return await this.transferDepositToSelf(response.nodes, signingPubKey);
    }
    // **********************
    // **** Tree Creation Flow ****
    async generateDepositAddressForTree(vout, parentSigningPubKey, parentTx, parentNode) {
        return await this.treeCreationService.generateDepositAddressForTree(vout, parentSigningPubKey, parentTx, parentNode);
    }
    async createTree(vout, root, createLeaves, parentTx, parentNode) {
        return await this.treeCreationService.createTree(vout, root, createLeaves, parentTx, parentNode);
    }
}
//# sourceMappingURL=spark-sdk.js.map