import { Query } from "@lightsparkdev/core";
import CoopExitRequest from "./objects/CoopExitRequest.js";
import { BitcoinNetwork, CompleteCoopExitInput, CompleteLeavesSwapInput, CoopExitFeeEstimateInput, CoopExitFeeEstimateOutput, LightningSendRequest, RequestCoopExitInput, RequestLeavesSwapInput, RequestLightningReceiveInput, RequestLightningSendInput } from "./objects/index.js";
import LeavesSwapRequest from "./objects/LeavesSwapRequest.js";
import LightningReceiveFeeEstimateOutput from "./objects/LightningReceiveFeeEstimateOutput.js";
import LightningReceiveRequest from "./objects/LightningReceiveRequest.js";
import LightningSendFeeEstimateOutput from "./objects/LightningSendFeeEstimateOutput.js";
export default class SspClient {
    private readonly requester;
    private identityPublicKey;
    private readonly signingKey?;
    constructor(identityPublicKey: string);
    executeRawQuery<T>(query: Query<T>): Promise<T | null>;
    getLightningReceiveFeeEstimate(amountSats: number, network: BitcoinNetwork): Promise<LightningReceiveFeeEstimateOutput | null>;
    getLightningSendFeeEstimate(encodedInvoice: string): Promise<LightningSendFeeEstimateOutput | null>;
    getCoopExitFeeEstimate({ leafExternalIds, withdrawalAddress, }: CoopExitFeeEstimateInput): Promise<CoopExitFeeEstimateOutput | null>;
    getCurrentUser(): Promise<void>;
    completeCoopExit({ userOutboundTransferExternalId, coopExitRequestId, }: CompleteCoopExitInput): Promise<CoopExitRequest | null>;
    requestCoopExit({ leafExternalIds, withdrawalAddress, }: RequestCoopExitInput): Promise<CoopExitRequest | null>;
    requestLightningReceive({ amountSats, network, paymentHash, expirySecs, memo, }: RequestLightningReceiveInput): Promise<LightningReceiveRequest | null>;
    requestLightningSend({ encodedInvoice, idempotencyKey, }: RequestLightningSendInput): Promise<LightningSendRequest | null>;
    requestLeaveSwap({ adaptorPubkey, totalAmountSats, targetAmountSats, feeSats, userLeaves, }: RequestLeavesSwapInput): Promise<LeavesSwapRequest | null>;
    completeLeaveSwap({ adaptorSecretKey, userOutboundTransferExternalId, leavesSwapRequestId, }: CompleteLeavesSwapInput): Promise<LeavesSwapRequest | null>;
}
