
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import {CurrencyAmountToJson} from './CurrencyAmount.js';
import CurrencyAmount from './CurrencyAmount.js';
import {CurrencyAmountFromJson} from './CurrencyAmount.js';


interface LightningSendFeeEstimateOutput {


    feeEstimate: CurrencyAmount;




}

export const LightningSendFeeEstimateOutputFromJson = (obj: any): LightningSendFeeEstimateOutput => {
    return {
        feeEstimate: CurrencyAmountFromJson(obj["lightning_send_fee_estimate_output_fee_estimate"]),

        } as LightningSendFeeEstimateOutput;

}
export const LightningSendFeeEstimateOutputToJson = (obj: LightningSendFeeEstimateOutput): any => {
return {
lightning_send_fee_estimate_output_fee_estimate: CurrencyAmountToJson(obj.feeEstimate),

        }

}


    export const FRAGMENT = `
fragment LightningSendFeeEstimateOutputFragment on LightningSendFeeEstimateOutput {
    __typename
    lightning_send_fee_estimate_output_fee_estimate: fee_estimate {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
        currency_amount_preferred_currency_unit: preferred_currency_unit
        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
    }
}`;




export default LightningSendFeeEstimateOutput;
