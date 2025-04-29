
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface CoopExitFeeEstimatesInput {


    leafExternalIds: string[];

    withdrawalAddress: string;




}

export const CoopExitFeeEstimatesInputFromJson = (obj: any): CoopExitFeeEstimatesInput => {
    return {
        leafExternalIds: obj["coop_exit_fee_estimates_input_leaf_external_ids"],
        withdrawalAddress: obj["coop_exit_fee_estimates_input_withdrawal_address"],

        } as CoopExitFeeEstimatesInput;

}
export const CoopExitFeeEstimatesInputToJson = (obj: CoopExitFeeEstimatesInput): any => {
return {
coop_exit_fee_estimates_input_leaf_external_ids: obj.leafExternalIds,
coop_exit_fee_estimates_input_withdrawal_address: obj.withdrawalAddress,

        }

}





export default CoopExitFeeEstimatesInput;
