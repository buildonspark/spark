// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const UserLeafInputFromJson = (obj) => {
    return {
        leaf_id: obj["user_leaf_input_leaf_id"],
        raw_unsigned_refund_transaction: obj["user_leaf_input_raw_unsigned_refund_transaction"],
        adaptor_added_signature: obj["user_leaf_input_adaptor_added_signature"],
    };
};
export const UserLeafInputToJson = (obj) => {
    return {
        user_leaf_input_leaf_id: obj.leaf_id,
        user_leaf_input_raw_unsigned_refund_transaction: obj.raw_unsigned_refund_transaction,
        user_leaf_input_adaptor_added_signature: obj.adaptor_added_signature,
    };
};
//# sourceMappingURL=UserLeafInput.js.map