// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import { Query, isObject } from "@lightsparkdev/core";
import autoBind from "../../auto-bind.js";
import LightsparkClient from "../client.js";
import BitcoinNetwork from "./BitcoinNetwork.js";
import Entity from "./Entity.js";
import SparkUserRequestStatus from "./SparkUserRequestStatus.js";
import SparkUserRequestType from "./SparkUserRequestType.js";
import SparkWalletUserToUserRequestsConnection, {
  SparkWalletUserToUserRequestsConnectionFromJson,
} from "./SparkWalletUserToUserRequestsConnection.js";

class SparkWalletUser implements Entity {
  constructor(
    /**
     * The unique identifier of this entity across all Lightspark systems. Should be treated as an opaque
     * string.
     **/
    public readonly id: string,
    /** The date and time when the entity was first created. **/
    public readonly createdAt: string,
    /** The date and time when the entity was last updated. **/
    public readonly updatedAt: string,
    /** The identity public key of the user. **/
    public readonly identityPublicKey: string,
    /** The typename of the object **/
    public readonly typename: string,
  ) {
    autoBind(this);
  }

  public async getUserRequests(
    client: LightsparkClient,
    first: number | undefined = undefined,
    after: string | undefined = undefined,
    types: SparkUserRequestType[] | undefined = undefined,
    statuses: SparkUserRequestStatus[] | undefined = undefined,
    networks: BitcoinNetwork[] | undefined = undefined,
  ): Promise<SparkWalletUserToUserRequestsConnection> {
    return (await client.executeRawQuery({
      queryPayload: ` 
query FetchSparkWalletUserToUserRequestsConnection($entity_id: ID!, $first: Int, $after: String, $types: [SparkUserRequestType!], $statuses: [SparkUserRequestStatus!], $networks: [BitcoinNetwork!]) {
    entity(id: $entity_id) {
        ... on SparkWalletUser {
            user_requests(, first: $first, after: $after, types: $types, statuses: $statuses, networks: $networks) {
                __typename
                spark_wallet_user_to_user_requests_connection_count: count
                spark_wallet_user_to_user_requests_connection_page_info: page_info {
                    __typename
                    page_info_has_next_page: has_next_page
                    page_info_has_previous_page: has_previous_page
                    page_info_start_cursor: start_cursor
                    page_info_end_cursor: end_cursor
                }
                spark_wallet_user_to_user_requests_connection_entities: entities {
                    __typename
                    ... on CoopExitRequest {
                        __typename
                        coop_exit_request_id: id
                        coop_exit_request_created_at: created_at
                        coop_exit_request_updated_at: updated_at
                        coop_exit_request_network: network
                        coop_exit_request_fee: fee {
                            __typename
                            currency_amount_original_value: original_value
                            currency_amount_original_unit: original_unit
                            currency_amount_preferred_currency_unit: preferred_currency_unit
                            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
                            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
                        }
                        coop_exit_request_status: status
                        coop_exit_request_expires_at: expires_at
                        coop_exit_request_raw_connector_transaction: raw_connector_transaction
                        coop_exit_request_raw_coop_exit_transaction: raw_coop_exit_transaction
                        coop_exit_request_coop_exit_txid: coop_exit_txid
                        coop_exit_request_transfer: transfer {
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
                        }
                    }
                    ... on LeavesSwapRequest {
                        __typename
                        leaves_swap_request_id: id
                        leaves_swap_request_created_at: created_at
                        leaves_swap_request_updated_at: updated_at
                        leaves_swap_request_network: network
                        leaves_swap_request_status: status
                        leaves_swap_request_total_amount: total_amount {
                            __typename
                            currency_amount_original_value: original_value
                            currency_amount_original_unit: original_unit
                            currency_amount_preferred_currency_unit: preferred_currency_unit
                            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
                            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
                        }
                        leaves_swap_request_target_amount: target_amount {
                            __typename
                            currency_amount_original_value: original_value
                            currency_amount_original_unit: original_unit
                            currency_amount_preferred_currency_unit: preferred_currency_unit
                            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
                            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
                        }
                        leaves_swap_request_fee: fee {
                            __typename
                            currency_amount_original_value: original_value
                            currency_amount_original_unit: original_unit
                            currency_amount_preferred_currency_unit: preferred_currency_unit
                            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
                            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
                        }
                        leaves_swap_request_inbound_transfer: inbound_transfer {
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
                        }
                        leaves_swap_request_outbound_transfer: outbound_transfer {
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
                        }
                        leaves_swap_request_expires_at: expires_at
                        leaves_swap_request_swap_leaves: swap_leaves {
                            __typename
                            swap_leaf_leaf_id: leaf_id
                            swap_leaf_raw_unsigned_refund_transaction: raw_unsigned_refund_transaction
                            swap_leaf_adaptor_signed_signature: adaptor_signed_signature
                        }
                    }
                    ... on LightningReceiveRequest {
                        __typename
                        lightning_receive_request_id: id
                        lightning_receive_request_created_at: created_at
                        lightning_receive_request_updated_at: updated_at
                        lightning_receive_request_network: network
                        lightning_receive_request_invoice: invoice {
                            __typename
                            invoice_encoded_invoice: encoded_invoice
                            invoice_bitcoin_network: bitcoin_network
                            invoice_payment_hash: payment_hash
                            invoice_amount: amount {
                                __typename
                                currency_amount_original_value: original_value
                                currency_amount_original_unit: original_unit
                                currency_amount_preferred_currency_unit: preferred_currency_unit
                                currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
                                currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
                            }
                            invoice_created_at: created_at
                            invoice_expires_at: expires_at
                            invoice_memo: memo
                        }
                        lightning_receive_request_status: status
                        lightning_receive_request_transfer: transfer {
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
                        }
                        lightning_receive_request_payment_preimage: payment_preimage
                    }
                    ... on LightningSendRequest {
                        __typename
                        lightning_send_request_id: id
                        lightning_send_request_created_at: created_at
                        lightning_send_request_updated_at: updated_at
                        lightning_send_request_network: network
                        lightning_send_request_encoded_invoice: encoded_invoice
                        lightning_send_request_fee: fee {
                            __typename
                            currency_amount_original_value: original_value
                            currency_amount_original_unit: original_unit
                            currency_amount_preferred_currency_unit: preferred_currency_unit
                            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
                            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
                        }
                        lightning_send_request_idempotency_key: idempotency_key
                        lightning_send_request_status: status
                        lightning_send_request_transfer: transfer {
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
                        }
                        lightning_send_request_payment_preimage: payment_preimage
                    }
                }
            }
        }
    }
}
`,
      variables: {
        entity_id: this.id,
        first: first,
        after: after,
        types: types,
        statuses: statuses,
        networks: networks,
      },
      constructObject: (json) => {
        const connection = json["entity"]["user_requests"];
        return SparkWalletUserToUserRequestsConnectionFromJson(connection);
      },
    }))!;
  }

  static getSparkWalletUserQuery(id: string): Query<SparkWalletUser> {
    return {
      queryPayload: `
query GetSparkWalletUser($id: ID!) {
    entity(id: $id) {
        ... on SparkWalletUser {
            ...SparkWalletUserFragment
        }
    }
}

${FRAGMENT}    
`,
      variables: { id },
      constructObject: (data: unknown) =>
        isObject(data) && "entity" in data && isObject(data.entity)
          ? SparkWalletUserFromJson(data.entity)
          : null,
    };
  }

  public toJson() {
    return {
      __typename: "SparkWalletUser",
      spark_wallet_user_id: this.id,
      spark_wallet_user_created_at: this.createdAt,
      spark_wallet_user_updated_at: this.updatedAt,
      spark_wallet_user_identity_public_key: this.identityPublicKey,
    };
  }
}

export const SparkWalletUserFromJson = (obj: any): SparkWalletUser => {
  return new SparkWalletUser(
    obj["spark_wallet_user_id"],
    obj["spark_wallet_user_created_at"],
    obj["spark_wallet_user_updated_at"],
    obj["spark_wallet_user_identity_public_key"],
    "SparkWalletUser",
  );
};

export const FRAGMENT = `
fragment SparkWalletUserFragment on SparkWalletUser {
    __typename
    spark_wallet_user_id: id
    spark_wallet_user_created_at: created_at
    spark_wallet_user_updated_at: updated_at
    spark_wallet_user_identity_public_key: identity_public_key
}`;

export default SparkWalletUser;
