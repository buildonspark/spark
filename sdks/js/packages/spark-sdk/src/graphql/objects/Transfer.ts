// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import autoBind from "../../auto-bind.js";
import type LightsparkClient from "../client.js";
import CurrencyAmount, {
  CurrencyAmountFromJson,
  CurrencyAmountToJson,
} from "./CurrencyAmount.js";
import SparkTransferToLeavesConnection, {
  SparkTransferToLeavesConnectionFromJson,
} from "./SparkTransferToLeavesConnection.js";

class Transfer {
  constructor(
    /** The total amount of the transfer. **/
    public readonly totalAmount: CurrencyAmount,
    /** The id of the transfer known at signing operators. If not set, the transfer hasn't been
     * initialized. **/
    public readonly sparkId?: string | undefined,
  ) {
    autoBind(this);
  }

  public async getLeaves(
    client: LightsparkClient,
    first: number | undefined = undefined,
    after: string | undefined = undefined,
  ): Promise<SparkTransferToLeavesConnection> {
    return (await client.executeRawQuery({
      queryPayload: ` 
query FetchSparkTransferToLeavesConnection($entity_id: ID!, $first: Int, $after: String) {
    entity(id: $entity_id) {
        ... on Transfer {
            leaves(, first: $first, after: $after) {
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
        }
    }
}
`,
      variables: { entity_id: this.sparkId, first: first, after: after },
      constructObject: (json) => {
        const connection = json["entity"]["leaves"];
        return SparkTransferToLeavesConnectionFromJson(connection);
      },
    }))!;
  }

  public toJson() {
    return {
      transfer_total_amount: CurrencyAmountToJson(this.totalAmount),
      transfer_spark_id: this.sparkId,
    };
  }
}

export const TransferFromJson = (obj: any): Transfer => {
  return new Transfer(
    CurrencyAmountFromJson(obj["transfer_total_amount"]),
    obj["transfer_spark_id"],
  );
};

export const FRAGMENT = `
fragment TransferFragment on Transfer {
    __typename
    transfer_total_amount: total_amount {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
        currency_amount_preferred_currency_unit: preferred_currency_unit
        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
    }
    transfer_spark_id: spark_id
}`;

export default Transfer;
