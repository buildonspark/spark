
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface UserLeafInput {


    leaf_id: string;

    raw_unsigned_refund_transaction: string;

    adaptor_added_signature: string;




}

export const UserLeafInputFromJson = (obj: any): UserLeafInput => {
    return {
        leaf_id: obj["user_leaf_input_leaf_id"],
        raw_unsigned_refund_transaction: obj["user_leaf_input_raw_unsigned_refund_transaction"],
        adaptor_added_signature: obj["user_leaf_input_adaptor_added_signature"],

        } as UserLeafInput;

}
export const UserLeafInputToJson = (obj: UserLeafInput): any => {
return {
user_leaf_input_leaf_id: obj.leaf_id,
user_leaf_input_raw_unsigned_refund_transaction: obj.raw_unsigned_refund_transaction,
user_leaf_input_adaptor_added_signature: obj.adaptor_added_signature,

        }

}





export default UserLeafInput;
