
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface LightningSendFeeEstimateInput {


    encodedInvoice: string;




}

export const LightningSendFeeEstimateInputFromJson = (obj: any): LightningSendFeeEstimateInput => {
    return {
        encodedInvoice: obj["lightning_send_fee_estimate_input_encoded_invoice"],

        } as LightningSendFeeEstimateInput;

}
export const LightningSendFeeEstimateInputToJson = (obj: LightningSendFeeEstimateInput): any => {
return {
lightning_send_fee_estimate_input_encoded_invoice: obj.encodedInvoice,

        }

}





export default LightningSendFeeEstimateInput;
