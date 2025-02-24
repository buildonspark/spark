import CurrencyUnit from './CurrencyUnit.js';
/** This object represents the value and unit for an amount of currency. **/
interface CurrencyAmount {
    /** The original numeric value for this CurrencyAmount. **/
    originalValue: number;
    /** The original unit of currency for this CurrencyAmount. **/
    originalUnit: CurrencyUnit;
    /** The unit of user's preferred currency. **/
    preferredCurrencyUnit: CurrencyUnit;
    /**
 * The rounded numeric value for this CurrencyAmount in the very base level of user's preferred
 * currency. For example, for USD, the value will be in cents.
**/
    preferredCurrencyValueRounded: number;
    /**
 * The approximate float value for this CurrencyAmount in the very base level of user's preferred
 * currency. For example, for USD, the value will be in cents.
**/
    preferredCurrencyValueApprox: number;
}
export declare const CurrencyAmountFromJson: (obj: any) => CurrencyAmount;
export declare const CurrencyAmountToJson: (obj: CurrencyAmount) => any;
export declare const FRAGMENT = "\nfragment CurrencyAmountFragment on CurrencyAmount {\n    __typename\n    currency_amount_original_value: original_value\n    currency_amount_original_unit: original_unit\n    currency_amount_preferred_currency_unit: preferred_currency_unit\n    currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n    currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n}";
export default CurrencyAmount;
