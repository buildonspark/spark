
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import {CurrencyAmountToJson} from './CurrencyAmount.js';
import CurrencyAmount from './CurrencyAmount.js';
import {CurrencyAmountFromJson} from './CurrencyAmount.js';


interface Leaf {


    /** The amount of the leaf. **/
amount: CurrencyAmount;

    /** The id of the leaf known at signing operators. **/
sparkNodeId: string;




}

export const LeafFromJson = (obj: any): Leaf => {
    return {
        amount: CurrencyAmountFromJson(obj["leaf_amount"]),
        sparkNodeId: obj["leaf_spark_node_id"],

        } as Leaf;

}
export const LeafToJson = (obj: Leaf): any => {
return {
leaf_amount: CurrencyAmountToJson(obj.amount),
leaf_spark_node_id: obj.sparkNodeId,

        }

}


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




export default Leaf;
