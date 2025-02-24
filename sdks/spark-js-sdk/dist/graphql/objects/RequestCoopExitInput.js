// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const RequestCoopExitInputFromJson = (obj) => {
    return {
        leafExternalIds: obj["request_coop_exit_input_leaf_external_ids"],
        withdrawalAddress: obj["request_coop_exit_input_withdrawal_address"],
    };
};
export const RequestCoopExitInputToJson = (obj) => {
    return {
        request_coop_exit_input_leaf_external_ids: obj.leafExternalIds,
        request_coop_exit_input_withdrawal_address: obj.withdrawalAddress,
    };
};
//# sourceMappingURL=RequestCoopExitInput.js.map