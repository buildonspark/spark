
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import ExitSpeed from './ExitSpeed.js';


interface RequestCoopExitInput {


    leafExternalIds: string[];

    withdrawalAddress: string;

    idempotencyKey: string;

    exitSpeed: ExitSpeed;

    withdrawAll: boolean;




}

export const RequestCoopExitInputFromJson = (obj: any): RequestCoopExitInput => {
    return {
        leafExternalIds: obj["request_coop_exit_input_leaf_external_ids"],
        withdrawalAddress: obj["request_coop_exit_input_withdrawal_address"],
        idempotencyKey: obj["request_coop_exit_input_idempotency_key"],
        exitSpeed: ExitSpeed[obj["request_coop_exit_input_exit_speed"]] ?? ExitSpeed.FUTURE_VALUE,
        withdrawAll: obj["request_coop_exit_input_withdraw_all"],

        } as RequestCoopExitInput;

}
export const RequestCoopExitInputToJson = (obj: RequestCoopExitInput): any => {
return {
request_coop_exit_input_leaf_external_ids: obj.leafExternalIds,
request_coop_exit_input_withdrawal_address: obj.withdrawalAddress,
request_coop_exit_input_idempotency_key: obj.idempotencyKey,
request_coop_exit_input_exit_speed: obj.exitSpeed,
request_coop_exit_input_withdraw_all: obj.withdrawAll,

        }

}





export default RequestCoopExitInput;
