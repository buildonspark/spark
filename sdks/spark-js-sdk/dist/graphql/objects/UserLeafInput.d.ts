interface UserLeafInput {
    leaf_id: string;
    raw_unsigned_refund_transaction: string;
    adaptor_added_signature: string;
}
export declare const UserLeafInputFromJson: (obj: any) => UserLeafInput;
export declare const UserLeafInputToJson: (obj: UserLeafInput) => any;
export default UserLeafInput;
