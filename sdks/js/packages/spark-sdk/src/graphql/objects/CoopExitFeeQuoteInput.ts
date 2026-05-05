
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface CoopExitFeeQuoteInput {


    leafExternalIds: string[];

    withdrawalAddress: string;




}

export const CoopExitFeeQuoteInputFromJson = (obj: any): CoopExitFeeQuoteInput => {
    return {
        leafExternalIds: obj["coop_exit_fee_quote_input_leaf_external_ids"],
        withdrawalAddress: obj["coop_exit_fee_quote_input_withdrawal_address"],

        };

}
export const CoopExitFeeQuoteInputToJson = (obj: CoopExitFeeQuoteInput): any => {
return {
coop_exit_fee_quote_input_leaf_external_ids: obj.leafExternalIds,
coop_exit_fee_quote_input_withdrawal_address: obj.withdrawalAddress,

        }

}





export default CoopExitFeeQuoteInput;
