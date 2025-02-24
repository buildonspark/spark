interface CompleteLeavesSwapInput {
    adaptorSecretKey: string;
    userOutboundTransferExternalId: string;
    leavesSwapRequestId: string;
}
export declare const CompleteLeavesSwapInputFromJson: (obj: any) => CompleteLeavesSwapInput;
export declare const CompleteLeavesSwapInputToJson: (obj: CompleteLeavesSwapInput) => any;
export default CompleteLeavesSwapInput;
