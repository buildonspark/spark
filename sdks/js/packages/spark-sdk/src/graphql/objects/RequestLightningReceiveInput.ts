
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import BitcoinNetwork from './BitcoinNetwork.js';


interface RequestLightningReceiveInput {


    /** The bitcoin network the lightning invoice is created on. **/
network: BitcoinNetwork;

    /** The amount for which the lightning invoice should be created in satoshis. **/
amountSats: number;

    /** The 32-byte hash of the payment preimage to use when generating the lightning invoice. **/
paymentHash: string;

    /**
 * Whether to embed the spark address in the fallback address field of the Bolt 11 lightning invoice.
 * Spark-aware wallets can use this field to preferentially pay over spark if they find a spark
 * address in the fallback address field.
**/
includeSparkAddress: boolean;

    /** The expiry of the lightning invoice in seconds. Default value is 86400 (1 day). **/
expirySecs?: number | undefined;

    /**
 * The memo to include in the lightning invoice. Should not be provided if the description_hash is
 * provided.
**/
memo?: string | undefined;

    /**
 * The public key of the user receiving the lightning invoice. If not present, the receiver will be
 * the creator of this request.
**/
receiverIdentityPubkey?: string | undefined;

    /**
 * The h tag of the invoice. This is the hash of a longer description to include in the lightning
 * invoice. It is used in LNURL and UMA as the hash of the metadata. This field is mutually exclusive
 * with the memo field. Only one or the other should be provided.
**/
descriptionHash?: string | undefined;

    /**
 * The spark invoice to embed in the routing hints of the Bolt 11 lightning invoice.
 * This is mutually exclusive with includeSparkAddress.
**/
sparkInvoice?: string | undefined;




}

export const RequestLightningReceiveInputFromJson = (obj: any): RequestLightningReceiveInput => {
    return {
        network: BitcoinNetwork[obj["request_lightning_receive_input_network"]] ?? BitcoinNetwork.FUTURE_VALUE,
        amountSats: obj["request_lightning_receive_input_amount_sats"],
        paymentHash: obj["request_lightning_receive_input_payment_hash"],
        includeSparkAddress: obj["request_lightning_receive_input_include_spark_address"],
        expirySecs: obj["request_lightning_receive_input_expiry_secs"],
        memo: obj["request_lightning_receive_input_memo"],
        receiverIdentityPubkey: obj["request_lightning_receive_input_receiver_identity_pubkey"],
        descriptionHash: obj["request_lightning_receive_input_description_hash"],
        sparkInvoice: obj["request_lightning_receive_input_spark_invoice"],

        } as RequestLightningReceiveInput;

}
export const RequestLightningReceiveInputToJson = (obj: RequestLightningReceiveInput): any => {
return {
request_lightning_receive_input_network: obj.network,
request_lightning_receive_input_amount_sats: obj.amountSats,
request_lightning_receive_input_payment_hash: obj.paymentHash,
request_lightning_receive_input_expiry_secs: obj.expirySecs,
request_lightning_receive_input_memo: obj.memo,
request_lightning_receive_input_receiver_identity_pubkey: obj.receiverIdentityPubkey,
request_lightning_receive_input_include_spark_address: obj.includeSparkAddress,
request_lightning_receive_input_description_hash: obj.descriptionHash,
request_lightning_receive_input_spark_invoice: obj.sparkInvoice,

        }

}





export default RequestLightningReceiveInput;
