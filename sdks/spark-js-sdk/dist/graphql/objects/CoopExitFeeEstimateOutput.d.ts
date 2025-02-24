import CurrencyAmount from './CurrencyAmount.js';
interface CoopExitFeeEstimateOutput {
    feeEstimate: CurrencyAmount;
}
export declare const CoopExitFeeEstimateOutputFromJson: (obj: any) => CoopExitFeeEstimateOutput;
export declare const CoopExitFeeEstimateOutputToJson: (obj: CoopExitFeeEstimateOutput) => any;
export declare const FRAGMENT = "\nfragment CoopExitFeeEstimateOutputFragment on CoopExitFeeEstimateOutput {\n    __typename\n    coop_exit_fee_estimate_output_fee_estimate: fee_estimate {\n        __typename\n        currency_amount_original_value: original_value\n        currency_amount_original_unit: original_unit\n        currency_amount_preferred_currency_unit: preferred_currency_unit\n        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n    }\n}";
export default CoopExitFeeEstimateOutput;
