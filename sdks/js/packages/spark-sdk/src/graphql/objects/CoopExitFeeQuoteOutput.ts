
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface CoopExitFeeQuoteOutput {


    quoteId: string;




}

export const CoopExitFeeQuoteOutputFromJson = (obj: any): CoopExitFeeQuoteOutput => {
    return {
        quoteId: obj["coop_exit_fee_quote_output_quote"].id,

        };

}
export const CoopExitFeeQuoteOutputToJson = (obj: CoopExitFeeQuoteOutput): any => {
return {
coop_exit_fee_quote_output_quote: { id: obj.quoteId },

        }

}


    export const FRAGMENT = `
fragment CoopExitFeeQuoteOutputFragment on CoopExitFeeQuoteOutput {
    __typename
    coop_exit_fee_quote_output_quote: quote {
        id
    }
}`;




export default CoopExitFeeQuoteOutput;
