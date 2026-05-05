// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import type CurrencyAmount from "./CurrencyAmount.js";
import {
  CurrencyAmountFromJson,
  CurrencyAmountToJson,
} from "./CurrencyAmount.js";

interface CoopExitFeeEstimate {
  userFee: CurrencyAmount;

  l1BroadcastFee: CurrencyAmount;
}

export const CoopExitFeeEstimateFromJson = (obj: any): CoopExitFeeEstimate => {
  return {
    userFee: CurrencyAmountFromJson(obj["coop_exit_fee_estimate_user_fee"]),
    l1BroadcastFee: CurrencyAmountFromJson(
      obj["coop_exit_fee_estimate_l1_broadcast_fee"],
    ),
  };
};
export const CoopExitFeeEstimateToJson = (obj: CoopExitFeeEstimate): any => {
  return {
    coop_exit_fee_estimate_user_fee: CurrencyAmountToJson(obj.userFee),
    coop_exit_fee_estimate_l1_broadcast_fee: CurrencyAmountToJson(
      obj.l1BroadcastFee,
    ),
  };
};

export const FRAGMENT = `
fragment CoopExitFeeEstimateFragment on CoopExitFeeEstimate {
    __typename
    coop_exit_fee_estimate_user_fee: user_fee {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
    }
    coop_exit_fee_estimate_l1_broadcast_fee: l1_broadcast_fee {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
    }
}`;

export default CoopExitFeeEstimate;
