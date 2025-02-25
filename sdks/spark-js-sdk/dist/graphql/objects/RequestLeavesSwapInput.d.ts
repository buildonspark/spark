import UserLeafInput from './UserLeafInput.js';
interface RequestLeavesSwapInput {
    adaptorPubkey: string;
    totalAmountSats: number;
    targetAmountSats: number;
    feeSats: number;
    userLeaves: UserLeafInput[];
}
export declare const RequestLeavesSwapInputFromJson: (obj: any) => RequestLeavesSwapInput;
export declare const RequestLeavesSwapInputToJson: (obj: RequestLeavesSwapInput) => any;
export default RequestLeavesSwapInput;
