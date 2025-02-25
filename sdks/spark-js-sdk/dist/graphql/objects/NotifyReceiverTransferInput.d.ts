interface NotifyReceiverTransferInput {
    phoneNumber: string;
    amountSats: number;
}
export declare const NotifyReceiverTransferInputFromJson: (obj: any) => NotifyReceiverTransferInput;
export declare const NotifyReceiverTransferInputToJson: (obj: NotifyReceiverTransferInput) => any;
export default NotifyReceiverTransferInput;
