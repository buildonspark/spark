
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import {CurrencyAmountToJson} from './CurrencyAmount.js';
import CurrencyAmount from './CurrencyAmount.js';
import {CurrencyAmountFromJson} from './CurrencyAmount.js';


interface LightningReceiveFeeEstimateOutput {


    feeEstimate: CurrencyAmount;




}

export const LightningReceiveFeeEstimateOutputFromJson = (obj: any): LightningReceiveFeeEstimateOutput => {
    return {
        feeEstimate: CurrencyAmountFromJson(obj["lightning_receive_fee_estimate_output_fee_estimate"]),

        } as LightningReceiveFeeEstimateOutput;

}
export const LightningReceiveFeeEstimateOutputToJson = (obj: LightningReceiveFeeEstimateOutput): any => {
return {
lightning_receive_fee_estimate_output_fee_estimate: CurrencyAmountToJson(obj.feeEstimate),

        }

}


    export const FRAGMENT = `
fragment LightningReceiveFeeEstimateOutputFragment on LightningReceiveFeeEstimateOutput {
    __typename
    lightning_receive_fee_estimate_output_fee_estimate: fee_estimate {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
        currency_amount_preferred_currency_unit: preferred_currency_unit
        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
    }
}`;




export default LightningReceiveFeeEstimateOutput;
