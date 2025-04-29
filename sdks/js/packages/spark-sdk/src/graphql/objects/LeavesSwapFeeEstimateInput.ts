
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface LeavesSwapFeeEstimateInput {


    totalAmountSats: number;




}

export const LeavesSwapFeeEstimateInputFromJson = (obj: any): LeavesSwapFeeEstimateInput => {
    return {
        totalAmountSats: obj["leaves_swap_fee_estimate_input_total_amount_sats"],

        } as LeavesSwapFeeEstimateInput;

}
export const LeavesSwapFeeEstimateInputToJson = (obj: LeavesSwapFeeEstimateInput): any => {
return {
leaves_swap_fee_estimate_input_total_amount_sats: obj.totalAmountSats,

        }

}





export default LeavesSwapFeeEstimateInput;
