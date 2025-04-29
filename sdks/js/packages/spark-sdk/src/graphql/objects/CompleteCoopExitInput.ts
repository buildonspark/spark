
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface CompleteCoopExitInput {


    userOutboundTransferExternalId: string;

    coopExitRequestId: string;




}

export const CompleteCoopExitInputFromJson = (obj: any): CompleteCoopExitInput => {
    return {
        userOutboundTransferExternalId: obj["complete_coop_exit_input_user_outbound_transfer_external_id"],
        coopExitRequestId: obj["complete_coop_exit_input_coop_exit_request_id"],

        } as CompleteCoopExitInput;

}
export const CompleteCoopExitInputToJson = (obj: CompleteCoopExitInput): any => {
return {
complete_coop_exit_input_user_outbound_transfer_external_id: obj.userOutboundTransferExternalId,
complete_coop_exit_input_coop_exit_request_id: obj.coopExitRequestId,

        }

}





export default CompleteCoopExitInput;
