import PageInfo from './PageInfo.js';
interface Connection {
    /**
 * The total count of objects in this connection, using the current filters. It is different from the
 * number of objects returned in the current page (in the `entities` field).
**/
    count: number;
    /** An object that holds pagination information about the objects in this connection. **/
    pageInfo: PageInfo;
    /** The typename of the object **/
    typename: string;
}
export declare const ConnectionFromJson: (obj: any) => Connection;
export declare const ConnectionToJson: (obj: Connection) => any;
export declare const FRAGMENT = "\nfragment ConnectionFragment on Connection {\n    __typename\n    ... on SparkTransferToLeavesConnection {\n        __typename\n        spark_transfer_to_leaves_connection_count: count\n        spark_transfer_to_leaves_connection_page_info: page_info {\n            __typename\n            page_info_has_next_page: has_next_page\n            page_info_has_previous_page: has_previous_page\n            page_info_start_cursor: start_cursor\n            page_info_end_cursor: end_cursor\n        }\n        spark_transfer_to_leaves_connection_entities: entities {\n            __typename\n            leaf_amount: amount {\n                __typename\n                currency_amount_original_value: original_value\n                currency_amount_original_unit: original_unit\n                currency_amount_preferred_currency_unit: preferred_currency_unit\n                currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n                currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n            }\n            leaf_spark_node_id: spark_node_id\n        }\n    }\n}";
export default Connection;
