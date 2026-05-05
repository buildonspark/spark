// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

interface SwapLeaf {
  leafId: string;

  rawUnsignedRefundTransaction: string;

  adaptorSignedSignature: string;

  directRawUnsignedRefundTransaction?: string;

  directAdaptorSignedSignature?: string;

  directFromCpfpRawUnsignedRefundTransaction?: string;

  directFromCpfpAdaptorSignedSignature?: string;
}

export const SwapLeafFromJson = (obj: any): SwapLeaf => {
  return {
    leafId: obj["swap_leaf_leaf_id"],
    rawUnsignedRefundTransaction:
      obj["swap_leaf_raw_unsigned_refund_transaction"],
    adaptorSignedSignature: obj["swap_leaf_adaptor_signed_signature"],
    directRawUnsignedRefundTransaction:
      obj["swap_leaf_direct_raw_unsigned_refund_transaction"],
    directAdaptorSignedSignature:
      obj["swap_leaf_direct_adaptor_signed_signature"],
    directFromCpfpRawUnsignedRefundTransaction:
      obj["swap_leaf_direct_from_cpfp_raw_unsigned_refund_transaction"],
    directFromCpfpAdaptorSignedSignature:
      obj["swap_leaf_direct_from_cpfp_adaptor_signed_signature"], 
  };
};
export const SwapLeafToJson = (obj: SwapLeaf): any => {
  return {
    swap_leaf_leaf_id: obj.leafId,
    swap_leaf_raw_unsigned_refund_transaction: obj.rawUnsignedRefundTransaction,
    swap_leaf_adaptor_signed_signature: obj.adaptorSignedSignature,
    swap_leaf_direct_raw_unsigned_refund_transaction:
      obj.directRawUnsignedRefundTransaction,
    swap_leaf_direct_adaptor_signed_signature: obj.directAdaptorSignedSignature,
    swap_leaf_direct_from_cpfp_raw_unsigned_refund_transaction:
    obj.directFromCpfpRawUnsignedRefundTransaction,
  swap_leaf_direct_from_cpfp_adaptor_signed_signature: obj.directFromCpfpAdaptorSignedSignature,
  };
};

export const FRAGMENT = `
fragment SwapLeafFragment on SwapLeaf {
    __typename
    swap_leaf_leaf_id: leaf_id
    swap_leaf_raw_unsigned_refund_transaction: raw_unsigned_refund_transaction
    swap_leaf_adaptor_signed_signature: adaptor_signed_signature
    swap_leaf_direct_raw_unsigned_refund_transaction: direct_raw_unsigned_refund_transaction
    swap_leaf_direct_adaptor_signed_signature: direct_adaptor_signed_signature
    swap_leaf_direct_from_cpfp_raw_unsigned_refund_transaction: direct_from_cpfp_raw_unsigned_refund_transaction
    swap_leaf_direct_from_cpfp_adaptor_signed_signature: direct_from_cpfp_adaptor_signed_signature
}`;

export default SwapLeaf;
