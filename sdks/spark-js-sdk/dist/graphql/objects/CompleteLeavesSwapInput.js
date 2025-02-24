// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const CompleteLeavesSwapInputFromJson = (obj) => {
    return {
        adaptorSecretKey: obj["complete_leaves_swap_input_adaptor_secret_key"],
        userOutboundTransferExternalId: obj["complete_leaves_swap_input_user_outbound_transfer_external_id"],
        leavesSwapRequestId: obj["complete_leaves_swap_input_leaves_swap_request_id"],
    };
};
export const CompleteLeavesSwapInputToJson = (obj) => {
    return {
        complete_leaves_swap_input_adaptor_secret_key: obj.adaptorSecretKey,
        complete_leaves_swap_input_user_outbound_transfer_external_id: obj.userOutboundTransferExternalId,
        complete_leaves_swap_input_leaves_swap_request_id: obj.leavesSwapRequestId,
    };
};
//# sourceMappingURL=CompleteLeavesSwapInput.js.map