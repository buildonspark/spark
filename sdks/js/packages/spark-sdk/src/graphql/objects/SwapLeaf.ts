
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface SwapLeaf {


    leafId: string;

    rawUnsignedRefundTransaction: string;

    adaptorSignedSignature: string;




}

export const SwapLeafFromJson = (obj: any): SwapLeaf => {
    return {
        leafId: obj["swap_leaf_leaf_id"],
        rawUnsignedRefundTransaction: obj["swap_leaf_raw_unsigned_refund_transaction"],
        adaptorSignedSignature: obj["swap_leaf_adaptor_signed_signature"],

        } as SwapLeaf;

}
export const SwapLeafToJson = (obj: SwapLeaf): any => {
return {
swap_leaf_leaf_id: obj.leafId,
swap_leaf_raw_unsigned_refund_transaction: obj.rawUnsignedRefundTransaction,
swap_leaf_adaptor_signed_signature: obj.adaptorSignedSignature,

        }

}


    export const FRAGMENT = `
fragment SwapLeafFragment on SwapLeaf {
    __typename
    swap_leaf_leaf_id: leaf_id
    swap_leaf_raw_unsigned_refund_transaction: raw_unsigned_refund_transaction
    swap_leaf_adaptor_signed_signature: adaptor_signed_signature
}`;




export default SwapLeaf;
