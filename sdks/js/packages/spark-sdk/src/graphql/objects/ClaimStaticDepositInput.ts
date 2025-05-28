
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import ClaimStaticDepositRequestType from './ClaimStaticDepositRequestType.js';
import BitcoinNetwork from './BitcoinNetwork.js';


interface ClaimStaticDepositInput {


    /** The transaction id of the deposit. **/
transactionId: string;

    /** The output index of the deposit. **/
outputIndex: number;

    /** The bitcoin network of the deposit. **/
network: BitcoinNetwork;

    /** The type of the claim request. **/
requestType: ClaimStaticDepositRequestType;

    /** The deposit key of the user. **/
depositSecretKey: string;

    /** The signature of the claim provided by the user. **/
signature: string;

    /** The signature of the quote provided by the SSP. **/
quoteSignature: string;

    /** The amount of sats to claim for FIXED_AMOUNT quote. **/
creditAmountSats?: number | undefined;

    /** The amount of sats to deduct from the UTXO value for MAX_FEE quote. **/
maxFeeSats?: number | undefined;




}

export const ClaimStaticDepositInputFromJson = (obj: any): ClaimStaticDepositInput => {
    return {
        transactionId: obj["claim_static_deposit_input_transaction_id"],
        outputIndex: obj["claim_static_deposit_input_output_index"],
        network: BitcoinNetwork[obj["claim_static_deposit_input_network"]] ?? BitcoinNetwork.FUTURE_VALUE,
        requestType: ClaimStaticDepositRequestType[obj["claim_static_deposit_input_request_type"]] ?? ClaimStaticDepositRequestType.FUTURE_VALUE,
        depositSecretKey: obj["claim_static_deposit_input_deposit_secret_key"],
        signature: obj["claim_static_deposit_input_signature"],
        quoteSignature: obj["claim_static_deposit_input_quote_signature"],
        creditAmountSats: obj["claim_static_deposit_input_credit_amount_sats"],
        maxFeeSats: obj["claim_static_deposit_input_max_fee_sats"],

        } as ClaimStaticDepositInput;

}
export const ClaimStaticDepositInputToJson = (obj: ClaimStaticDepositInput): any => {
return {
claim_static_deposit_input_transaction_id: obj.transactionId,
claim_static_deposit_input_output_index: obj.outputIndex,
claim_static_deposit_input_network: obj.network,
claim_static_deposit_input_request_type: obj.requestType,
claim_static_deposit_input_credit_amount_sats: obj.creditAmountSats,
claim_static_deposit_input_max_fee_sats: obj.maxFeeSats,
claim_static_deposit_input_deposit_secret_key: obj.depositSecretKey,
claim_static_deposit_input_signature: obj.signature,
claim_static_deposit_input_quote_signature: obj.quoteSignature,

        }

}





export default ClaimStaticDepositInput;
