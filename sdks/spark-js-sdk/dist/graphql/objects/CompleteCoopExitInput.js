// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const CompleteCoopExitInputFromJson = (obj) => {
    return {
        userOutboundTransferExternalId: obj["complete_coop_exit_input_user_outbound_transfer_external_id"],
        coopExitRequestId: obj["complete_coop_exit_input_coop_exit_request_id"],
    };
};
export const CompleteCoopExitInputToJson = (obj) => {
    return {
        complete_coop_exit_input_user_outbound_transfer_external_id: obj.userOutboundTransferExternalId,
        complete_coop_exit_input_coop_exit_request_id: obj.coopExitRequestId,
    };
};
//# sourceMappingURL=CompleteCoopExitInput.js.map