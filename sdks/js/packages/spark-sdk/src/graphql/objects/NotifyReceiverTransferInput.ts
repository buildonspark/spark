
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface NotifyReceiverTransferInput {


    phoneNumber: string;

    amountSats: number;




}

export const NotifyReceiverTransferInputFromJson = (obj: any): NotifyReceiverTransferInput => {
    return {
        phoneNumber: obj["notify_receiver_transfer_input_phone_number"],
        amountSats: obj["notify_receiver_transfer_input_amount_sats"],

        } as NotifyReceiverTransferInput;

}
export const NotifyReceiverTransferInputToJson = (obj: NotifyReceiverTransferInput): any => {
return {
notify_receiver_transfer_input_phone_number: obj.phoneNumber,
notify_receiver_transfer_input_amount_sats: obj.amountSats,

        }

}





export default NotifyReceiverTransferInput;
