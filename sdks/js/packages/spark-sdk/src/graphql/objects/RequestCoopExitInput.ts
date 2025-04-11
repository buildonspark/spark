
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface RequestCoopExitInput {


    leafExternalIds: string[];

    withdrawalAddress: string;

    idempotencyKey?: string | undefined;




}

export const RequestCoopExitInputFromJson = (obj: any): RequestCoopExitInput => {
    return {
        leafExternalIds: obj["request_coop_exit_input_leaf_external_ids"],
        withdrawalAddress: obj["request_coop_exit_input_withdrawal_address"],
        idempotencyKey: obj["request_coop_exit_input_idempotency_key"],

        } as RequestCoopExitInput;

}
export const RequestCoopExitInputToJson = (obj: RequestCoopExitInput): any => {
return {
request_coop_exit_input_leaf_external_ids: obj.leafExternalIds,
request_coop_exit_input_withdrawal_address: obj.withdrawalAddress,
request_coop_exit_input_idempotency_key: obj.idempotencyKey,

        }

}





export default RequestCoopExitInput;
