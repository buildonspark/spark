import { InitiatePreimageSwapResponse, Transfer, UserSignedRefund } from "../proto/spark.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection.js";
import { LeafKeyTweak } from "./transfer.js";
export type CreateLightningInvoiceParams = {
    invoiceCreator: (amountSats: number, paymentHash: Uint8Array, memo: string) => Promise<string | undefined>;
    amountSats: number;
    memo: string;
};
export type CreateLightningInvoiceWithPreimageParams = {
    preimage: Uint8Array;
} & CreateLightningInvoiceParams;
export type SwapNodesForPreimageParams = {
    leaves: LeafKeyTweak[];
    receiverIdentityPubkey: Uint8Array;
    paymentHash: Uint8Array;
    invoiceString?: string;
    isInboundPayment: boolean;
};
export declare class LightningService {
    private readonly config;
    private readonly connectionManager;
    constructor(config: WalletConfigService, connectionManager: ConnectionManager);
    createLightningInvoice({ invoiceCreator, amountSats, memo, }: CreateLightningInvoiceParams): Promise<string>;
    createLightningInvoiceWithPreImage({ invoiceCreator, amountSats, memo, preimage, }: CreateLightningInvoiceWithPreimageParams): Promise<string>;
    swapNodesForPreimage({ leaves, receiverIdentityPubkey, paymentHash, invoiceString, isInboundPayment, }: SwapNodesForPreimageParams): Promise<InitiatePreimageSwapResponse>;
    queryUserSignedRefunds(paymentHash: Uint8Array): Promise<UserSignedRefund[]>;
    validateUserSignedRefund(userSignedRefund: UserSignedRefund): bigint;
    providePreimage(preimage: Uint8Array): Promise<Transfer>;
    private signRefunds;
}
