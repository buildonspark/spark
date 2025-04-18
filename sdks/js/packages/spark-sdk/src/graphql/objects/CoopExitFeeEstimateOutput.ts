
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import {CurrencyAmountToJson} from './CurrencyAmount.js';
import {CurrencyAmountFromJson} from './CurrencyAmount.js';
import CurrencyAmount from './CurrencyAmount.js';


interface CoopExitFeeEstimateOutput {


    userFee: CurrencyAmount;

    l1BroadcastFee: CurrencyAmount;




}

export const CoopExitFeeEstimateOutputFromJson = (obj: any): CoopExitFeeEstimateOutput => {
    return {
        userFee: CurrencyAmountFromJson(obj["coop_exit_fee_estimate_output_user_fee"]),
        l1BroadcastFee: CurrencyAmountFromJson(obj["coop_exit_fee_estimate_output_l1_broadcast_fee"]),

        } as CoopExitFeeEstimateOutput;

}
export const CoopExitFeeEstimateOutputToJson = (obj: CoopExitFeeEstimateOutput): any => {
return {
coop_exit_fee_estimate_output_user_fee: CurrencyAmountToJson(obj.userFee),
coop_exit_fee_estimate_output_l1_broadcast_fee: CurrencyAmountToJson(obj.l1BroadcastFee),

        }

}


    export const FRAGMENT = `
fragment CoopExitFeeEstimateOutputFragment on CoopExitFeeEstimateOutput {
    __typename
    coop_exit_fee_estimate_output_user_fee: user_fee {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
        currency_amount_preferred_currency_unit: preferred_currency_unit
        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
    }
    coop_exit_fee_estimate_output_l1_broadcast_fee: l1_broadcast_fee {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
        currency_amount_preferred_currency_unit: preferred_currency_unit
        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
    }
}`;




export default CoopExitFeeEstimateOutput;
