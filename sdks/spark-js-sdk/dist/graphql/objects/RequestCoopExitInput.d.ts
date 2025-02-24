interface RequestCoopExitInput {
    leafExternalIds: string[];
    withdrawalAddress: string;
}
export declare const RequestCoopExitInputFromJson: (obj: any) => RequestCoopExitInput;
export declare const RequestCoopExitInputToJson: (obj: RequestCoopExitInput) => any;
export default RequestCoopExitInput;
