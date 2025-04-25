
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import {CoopExitFeeEstimateToJson} from './CoopExitFeeEstimate.js';
import {CoopExitFeeEstimateFromJson} from './CoopExitFeeEstimate.js';
import CoopExitFeeEstimate from './CoopExitFeeEstimate.js';


interface CoopExitFeeEstimatesOutput {


    speedFast?: CoopExitFeeEstimate | undefined;

    speedMedium?: CoopExitFeeEstimate | undefined;

    speedSlow?: CoopExitFeeEstimate | undefined;




}

export const CoopExitFeeEstimatesOutputFromJson = (obj: any): CoopExitFeeEstimatesOutput => {
    return {
        speedFast: (!!obj["coop_exit_fee_estimates_output_speed_fast"] ? CoopExitFeeEstimateFromJson(obj["coop_exit_fee_estimates_output_speed_fast"]) : undefined),
        speedMedium: (!!obj["coop_exit_fee_estimates_output_speed_medium"] ? CoopExitFeeEstimateFromJson(obj["coop_exit_fee_estimates_output_speed_medium"]) : undefined),
        speedSlow: (!!obj["coop_exit_fee_estimates_output_speed_slow"] ? CoopExitFeeEstimateFromJson(obj["coop_exit_fee_estimates_output_speed_slow"]) : undefined),

        } as CoopExitFeeEstimatesOutput;

}
export const CoopExitFeeEstimatesOutputToJson = (obj: CoopExitFeeEstimatesOutput): any => {
return {
coop_exit_fee_estimates_output_speed_fast: (obj.speedFast ? CoopExitFeeEstimateToJson(obj.speedFast) : undefined),
coop_exit_fee_estimates_output_speed_medium: (obj.speedMedium ? CoopExitFeeEstimateToJson(obj.speedMedium) : undefined),
coop_exit_fee_estimates_output_speed_slow: (obj.speedSlow ? CoopExitFeeEstimateToJson(obj.speedSlow) : undefined),

        }

}


    export const FRAGMENT = `
fragment CoopExitFeeEstimatesOutputFragment on CoopExitFeeEstimatesOutput {
    __typename
    coop_exit_fee_estimates_output_speed_fast: speed_fast {
        __typename
        coop_exit_fee_estimate_user_fee: user_fee {
            __typename
            currency_amount_original_value: original_value
            currency_amount_original_unit: original_unit
            currency_amount_preferred_currency_unit: preferred_currency_unit
            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
        }
        coop_exit_fee_estimate_l1_broadcast_fee: l1_broadcast_fee {
            __typename
            currency_amount_original_value: original_value
            currency_amount_original_unit: original_unit
            currency_amount_preferred_currency_unit: preferred_currency_unit
            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
        }
    }
    coop_exit_fee_estimates_output_speed_medium: speed_medium {
        __typename
        coop_exit_fee_estimate_user_fee: user_fee {
            __typename
            currency_amount_original_value: original_value
            currency_amount_original_unit: original_unit
            currency_amount_preferred_currency_unit: preferred_currency_unit
            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
        }
        coop_exit_fee_estimate_l1_broadcast_fee: l1_broadcast_fee {
            __typename
            currency_amount_original_value: original_value
            currency_amount_original_unit: original_unit
            currency_amount_preferred_currency_unit: preferred_currency_unit
            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
        }
    }
    coop_exit_fee_estimates_output_speed_slow: speed_slow {
        __typename
        coop_exit_fee_estimate_user_fee: user_fee {
            __typename
            currency_amount_original_value: original_value
            currency_amount_original_unit: original_unit
            currency_amount_preferred_currency_unit: preferred_currency_unit
            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
        }
        coop_exit_fee_estimate_l1_broadcast_fee: l1_broadcast_fee {
            __typename
            currency_amount_original_value: original_value
            currency_amount_original_unit: original_unit
            currency_amount_preferred_currency_unit: preferred_currency_unit
            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
        }
    }
}`;




export default CoopExitFeeEstimatesOutput;
