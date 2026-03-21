// Copyright ©, 2025-present, Lightspark Group, Inc. - All Rights Reserved

import CurrencyAmount, { CurrencyAmountFromJson } from "./CurrencyAmount.js";

interface InstantStaticDepositPlan {
  id: string;
  amount: CurrencyAmount;
  confirmations: number;
  status: string;
  transferSparkId: string | null;
}

export const InstantStaticDepositPlanFromJson = (
  obj: any,
): InstantStaticDepositPlan => {
  return {
    id: obj["static_deposit_plan_id"],
    amount: CurrencyAmountFromJson(obj["static_deposit_plan_amount"]),
    confirmations: obj["static_deposit_plan_confirmations"],
    status: obj["static_deposit_plan_status"],
    transferSparkId: obj["static_deposit_plan_transfer_spark_id"] ?? null,
  } as InstantStaticDepositPlan;
};

export const FRAGMENT = `
fragment InstantStaticDepositPlanFragment on StaticDepositPlan {
    __typename
    static_deposit_plan_id: id
    static_deposit_plan_amount: amount {
        ...CurrencyAmountFragment
    }
    static_deposit_plan_confirmations: confirmations
    static_deposit_plan_status: status
    static_deposit_plan_transfer_spark_id: transfer_spark_id
}`;

export default InstantStaticDepositPlan;
