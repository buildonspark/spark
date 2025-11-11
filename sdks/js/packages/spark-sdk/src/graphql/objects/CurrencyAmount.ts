// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import CurrencyUnit from "./CurrencyUnit.js";

/** This object represents the value and unit for an amount of currency. **/
interface CurrencyAmount {
  /** The original numeric value for this CurrencyAmount. **/
  originalValue: number;

  /** The original unit of currency for this CurrencyAmount. **/
  originalUnit: CurrencyUnit;
}

export const CurrencyAmountFromJson = (obj: any): CurrencyAmount => {
  return {
    originalValue: obj["currency_amount_original_value"],
    originalUnit:
      CurrencyUnit[obj["currency_amount_original_unit"]] ??
      CurrencyUnit.FUTURE_VALUE,
  } as CurrencyAmount;
};
export const CurrencyAmountToJson = (obj: CurrencyAmount): any => {
  return {
    currency_amount_original_value: obj.originalValue,
    currency_amount_original_unit: obj.originalUnit,
  };
};

export const FRAGMENT = `
fragment CurrencyAmountFragment on CurrencyAmount {
    __typename
    currency_amount_original_value: original_value
    currency_amount_original_unit: original_unit
}`;

export default CurrencyAmount;
