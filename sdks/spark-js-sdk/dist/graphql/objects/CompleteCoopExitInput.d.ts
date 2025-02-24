interface CompleteCoopExitInput {
    userOutboundTransferExternalId: string;
    coopExitRequestId: string;
}
export declare const CompleteCoopExitInputFromJson: (obj: any) => CompleteCoopExitInput;
export declare const CompleteCoopExitInputToJson: (obj: CompleteCoopExitInput) => any;
export default CompleteCoopExitInput;
