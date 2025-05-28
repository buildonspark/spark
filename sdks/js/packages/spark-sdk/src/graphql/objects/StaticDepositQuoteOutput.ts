
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import BitcoinNetwork from './BitcoinNetwork.js';


interface StaticDepositQuoteOutput {


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

export const StaticDepositQuoteOutputFromJson = (obj: any): StaticDepositQuoteOutput => {
    return {
        transactionId: obj["static_deposit_quote_output_transaction_id"],
        outputIndex: obj["static_deposit_quote_output_output_index"],
        network: BitcoinNetwork[obj["static_deposit_quote_output_network"]] ?? BitcoinNetwork.FUTURE_VALUE,
        creditAmountSats: obj["static_deposit_quote_output_credit_amount_sats"],
        signature: obj["static_deposit_quote_output_signature"],

        } as StaticDepositQuoteOutput;

}
export const StaticDepositQuoteOutputToJson = (obj: StaticDepositQuoteOutput): any => {
return {
static_deposit_quote_output_transaction_id: obj.transactionId,
static_deposit_quote_output_output_index: obj.outputIndex,
static_deposit_quote_output_network: obj.network,
static_deposit_quote_output_credit_amount_sats: obj.creditAmountSats,
static_deposit_quote_output_signature: obj.signature,

        }

}


    export const FRAGMENT = `
fragment StaticDepositQuoteOutputFragment on StaticDepositQuoteOutput {
    __typename
    static_deposit_quote_output_transaction_id: transaction_id
    static_deposit_quote_output_output_index: output_index
    static_deposit_quote_output_network: network
    static_deposit_quote_output_credit_amount_sats: credit_amount_sats
    static_deposit_quote_output_signature: signature
}`;




export default StaticDepositQuoteOutput;
