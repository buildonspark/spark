
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import Leaf from './Leaf.js';
import {PageInfoFromJson} from './PageInfo.js';
import {LeafToJson} from './Leaf.js';
import {LeafFromJson} from './Leaf.js';
import PageInfo from './PageInfo.js';
import {PageInfoToJson} from './PageInfo.js';


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

export const SparkTransferToLeavesConnectionFromJson = (obj: any): SparkTransferToLeavesConnection => {
    return {
        count: obj["spark_transfer_to_leaves_connection_count"],
        pageInfo: PageInfoFromJson(obj["spark_transfer_to_leaves_connection_page_info"]),
        entities: obj["spark_transfer_to_leaves_connection_entities"].map((e) => LeafFromJson(e)),
typename: "SparkTransferToLeavesConnection",
        } as SparkTransferToLeavesConnection;

}
export const SparkTransferToLeavesConnectionToJson = (obj: SparkTransferToLeavesConnection): any => {
return {
__typename: "SparkTransferToLeavesConnection",spark_transfer_to_leaves_connection_count: obj.count,
spark_transfer_to_leaves_connection_page_info: PageInfoToJson(obj.pageInfo),
spark_transfer_to_leaves_connection_entities: obj.entities.map((e) => LeafToJson(e)),

        }

}


    export const FRAGMENT = `
fragment SparkTransferToLeavesConnectionFragment on SparkTransferToLeavesConnection {
    __typename
    spark_transfer_to_leaves_connection_count: count
    spark_transfer_to_leaves_connection_page_info: page_info {
        __typename
        page_info_has_next_page: has_next_page
        page_info_has_previous_page: has_previous_page
        page_info_start_cursor: start_cursor
        page_info_end_cursor: end_cursor
    }
    spark_transfer_to_leaves_connection_entities: entities {
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
    }
}`;




export default SparkTransferToLeavesConnection;
