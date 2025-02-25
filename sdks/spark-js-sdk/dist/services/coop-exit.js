import { Transaction } from "@scure/btc-signer";
import { getP2TRScriptFromPublicKey, getTxFromRawTxBytes, } from "../utils/bitcoin.js";
import { getCrypto } from "../utils/crypto.js";
import { getNextTransactionSequence } from "../utils/transaction.js";
import { BaseTransferService, } from "./transfer.js";
const crypto = getCrypto();
const TIME_LOCK_INTERVAL = 100;
export class CoopExitService extends BaseTransferService {
    constructor(config, connectionManager) {
        super(config, connectionManager);
    }
    async getConnectorRefundSignatures({ leaves, exitTxId, connectorOutputs, receiverPubKey, }) {
        const { transfer, signaturesMap } = await this.signCoopExitRefunds(leaves, exitTxId, connectorOutputs, receiverPubKey);
        const transferTweak = await this.sendTransferTweakKey(transfer, leaves, signaturesMap);
        return { transfer: transferTweak, signaturesMap };
    }
    createConnectorRefundTransaction(sequence, nodeOutPoint, connectorOutput, amountSats, receiverPubKey) {
        const refundTx = new Transaction();
        if (!nodeOutPoint.txid || nodeOutPoint.index === undefined) {
            throw new Error("Node outpoint txid or index is undefined");
        }
        refundTx.addInput({
            txid: nodeOutPoint.txid,
            index: nodeOutPoint.index,
            sequence,
        });
        refundTx.addInput(connectorOutput);
        const receiverScript = getP2TRScriptFromPublicKey(receiverPubKey, this.config.getNetwork());
        refundTx.addOutput({
            script: receiverScript,
            amount: amountSats,
        });
        return refundTx;
    }
    async signCoopExitRefunds(leaves, exitTxId, connectorOutputs, receiverPubKey) {
        if (leaves.length !== connectorOutputs.length) {
            throw new Error("Number of leaves and connector outputs must match");
        }
        const signingJobs = [];
        const leafDataMap = new Map();
        for (let i = 0; i < leaves.length; i++) {
            const leaf = leaves[i];
            const connectorOutput = connectorOutputs[i];
            if (!leaf?.leaf) {
                throw new Error("Leaf not found");
            }
            if (!connectorOutput) {
                throw new Error("Connector output not found");
            }
            const currentRefundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);
            const sequence = getNextTransactionSequence(currentRefundTx.getInput(0).sequence);
            const refundTx = this.createConnectorRefundTransaction(sequence, currentRefundTx.getInput(0), connectorOutput, BigInt(leaf.leaf.value), receiverPubKey);
            const signingNonceCommitment = await this.config.signer.getRandomSigningCommitment();
            const signingJob = {
                leafId: leaf.leaf.id,
                refundTxSigningJob: {
                    signingPublicKey: leaf.signingPubKey,
                    rawTx: refundTx.toBytes(),
                    signingNonceCommitment: signingNonceCommitment,
                },
            };
            signingJobs.push(signingJob);
            const tx = getTxFromRawTxBytes(leaf.leaf.nodeTx);
            leafDataMap.set(leaf.leaf.id, {
                signingPubKey: leaf.signingPubKey,
                refundTx,
                signingNonceCommitment,
                tx,
                vout: leaf.leaf.vout,
                receivingPubkey: receiverPubKey,
            });
        }
        const sparkClient = await this.connectionManager.createSparkClient(this.config.getCoordinatorAddress());
        let response;
        try {
            response = await sparkClient.cooperative_exit({
                transfer: {
                    transferId: crypto.randomUUID(),
                    leavesToSend: signingJobs,
                    ownerIdentityPublicKey: await this.config.signer.getIdentityPublicKey(),
                    receiverIdentityPublicKey: receiverPubKey,
                    expiryTime: new Date(Date.now() + 24 * 60 * 1000),
                },
                exitId: crypto.randomUUID(),
                exitTxid: exitTxId,
            });
        }
        catch (error) {
            throw new Error(`Error initiating cooperative exit: ${error}`);
        }
        if (!response.transfer) {
            throw new Error("Failed to initiate cooperative exit");
        }
        const signatures = await this.signRefunds(leafDataMap, response.signingResults);
        const signaturesMap = new Map();
        for (const signature of signatures) {
            signaturesMap.set(signature.nodeId, signature.refundTxSignature);
        }
        return { transfer: response.transfer, signaturesMap };
    }
}
//# sourceMappingURL=coop-exit.js.map