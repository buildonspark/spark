// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const NotifyReceiverTransferInputFromJson = (obj) => {
    return {
        phoneNumber: obj["notify_receiver_transfer_input_phone_number"],
        amountSats: obj["notify_receiver_transfer_input_amount_sats"],
    };
};
export const NotifyReceiverTransferInputToJson = (obj) => {
    return {
        notify_receiver_transfer_input_phone_number: obj.phoneNumber,
        notify_receiver_transfer_input_amount_sats: obj.amountSats,
    };
};
//# sourceMappingURL=NotifyReceiverTransferInput.js.map