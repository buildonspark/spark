// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import type CurrencyAmount from "./CurrencyAmount.js";
import {
  CurrencyAmountFromJson,
  CurrencyAmountToJson,
} from "./CurrencyAmount.js";

interface LightningSendFeeEstimateOutput {
  feeEstimate: CurrencyAmount;
}

export const LightningSendFeeEstimateOutputFromJson = (
  obj: any,
): LightningSendFeeEstimateOutput => {
  return {
    feeEstimate: CurrencyAmountFromJson(
      obj["lightning_send_fee_estimate_output_fee_estimate"],
    ),
  };
};
export const LightningSendFeeEstimateOutputToJson = (
  obj: LightningSendFeeEstimateOutput,
): any => {
  return {
    lightning_send_fee_estimate_output_fee_estimate: CurrencyAmountToJson(
      obj.feeEstimate,
    ),
  };
};

export const FRAGMENT = `
fragment LightningSendFeeEstimateOutputFragment on LightningSendFeeEstimateOutput {
    __typename
    lightning_send_fee_estimate_output_fee_estimate: fee_estimate {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
    }
}`;

export default LightningSendFeeEstimateOutput;
