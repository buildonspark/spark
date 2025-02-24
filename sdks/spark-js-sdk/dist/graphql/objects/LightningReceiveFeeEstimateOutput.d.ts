import CurrencyAmount from './CurrencyAmount.js';
interface LightningReceiveFeeEstimateOutput {
    feeEstimate: CurrencyAmount;
}
export declare const LightningReceiveFeeEstimateOutputFromJson: (obj: any) => LightningReceiveFeeEstimateOutput;
export declare const LightningReceiveFeeEstimateOutputToJson: (obj: LightningReceiveFeeEstimateOutput) => any;
export declare const FRAGMENT = "\nfragment LightningReceiveFeeEstimateOutputFragment on LightningReceiveFeeEstimateOutput {\n    __typename\n    lightning_receive_fee_estimate_output_fee_estimate: fee_estimate {\n        __typename\n        currency_amount_original_value: original_value\n        currency_amount_original_unit: original_unit\n        currency_amount_preferred_currency_unit: preferred_currency_unit\n        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n    }\n}";
export default LightningReceiveFeeEstimateOutput;
