interface SwapLeaf {
    leafId: string;
    rawUnsignedRefundTransaction: string;
    adaptorSignedSignature: string;
}
export declare const SwapLeafFromJson: (obj: any) => SwapLeaf;
export declare const SwapLeafToJson: (obj: SwapLeaf) => any;
export declare const FRAGMENT = "\nfragment SwapLeafFragment on SwapLeaf {\n    __typename\n    swap_leaf_leaf_id: leaf_id\n    swap_leaf_raw_unsigned_refund_transaction: raw_unsigned_refund_transaction\n    swap_leaf_adaptor_signed_signature: adaptor_signed_signature\n}";
export default SwapLeaf;
