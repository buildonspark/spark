interface CoopExitFeeEstimateInput {
    leafExternalIds: string[];
    withdrawalAddress: string;
}
export declare const CoopExitFeeEstimateInputFromJson: (obj: any) => CoopExitFeeEstimateInput;
export declare const CoopExitFeeEstimateInputToJson: (obj: CoopExitFeeEstimateInput) => any;
export default CoopExitFeeEstimateInput;
