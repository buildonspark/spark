// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
import { UserLeafInputFromJson } from './UserLeafInput.js';
import { UserLeafInputToJson } from './UserLeafInput.js';
export const RequestLeavesSwapInputFromJson = (obj) => {
    return {
        adaptorPubkey: obj["request_leaves_swap_input_adaptor_pubkey"],
        totalAmountSats: obj["request_leaves_swap_input_total_amount_sats"],
        targetAmountSats: obj["request_leaves_swap_input_target_amount_sats"],
        feeSats: obj["request_leaves_swap_input_fee_sats"],
        userLeaves: obj["request_leaves_swap_input_user_leaves"].map((e) => UserLeafInputFromJson(e)),
    };
};
export const RequestLeavesSwapInputToJson = (obj) => {
    return {
        request_leaves_swap_input_adaptor_pubkey: obj.adaptorPubkey,
        request_leaves_swap_input_total_amount_sats: obj.totalAmountSats,
        request_leaves_swap_input_target_amount_sats: obj.targetAmountSats,
        request_leaves_swap_input_fee_sats: obj.feeSats,
        request_leaves_swap_input_user_leaves: obj.userLeaves.map((e) => UserLeafInputToJson(e)),
    };
};
//# sourceMappingURL=RequestLeavesSwapInput.js.map