import { DefaultCrypto, NodeKeyCache, Requester, } from "@lightsparkdev/core";
import { CompleteCoopExit } from "./mutations/CompleteCoopExit.js";
import { CompleteLeavesSwap } from "./mutations/CompleteLeavesSwap.js";
import { RequestCoopExit } from "./mutations/RequestCoopExit.js";
import { RequestLightningReceive } from "./mutations/RequestLightningReceive.js";
import { RequestLightningSend } from "./mutations/RequestLightningSend.js";
import { RequestSwapLeaves } from "./mutations/RequestSwapLeaves.js";
import { CoopExitFeeEstimateOutputFromJson } from "./objects/CoopExitFeeEstimateOutput.js";
import { CoopExitRequestFromJson, } from "./objects/CoopExitRequest.js";
import { LeavesSwapRequestFromJson, } from "./objects/LeavesSwapRequest.js";
import { LightningReceiveFeeEstimateOutputFromJson, } from "./objects/LightningReceiveFeeEstimateOutput.js";
import { LightningReceiveRequestFromJson, } from "./objects/LightningReceiveRequest.js";
import { LightningSendFeeEstimateOutputFromJson, } from "./objects/LightningSendFeeEstimateOutput.js";
import { LightningSendRequestFromJson } from "./objects/LightningSendRequest.js";
import { CoopExitFeeEstimate } from "./queries/CoopExitFeeEstimate.js";
import { LightningReceiveFeeEstimate } from "./queries/LightningReceiveFeeEstimate.js";
import { LightningSendFeeEstimate } from "./queries/LightningSendFeeEstimate.js";
export default class SspClient {
    requester;
    identityPublicKey;
    signingKey;
    constructor(identityPublicKey) {
        this.identityPublicKey = identityPublicKey;
        const fetchFunction = typeof window !== "undefined" ? window.fetch.bind(window) : fetch;
        this.requester = new Requester(new NodeKeyCache(DefaultCrypto), "graphql/spark/rc", "spark-js-sdk/v1.0.0  ", new SparkAuthProvider(identityPublicKey), "https://api.dev.dev.sparkinfra.net", DefaultCrypto, this.signingKey, fetchFunction);
    }
    async executeRawQuery(query) {
        return await this.requester.executeQuery(query);
    }
    async getLightningReceiveFeeEstimate(amountSats, network) {
        return await this.executeRawQuery({
            queryPayload: LightningReceiveFeeEstimate,
            variables: {
                amount_sats: amountSats,
                network: network,
            },
            constructObject: (response) => {
                return LightningReceiveFeeEstimateOutputFromJson(response.lightning_receive_fee_estimate);
            },
        });
    }
    async getLightningSendFeeEstimate(encodedInvoice) {
        return await this.executeRawQuery({
            queryPayload: LightningSendFeeEstimate,
            variables: {
                encoded_invoice: encodedInvoice,
            },
            constructObject: (response) => {
                return LightningSendFeeEstimateOutputFromJson(response.lightning_send_fee_estimate);
            },
        });
    }
    async getCoopExitFeeEstimate({ leafExternalIds, withdrawalAddress, }) {
        return await this.executeRawQuery({
            queryPayload: CoopExitFeeEstimate,
            variables: {
                leaf_external_ids: leafExternalIds,
                withdrawal_address: withdrawalAddress,
            },
            constructObject: (response) => {
                return CoopExitFeeEstimateOutputFromJson(response.coop_exit_fee_estimate);
            },
        });
    }
    // TODO: Might not need
    async getCurrentUser() {
        throw new Error("Not implemented");
    }
    async completeCoopExit({ userOutboundTransferExternalId, coopExitRequestId, }) {
        return await this.executeRawQuery({
            queryPayload: CompleteCoopExit,
            variables: {
                user_outbound_transfer_external_id: userOutboundTransferExternalId,
                coop_exit_request_id: coopExitRequestId,
            },
            constructObject: (response) => {
                return CoopExitRequestFromJson(response.complete_coop_exit.request);
            },
        });
    }
    async requestCoopExit({ leafExternalIds, withdrawalAddress, }) {
        return await this.executeRawQuery({
            queryPayload: RequestCoopExit,
            variables: {
                leaf_external_ids: leafExternalIds,
                withdrawal_address: withdrawalAddress,
            },
            constructObject: (response) => {
                return CoopExitRequestFromJson(response.request_coop_exit.request);
            },
        });
    }
    // TODO: Lets name this better
    async requestLightningReceive({ amountSats, network, paymentHash, expirySecs, memo, }) {
        return await this.executeRawQuery({
            queryPayload: RequestLightningReceive,
            variables: {
                amount_sats: amountSats,
                network: network,
                payment_hash: paymentHash,
                expiry_secs: expirySecs,
                memo: memo,
            },
            constructObject: (response) => {
                return LightningReceiveRequestFromJson(response.request_lightning_receive.request);
            },
        });
    }
    async requestLightningSend({ encodedInvoice, idempotencyKey, }) {
        return await this.executeRawQuery({
            queryPayload: RequestLightningSend,
            variables: {
                encoded_invoice: encodedInvoice,
                idempotency_key: idempotencyKey,
            },
            constructObject: (response) => {
                return LightningSendRequestFromJson(response.request_lightning_send.request);
            },
        });
    }
    async requestLeaveSwap({ adaptorPubkey, totalAmountSats, targetAmountSats, feeSats, network, userLeaves, }) {
        const query = {
            queryPayload: RequestSwapLeaves,
            variables: {
                adaptor_pubkey: adaptorPubkey,
                total_amount_sats: totalAmountSats,
                target_amount_sats: targetAmountSats,
                fee_sats: feeSats,
                network: network,
                user_leaves: userLeaves,
            },
            constructObject: (response) => {
                if (!response.request_leaves_swap) {
                    return null;
                }
                return LeavesSwapRequestFromJson(response.request_leaves_swap.request);
            },
        };
        return await this.executeRawQuery(query);
    }
    async completeLeaveSwap({ adaptorSecretKey, userOutboundTransferExternalId, leavesSwapRequestId, }) {
        return await this.executeRawQuery({
            queryPayload: CompleteLeavesSwap,
            variables: {
                adaptor_secret_key: adaptorSecretKey,
                user_outbound_transfer_external_id: userOutboundTransferExternalId,
                leaves_swap_request_id: leavesSwapRequestId,
            },
            constructObject: (response) => {
                return LeavesSwapRequestFromJson(response.complete_leaves_swap.request);
            },
        });
    }
}
class SparkAuthProvider {
    publicKey;
    constructor(publicKey) {
        this.publicKey = publicKey;
    }
    async addAuthHeaders(headers) {
        const _headers = {
            "Spark-Identity-Public-Key": this.publicKey,
            "Content-Type": "application/json",
        };
        return Promise.resolve(_headers);
    }
    async isAuthorized() {
        return Promise.resolve(true);
    }
    async addWsConnectionParams(params) {
        return Promise.resolve({
            ...params,
            "Spark-Identity-Public-Key": this.publicKey,
        });
    }
}
//# sourceMappingURL=client.js.map