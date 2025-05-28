
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import BitcoinNetwork from './BitcoinNetwork.js';


interface StaticDepositQuoteInput {


    /** The transaction id of the deposit. **/
transactionId: string;

    /** The output index of the deposit. **/
outputIndex: number;

    /** The bitcoin network of the deposit. **/
network: BitcoinNetwork;




}

export const StaticDepositQuoteInputFromJson = (obj: any): StaticDepositQuoteInput => {
    return {
        transactionId: obj["static_deposit_quote_input_transaction_id"],
        outputIndex: obj["static_deposit_quote_input_output_index"],
        network: BitcoinNetwork[obj["static_deposit_quote_input_network"]] ?? BitcoinNetwork.FUTURE_VALUE,

        } as StaticDepositQuoteInput;

}
export const StaticDepositQuoteInputToJson = (obj: StaticDepositQuoteInput): any => {
return {
static_deposit_quote_input_transaction_id: obj.transactionId,
static_deposit_quote_input_output_index: obj.outputIndex,
static_deposit_quote_input_network: obj.network,

        }

}





export default StaticDepositQuoteInput;
