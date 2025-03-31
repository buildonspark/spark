
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import BitcoinNetwork from './BitcoinNetwork.js';


interface RequestLightningReceiveInput {


    /** The bitcoin network the lightning invoice is created on. **/
network: BitcoinNetwork;

    /** The amount for which the lightning invoice should be created in satoshis. **/
amountSats: number;

    /** The 32-byte hash of the payment preimage to use when generating the lightning invoice. **/
paymentHash: string;

    /** The expiry of the lightning invoice in seconds. Default value is 86400 (1 day). **/
expirySecs?: number | undefined;

    /** The memo to include in the lightning invoice. **/
memo?: string | undefined;




}

export const RequestLightningReceiveInputFromJson = (obj: any): RequestLightningReceiveInput => {
    return {
        network: BitcoinNetwork[obj["request_lightning_receive_input_network"]] ?? BitcoinNetwork.FUTURE_VALUE,
        amountSats: obj["request_lightning_receive_input_amount_sats"],
        paymentHash: obj["request_lightning_receive_input_payment_hash"],
        expirySecs: obj["request_lightning_receive_input_expiry_secs"],
        memo: obj["request_lightning_receive_input_memo"],

        } as RequestLightningReceiveInput;

}
export const RequestLightningReceiveInputToJson = (obj: RequestLightningReceiveInput): any => {
return {
request_lightning_receive_input_network: obj.network,
request_lightning_receive_input_amount_sats: obj.amountSats,
request_lightning_receive_input_payment_hash: obj.paymentHash,
request_lightning_receive_input_expiry_secs: obj.expirySecs,
request_lightning_receive_input_memo: obj.memo,

        }

}





export default RequestLightningReceiveInput;
