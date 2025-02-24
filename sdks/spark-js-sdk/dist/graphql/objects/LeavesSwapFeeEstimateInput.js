// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const LeavesSwapFeeEstimateInputFromJson = (obj) => {
    return {
        totalAmountSats: obj["leaves_swap_fee_estimate_input_total_amount_sats"],
    };
};
export const LeavesSwapFeeEstimateInputToJson = (obj) => {
    return {
        leaves_swap_fee_estimate_input_total_amount_sats: obj.totalAmountSats,
    };
};
//# sourceMappingURL=LeavesSwapFeeEstimateInput.js.map