
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import SparkTransferToLeavesConnection from './SparkTransferToLeavesConnection.js';
import {PageInfoToJson} from './PageInfo.js';
import {LeafToJson} from './Leaf.js';
import {PageInfoFromJson} from './PageInfo.js';
import { LightsparkException } from '@lightsparkdev/core';
import {LeafFromJson} from './Leaf.js';
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

export const ConnectionFromJson = (obj: any): Connection => {
    if (obj["__typename"] == "SparkTransferToLeavesConnection") {
        return {
            count: obj["spark_transfer_to_leaves_connection_count"],
            pageInfo: PageInfoFromJson(obj["spark_transfer_to_leaves_connection_page_info"]),
            entities: obj["spark_transfer_to_leaves_connection_entities"].map((e) => LeafFromJson(e)),
typename: "SparkTransferToLeavesConnection",
        } as SparkTransferToLeavesConnection;

}    throw new LightsparkException("DeserializationError", `Couldn't find a concrete type for interface Connection corresponding to the typename=${obj["__typename"]}`)
}
export const ConnectionToJson = (obj: Connection): any => {
    if (obj.typename == "SparkTransferToLeavesConnection") {
       const sparkTransferToLeavesConnection = obj as SparkTransferToLeavesConnection;
return {
__typename: "SparkTransferToLeavesConnection",spark_transfer_to_leaves_connection_count: sparkTransferToLeavesConnection.count,
spark_transfer_to_leaves_connection_page_info: PageInfoToJson(sparkTransferToLeavesConnection.pageInfo),
spark_transfer_to_leaves_connection_entities: sparkTransferToLeavesConnection.entities.map((e) => LeafToJson(e)),

        }

}    throw new LightsparkException("DeserializationError", `Couldn't find a concrete type for interface Connection corresponding to the typename=${obj.typename}`)
}


    export const FRAGMENT = `
fragment ConnectionFragment on Connection {
    __typename
    ... on SparkTransferToLeavesConnection {
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
    }
}`;




export default Connection;
