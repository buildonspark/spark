import PageInfo from './PageInfo.js';
import Leaf from './Leaf.js';
interface SparkTransferToLeavesConnection {
    /**
 * The total count of objects in this connection, using the current filters. It is different from the
 * number of objects returned in the current page (in the `entities` field).
**/
    count: number;
    /** An object that holds pagination information about the objects in this connection. **/
    pageInfo: PageInfo;
    /** The leaves for the current page of this connection. **/
    entities: Leaf[];
    /** The typename of the object **/
    typename: string;
}
export declare const SparkTransferToLeavesConnectionFromJson: (obj: any) => SparkTransferToLeavesConnection;
export declare const SparkTransferToLeavesConnectionToJson: (obj: SparkTransferToLeavesConnection) => any;
export declare const FRAGMENT = "\nfragment SparkTransferToLeavesConnectionFragment on SparkTransferToLeavesConnection {\n    __typename\n    spark_transfer_to_leaves_connection_count: count\n    spark_transfer_to_leaves_connection_page_info: page_info {\n        __typename\n        page_info_has_next_page: has_next_page\n        page_info_has_previous_page: has_previous_page\n        page_info_start_cursor: start_cursor\n        page_info_end_cursor: end_cursor\n    }\n    spark_transfer_to_leaves_connection_entities: entities {\n        __typename\n        leaf_amount: amount {\n            __typename\n            currency_amount_original_value: original_value\n            currency_amount_original_unit: original_unit\n            currency_amount_preferred_currency_unit: preferred_currency_unit\n            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n        }\n        leaf_spark_node_id: spark_node_id\n    }\n}";
export default SparkTransferToLeavesConnection;
