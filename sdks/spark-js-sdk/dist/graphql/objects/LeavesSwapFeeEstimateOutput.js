// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
import { CurrencyAmountToJson } from './CurrencyAmount.js';
import { CurrencyAmountFromJson } from './CurrencyAmount.js';
export const LeavesSwapFeeEstimateOutputFromJson = (obj) => {
    return {
        feeEstimate: CurrencyAmountFromJson(obj["leaves_swap_fee_estimate_output_fee_estimate"]),
    };
};
export const LeavesSwapFeeEstimateOutputToJson = (obj) => {
    return {
        leaves_swap_fee_estimate_output_fee_estimate: CurrencyAmountToJson(obj.feeEstimate),
    };
};
export const FRAGMENT = `
fragment LeavesSwapFeeEstimateOutputFragment on LeavesSwapFeeEstimateOutput {
    __typename
    leaves_swap_fee_estimate_output_fee_estimate: fee_estimate {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
        currency_amount_preferred_currency_unit: preferred_currency_unit
        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
    }
}`;
//# sourceMappingURL=LeavesSwapFeeEstimateOutput.js.map