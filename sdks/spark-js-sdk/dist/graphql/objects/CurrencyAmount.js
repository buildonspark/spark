// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
import CurrencyUnit from './CurrencyUnit.js';
export const CurrencyAmountFromJson = (obj) => {
    return {
        originalValue: obj["currency_amount_original_value"],
        originalUnit: CurrencyUnit[obj["currency_amount_original_unit"]] ?? CurrencyUnit.FUTURE_VALUE,
        preferredCurrencyUnit: CurrencyUnit[obj["currency_amount_preferred_currency_unit"]] ?? CurrencyUnit.FUTURE_VALUE,
        preferredCurrencyValueRounded: obj["currency_amount_preferred_currency_value_rounded"],
        preferredCurrencyValueApprox: obj["currency_amount_preferred_currency_value_approx"],
    };
};
export const CurrencyAmountToJson = (obj) => {
    return {
        currency_amount_original_value: obj.originalValue,
        currency_amount_original_unit: obj.originalUnit,
        currency_amount_preferred_currency_unit: obj.preferredCurrencyUnit,
        currency_amount_preferred_currency_value_rounded: obj.preferredCurrencyValueRounded,
        currency_amount_preferred_currency_value_approx: obj.preferredCurrencyValueApprox,
    };
};
export const FRAGMENT = `
fragment CurrencyAmountFragment on CurrencyAmount {
    __typename
    currency_amount_original_value: original_value
    currency_amount_original_unit: original_unit
    currency_amount_preferred_currency_unit: preferred_currency_unit
    currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
    currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
}`;
//# sourceMappingURL=CurrencyAmount.js.map