import CurrencyAmount from './CurrencyAmount.js';
interface Leaf {
    /** The amount of the leaf. **/
    amount: CurrencyAmount;
    /** The id of the leaf known at signing operators. **/
    sparkNodeId: string;
}
export declare const LeafFromJson: (obj: any) => Leaf;
export declare const LeafToJson: (obj: Leaf) => any;
export declare const FRAGMENT = "\nfragment LeafFragment on Leaf {\n    __typename\n    leaf_amount: amount {\n        __typename\n        currency_amount_original_value: original_value\n        currency_amount_original_unit: original_unit\n        currency_amount_preferred_currency_unit: preferred_currency_unit\n        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n    }\n    leaf_spark_node_id: spark_node_id\n}";
export default Leaf;
