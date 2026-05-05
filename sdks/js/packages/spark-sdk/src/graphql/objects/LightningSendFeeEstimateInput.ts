
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface LightningSendFeeEstimateInput {


    encodedInvoice: string;

    /**
     * The amount you will pay for this invoice in sats. It should ONLY be set when the invoice
     * amount is zero.
**/
amountSats?: number | undefined;




}

export const LightningSendFeeEstimateInputFromJson = (obj: any): LightningSendFeeEstimateInput => {
    return {
        encodedInvoice: obj["lightning_send_fee_estimate_input_encoded_invoice"],
        amountSats: obj["lightning_send_fee_estimate_input_amount_sats"],

        };

}
export const LightningSendFeeEstimateInputToJson = (obj: LightningSendFeeEstimateInput): any => {
return {
lightning_send_fee_estimate_input_encoded_invoice: obj.encodedInvoice,
lightning_send_fee_estimate_input_amount_sats: obj.amountSats,

        }

}





export default LightningSendFeeEstimateInput;
