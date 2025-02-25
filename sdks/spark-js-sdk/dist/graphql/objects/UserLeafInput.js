// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const UserLeafInputFromJson = (obj) => {
    return {
        leafId: obj["user_leaf_input_leaf_id"],
        rawUnsignedRefundTransaction: obj["user_leaf_input_raw_unsigned_refund_transaction"],
        adaptorAddedSignature: obj["user_leaf_input_adaptor_added_signature"],
    };
};
export const UserLeafInputToJson = (obj) => {
    return {
        user_leaf_input_leaf_id: obj.leafId,
        user_leaf_input_raw_unsigned_refund_transaction: obj.rawUnsignedRefundTransaction,
        user_leaf_input_adaptor_added_signature: obj.adaptorAddedSignature,
    };
};
//# sourceMappingURL=UserLeafInput.js.map