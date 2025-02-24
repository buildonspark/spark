// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const LightningSendFeeEstimateInputFromJson = (obj) => {
    return {
        encodedInvoice: obj["lightning_send_fee_estimate_input_encoded_invoice"],
    };
};
export const LightningSendFeeEstimateInputToJson = (obj) => {
    return {
        lightning_send_fee_estimate_input_encoded_invoice: obj.encodedInvoice,
    };
};
//# sourceMappingURL=LightningSendFeeEstimateInput.js.map