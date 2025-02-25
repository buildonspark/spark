interface UserLeafInput {
    leafId: string;
    rawUnsignedRefundTransaction: string;
    adaptorAddedSignature: string;
}
export declare const UserLeafInputFromJson: (obj: any) => UserLeafInput;
export declare const UserLeafInputToJson: (obj: UserLeafInput) => any;
export default UserLeafInput;
