import { bytesToNumberBE, numberToBytesBE } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { sha256 } from "@scure/btc-signer/utils";
import { decode } from "light-bolt11-decoder";
import { InitiatePreimageSwapRequest_Reason, } from "../proto/spark.js";
import { getTxFromRawTxBytes } from "../utils/bitcoin.js";
import { getCrypto } from "../utils/crypto.js";
import { createRefundTx } from "../utils/transaction.js";
const crypto = getCrypto();
export class LightningService {
    config;
    connectionManager;
    constructor(config, connectionManager) {
        this.config = config;
        this.connectionManager = connectionManager;
    }
    async createLightningInvoice({ invoiceCreator, amountSats, memo, }) {
        const randBytes = crypto.getRandomValues(new Uint8Array(32));
        const preimage = numberToBytesBE(bytesToNumberBE(randBytes) % secp256k1.CURVE.n, 32);
        return await this.createLightningInvoiceWithPreImage({
            invoiceCreator,
            amountSats,
            memo,
            preimage,
        });
    }
    async createLightningInvoiceWithPreImage({ invoiceCreator, amountSats, memo, preimage, }) {
        const paymentHash = sha256(preimage);
        const invoice = await invoiceCreator(amountSats, paymentHash, memo);
        if (!invoice) {
            throw new Error("Error creating lightning invoice");
        }
        const shares = await this.config.signer.splitSecretWithProofs({
            secret: preimage,
            curveOrder: secp256k1.CURVE.n,
            threshold: this.config.getConfig().threshold,
            numShares: Object.keys(this.config.getConfig().signingOperators).length,
        });
        const errors = [];
        const promises = Object.entries(this.config.getConfig().signingOperators).map(async ([_, operator]) => {
            const share = shares[operator.id];
            if (!share) {
                throw new Error("Share not found");
            }
            const sparkClient = await this.connectionManager.createSparkClient(operator.address);
            try {
                await sparkClient.store_preimage_share({
                    paymentHash,
                    preimageShare: {
                        secretShare: numberToBytesBE(share.share, 32),
                        proofs: share.proofs,
                    },
                    threshold: this.config.getConfig().threshold,
                    invoiceString: invoice,
                    userIdentityPublicKey: await this.config.signer.getIdentityPublicKey(),
                });
            }
            catch (e) {
                errors.push(e);
            }
        });
        await Promise.all(promises);
        if (errors.length > 0) {
            throw new Error(`Error creating lightning invoice: ${errors[0]}`);
        }
        return invoice;
    }
    async swapNodesForPreimage({ leaves, receiverIdentityPubkey, paymentHash, invoiceString, isInboundPayment, }) {
        const sparkClient = await this.connectionManager.createSparkClient(this.config.getCoordinatorAddress());
        let signingCommitments;
        try {
            signingCommitments = await sparkClient.get_signing_commitments({
                nodeIds: leaves.map((leaf) => leaf.leaf.id),
            });
        }
        catch (error) {
            throw new Error(`Error getting signing commitments: ${error}`);
        }
        const userSignedRefunds = await this.signRefunds(leaves, signingCommitments.signingCommitments, receiverIdentityPubkey);
        const transferId = crypto.randomUUID();
        let bolt11String = "";
        let amountSats = 0;
        if (invoiceString) {
            const decodedInvoice = decode(invoiceString);
            let amountMsats = 0;
            try {
                amountMsats = Number(decodedInvoice.sections.find((section) => section.name === "amount")
                    ?.value);
            }
            catch (error) {
                console.error("Error decoding invoice", error);
            }
            amountSats = amountMsats / 1000;
            bolt11String = invoiceString;
        }
        const reason = isInboundPayment
            ? InitiatePreimageSwapRequest_Reason.REASON_RECEIVE
            : InitiatePreimageSwapRequest_Reason.REASON_SEND;
        let response;
        try {
            response = await sparkClient.initiate_preimage_swap({
                paymentHash,
                userSignedRefunds,
                reason,
                invoiceAmount: {
                    invoiceAmountProof: {
                        bolt11Invoice: bolt11String,
                    },
                    valueSats: amountSats,
                },
                transfer: {
                    transferId,
                    ownerIdentityPublicKey: await this.config.signer.getIdentityPublicKey(),
                    receiverIdentityPublicKey: receiverIdentityPubkey,
                },
                receiverIdentityPublicKey: receiverIdentityPubkey,
                feeSats: 0,
            });
        }
        catch (error) {
            throw new Error(`Error initiating preimage swap: ${error}`);
        }
        return response;
    }
    async queryUserSignedRefunds(paymentHash) {
        const sparkClient = await this.connectionManager.createSparkClient(this.config.getCoordinatorAddress());
        let response;
        try {
            response = await sparkClient.query_user_signed_refunds({
                paymentHash,
            });
        }
        catch (error) {
            throw new Error(`Error querying user signed refunds: ${error}`);
        }
        return response.userSignedRefunds;
    }
    validateUserSignedRefund(userSignedRefund) {
        const refundTx = getTxFromRawTxBytes(userSignedRefund.refundTx);
        // TODO: Should we assert that the amount is always defined here?
        return refundTx.getOutput(0).amount || 0n;
    }
    async providePreimage(preimage) {
        const sparkClient = await this.connectionManager.createSparkClient(this.config.getCoordinatorAddress());
        const paymentHash = sha256(preimage);
        let response;
        try {
            response = await sparkClient.provide_preimage({
                preimage,
                paymentHash,
            });
        }
        catch (error) {
            throw new Error(`Error providing preimage: ${error}`);
        }
        if (!response.transfer) {
            throw new Error("No transfer returned from coordinator");
        }
        return response.transfer;
    }
    async signRefunds(leaves, signingCommitments, receiverIdentityPubkey) {
        const userSignedRefunds = [];
        for (let i = 0; i < leaves.length; i++) {
            const leaf = leaves[i];
            if (!leaf?.leaf) {
                throw new Error("Leaf not found in signRefunds");
            }
            const { refundTx, sighash } = createRefundTx(leaf.leaf, receiverIdentityPubkey, this.config.getNetwork());
            const signingCommitment = await this.config.signer.getRandomSigningCommitment();
            const signingNonceCommitments = signingCommitments[i]?.signingNonceCommitments;
            if (!signingNonceCommitments) {
                throw new Error("Signing nonce commitments not found in signRefunds");
            }
            const signingResult = await this.config.signer.signFrost({
                message: sighash,
                publicKey: leaf.signingPubKey,
                privateAsPubKey: leaf.signingPubKey,
                selfCommitment: signingCommitment,
                statechainCommitments: signingNonceCommitments,
                adaptorPubKey: new Uint8Array(),
                verifyingKey: leaf.leaf.verifyingPublicKey,
            });
            userSignedRefunds.push({
                nodeId: leaf.leaf.id,
                refundTx: refundTx.toBytes(),
                userSignature: signingResult,
                userSignatureCommitment: signingCommitment,
                signingCommitments: {
                    signingCommitments: signingNonceCommitments,
                },
            });
        }
        return userSignedRefunds;
    }
}
//# sourceMappingURL=lightning.js.map