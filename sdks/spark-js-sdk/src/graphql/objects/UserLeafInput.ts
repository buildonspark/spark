
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface UserLeafInput {


    leafId: string;

    rawUnsignedRefundTransaction: string;

    adaptorAddedSignature: string;




}

export const UserLeafInputFromJson = (obj: any): UserLeafInput => {
    return {
        leafId: obj["user_leaf_input_leaf_id"],
        rawUnsignedRefundTransaction: obj["user_leaf_input_raw_unsigned_refund_transaction"],
        adaptorAddedSignature: obj["user_leaf_input_adaptor_added_signature"],

        } as UserLeafInput;

}
export const UserLeafInputToJson = (obj: UserLeafInput): any => {
return {
user_leaf_input_leaf_id: obj.leafId,
user_leaf_input_raw_unsigned_refund_transaction: obj.rawUnsignedRefundTransaction,
user_leaf_input_adaptor_added_signature: obj.adaptorAddedSignature,

        }

}





export default UserLeafInput;
