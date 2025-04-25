
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import BitcoinNetwork from './BitcoinNetwork.js';


interface RequestClaimDepositQuoteOutput {


    /** The transaction id of the deposit. **/
transactionId: string;

    /** The output index of the deposit. **/
outputIndex: number;

    /** The bitcoin network of the deposit. **/
network: BitcoinNetwork;

    /** The amount of sats that will be credited to the user's balance. **/
creditAmountSats: number;

    /** The signature of the quote. **/
signature: string;




}

export const RequestClaimDepositQuoteOutputFromJson = (obj: any): RequestClaimDepositQuoteOutput => {
    return {
        transactionId: obj["request_claim_deposit_quote_output_transaction_id"],
        outputIndex: obj["request_claim_deposit_quote_output_output_index"],
        network: BitcoinNetwork[obj["request_claim_deposit_quote_output_network"]] ?? BitcoinNetwork.FUTURE_VALUE,
        creditAmountSats: obj["request_claim_deposit_quote_output_credit_amount_sats"],
        signature: obj["request_claim_deposit_quote_output_signature"],

        } as RequestClaimDepositQuoteOutput;

}
export const RequestClaimDepositQuoteOutputToJson = (obj: RequestClaimDepositQuoteOutput): any => {
return {
request_claim_deposit_quote_output_transaction_id: obj.transactionId,
request_claim_deposit_quote_output_output_index: obj.outputIndex,
request_claim_deposit_quote_output_network: obj.network,
request_claim_deposit_quote_output_credit_amount_sats: obj.creditAmountSats,
request_claim_deposit_quote_output_signature: obj.signature,

        }

}


    export const FRAGMENT = `
fragment RequestClaimDepositQuoteOutputFragment on RequestClaimDepositQuoteOutput {
    __typename
    request_claim_deposit_quote_output_transaction_id: transaction_id
    request_claim_deposit_quote_output_output_index: output_index
    request_claim_deposit_quote_output_network: network
    request_claim_deposit_quote_output_credit_amount_sats: credit_amount_sats
    request_claim_deposit_quote_output_signature: signature
}`;




export default RequestClaimDepositQuoteOutput;
