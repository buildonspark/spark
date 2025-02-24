// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
import { CurrencyAmountFromJson } from './CurrencyAmount.js';
import { CurrencyAmountToJson } from './CurrencyAmount.js';
export const CoopExitFeeEstimateOutputFromJson = (obj) => {
    return {
        feeEstimate: CurrencyAmountFromJson(obj["coop_exit_fee_estimate_output_fee_estimate"]),
    };
};
export const CoopExitFeeEstimateOutputToJson = (obj) => {
    return {
        coop_exit_fee_estimate_output_fee_estimate: CurrencyAmountToJson(obj.feeEstimate),
    };
};
export const FRAGMENT = `
fragment CoopExitFeeEstimateOutputFragment on CoopExitFeeEstimateOutput {
    __typename
    coop_exit_fee_estimate_output_fee_estimate: fee_estimate {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
        currency_amount_preferred_currency_unit: preferred_currency_unit
        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
    }
}`;
//# sourceMappingURL=CoopExitFeeEstimateOutput.js.map