// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

interface UserLeafInput {
  leaf_id: string;

  raw_unsigned_refund_transaction: string;

  direct_raw_unsigned_refund_transaction: string;

  direct_from_cpfp_raw_unsigned_refund_transaction: string;

  adaptor_added_signature: string;

  direct_adaptor_added_signature: string;

  direct_from_cpfp_adaptor_added_signature: string;
}

export const UserLeafInputFromJson = (obj: any): UserLeafInput => {
  return {
    leaf_id: obj["user_leaf_input_leaf_id"],
    raw_unsigned_refund_transaction:
      obj["user_leaf_input_raw_unsigned_refund_transaction"],
    direct_raw_unsigned_refund_transaction:
      obj["user_leaf_input_direct_raw_unsigned_refund_transaction"],
    direct_from_cpfp_raw_unsigned_refund_transaction:
      obj["user_leaf_input_direct_from_cpfp_unsigned_refund_transaction"],
    adaptor_added_signature: obj["user_leaf_input_adaptor_added_signature"],
    direct_adaptor_added_signature:
      obj["user_leaf_input_direct_adaptor_added_signature"],
    direct_from_cpfp_adaptor_added_signature:
      obj["user_leaf_input_direct_from_cpfp_adaptor_added_signature"],
  };
};
export const UserLeafInputToJson = (obj: UserLeafInput): any => {
  return {
    user_leaf_input_leaf_id: obj.leaf_id,
    user_leaf_input_raw_unsigned_refund_transaction:
      obj.raw_unsigned_refund_transaction,
    user_leaf_input_direct_raw_unsigned_refund_transaction:
      obj.direct_raw_unsigned_refund_transaction,
    user_leaf_input_direct_from_cpfp_raw_unsigned_refund_transaction:
      obj.direct_from_cpfp_raw_unsigned_refund_transaction,
    user_leaf_input_adaptor_added_signature: obj.adaptor_added_signature,
    user_leaf_input_direct_adaptor_added_signature:
      obj.direct_adaptor_added_signature,
    user_leaf_input_direct_from_cpfp_adaptor_added_signature:
      obj.direct_from_cpfp_adaptor_added_signature,
  };
};

export default UserLeafInput;
