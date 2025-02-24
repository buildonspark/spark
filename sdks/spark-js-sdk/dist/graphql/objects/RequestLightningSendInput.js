// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const RequestLightningSendInputFromJson = (obj) => {
    return {
        encodedInvoice: obj["request_lightning_send_input_encoded_invoice"],
        idempotencyKey: obj["request_lightning_send_input_idempotency_key"],
    };
};
export const RequestLightningSendInputToJson = (obj) => {
    return {
        request_lightning_send_input_encoded_invoice: obj.encodedInvoice,
        request_lightning_send_input_idempotency_key: obj.idempotencyKey,
    };
};
//# sourceMappingURL=RequestLightningSendInput.js.map