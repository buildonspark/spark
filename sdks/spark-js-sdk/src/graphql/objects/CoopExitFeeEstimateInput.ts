
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface CoopExitFeeEstimateInput {


    leafExternalIds: string[];

    withdrawalAddress: string;




}

export const CoopExitFeeEstimateInputFromJson = (obj: any): CoopExitFeeEstimateInput => {
    return {
        leafExternalIds: obj["coop_exit_fee_estimate_input_leaf_external_ids"],
        withdrawalAddress: obj["coop_exit_fee_estimate_input_withdrawal_address"],

        } as CoopExitFeeEstimateInput;

}
export const CoopExitFeeEstimateInputToJson = (obj: CoopExitFeeEstimateInput): any => {
return {
coop_exit_fee_estimate_input_leaf_external_ids: obj.leafExternalIds,
coop_exit_fee_estimate_input_withdrawal_address: obj.withdrawalAddress,

        }

}





export default CoopExitFeeEstimateInput;
