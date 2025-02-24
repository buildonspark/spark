// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const CoopExitFeeEstimateInputFromJson = (obj) => {
    return {
        leafExternalIds: obj["coop_exit_fee_estimate_input_leaf_external_ids"],
        withdrawalAddress: obj["coop_exit_fee_estimate_input_withdrawal_address"],
    };
};
export const CoopExitFeeEstimateInputToJson = (obj) => {
    return {
        coop_exit_fee_estimate_input_leaf_external_ids: obj.leafExternalIds,
        coop_exit_fee_estimate_input_withdrawal_address: obj.withdrawalAddress,
    };
};
//# sourceMappingURL=CoopExitFeeEstimateInput.js.map