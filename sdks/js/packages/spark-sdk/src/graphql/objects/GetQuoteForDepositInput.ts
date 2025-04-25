
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import BitcoinNetwork from './BitcoinNetwork.js';


interface GetQuoteForDepositInput {


    /** The transaction id of the deposit. **/
transactionId: string;

    /** The output index of the deposit. **/
outputIndex: number;

    /** The bitcoin network of the deposit. **/
network: BitcoinNetwork;




}

export const GetQuoteForDepositInputFromJson = (obj: any): GetQuoteForDepositInput => {
    return {
        transactionId: obj["get_quote_for_deposit_input_transaction_id"],
        outputIndex: obj["get_quote_for_deposit_input_output_index"],
        network: BitcoinNetwork[obj["get_quote_for_deposit_input_network"]] ?? BitcoinNetwork.FUTURE_VALUE,

        } as GetQuoteForDepositInput;

}
export const GetQuoteForDepositInputToJson = (obj: GetQuoteForDepositInput): any => {
return {
get_quote_for_deposit_input_transaction_id: obj.transactionId,
get_quote_for_deposit_input_output_index: obj.outputIndex,
get_quote_for_deposit_input_network: obj.network,

        }

}





export default GetQuoteForDepositInput;
