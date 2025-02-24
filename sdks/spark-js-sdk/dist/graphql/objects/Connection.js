// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
import { LeafFromJson } from './Leaf.js';
import { PageInfoToJson } from './PageInfo.js';
import { PageInfoFromJson } from './PageInfo.js';
import { LeafToJson } from './Leaf.js';
import { LightsparkException } from '@lightsparkdev/core';
export const ConnectionFromJson = (obj) => {
    if (obj["__typename"] == "SparkTransferToLeavesConnection") {
        return {
            count: obj["spark_transfer_to_leaves_connection_count"],
            pageInfo: PageInfoFromJson(obj["spark_transfer_to_leaves_connection_page_info"]),
            entities: obj["spark_transfer_to_leaves_connection_entities"].map((e) => LeafFromJson(e)),
            typename: "SparkTransferToLeavesConnection",
        };
    }
    throw new LightsparkException("DeserializationError", `Couldn't find a concrete type for interface Connection corresponding to the typename=${obj["__typename"]}`);
};
export const ConnectionToJson = (obj) => {
    if (obj.typename == "SparkTransferToLeavesConnection") {
        const sparkTransferToLeavesConnection = obj;
        return {
            __typename: "SparkTransferToLeavesConnection", spark_transfer_to_leaves_connection_count: sparkTransferToLeavesConnection.count,
            spark_transfer_to_leaves_connection_page_info: PageInfoToJson(sparkTransferToLeavesConnection.pageInfo),
            spark_transfer_to_leaves_connection_entities: sparkTransferToLeavesConnection.entities.map((e) => LeafToJson(e)),
        };
    }
    throw new LightsparkException("DeserializationError", `Couldn't find a concrete type for interface Connection corresponding to the typename=${obj.typename}`);
};
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
//# sourceMappingURL=Connection.js.map