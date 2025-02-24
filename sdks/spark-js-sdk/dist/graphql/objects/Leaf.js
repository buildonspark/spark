// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
import { CurrencyAmountFromJson } from './CurrencyAmount.js';
import { CurrencyAmountToJson } from './CurrencyAmount.js';
export const LeafFromJson = (obj) => {
    return {
        amount: CurrencyAmountFromJson(obj["leaf_amount"]),
        sparkNodeId: obj["leaf_spark_node_id"],
    };
};
export const LeafToJson = (obj) => {
    return {
        leaf_amount: CurrencyAmountToJson(obj.amount),
        leaf_spark_node_id: obj.sparkNodeId,
    };
};
export const FRAGMENT = `
fragment LeafFragment on Leaf {
    __typename
    leaf_amount: amount {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
        currency_amount_preferred_currency_unit: preferred_currency_unit
        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
    }
    leaf_spark_node_id: spark_node_id
}`;
//# sourceMappingURL=Leaf.js.map