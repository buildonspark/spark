import BitcoinNetwork from './BitcoinNetwork.js';
interface RequestLightningReceiveInput {
    /** The bitcoin network the lightning invoice is created on. **/
    network: BitcoinNetwork;
    /** The amount for which the lightning invoice should be created in satoshis. **/
    amountSats: number;
    /** The 32-byte hash of the payment preimage to use when generating the lightning invoice. **/
    paymentHash: string;
    /** The expiry of the lightning invoice in seconds. Default value is 3600 (1 hour). **/
    expirySecs?: number | undefined;
    /** The memo to include in the lightning invoice. **/
    memo?: string | undefined;
}
export declare const RequestLightningReceiveInputFromJson: (obj: any) => RequestLightningReceiveInput;
export declare const RequestLightningReceiveInputToJson: (obj: RequestLightningReceiveInput) => any;
export default RequestLightningReceiveInput;
