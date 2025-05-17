
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import {UserLeafInputToJson} from './UserLeafInput.js';
import UserLeafInput from './UserLeafInput.js';
import {UserLeafInputFromJson} from './UserLeafInput.js';


interface RequestLeavesSwapInput {


    adaptorPubkey: string;

    totalAmountSats: number;

    targetAmountSats: number;

    feeSats: number;

    userLeaves: UserLeafInput[];

    idempotencyKey: string;




}

export const RequestLeavesSwapInputFromJson = (obj: any): RequestLeavesSwapInput => {
    return {
        adaptorPubkey: obj["request_leaves_swap_input_adaptor_pubkey"],
        totalAmountSats: obj["request_leaves_swap_input_total_amount_sats"],
        targetAmountSats: obj["request_leaves_swap_input_target_amount_sats"],
        feeSats: obj["request_leaves_swap_input_fee_sats"],
        userLeaves: obj["request_leaves_swap_input_user_leaves"].map((e) => UserLeafInputFromJson(e)),
        idempotencyKey: obj["request_leaves_swap_input_idempotency_key"],

        } as RequestLeavesSwapInput;

}
export const RequestLeavesSwapInputToJson = (obj: RequestLeavesSwapInput): any => {
return {
request_leaves_swap_input_adaptor_pubkey: obj.adaptorPubkey,
request_leaves_swap_input_total_amount_sats: obj.totalAmountSats,
request_leaves_swap_input_target_amount_sats: obj.targetAmountSats,
request_leaves_swap_input_fee_sats: obj.feeSats,
request_leaves_swap_input_user_leaves: obj.userLeaves.map((e) => UserLeafInputToJson(e)),
request_leaves_swap_input_idempotency_key: obj.idempotencyKey,

        }

}





export default RequestLeavesSwapInput;
