// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import { Query, isObject } from "@lightsparkdev/core";
import CurrencyAmount, {
  CurrencyAmountFromJson,
  CurrencyAmountToJson,
} from "./CurrencyAmount.js";
import SparkCoopExitRequestStatus from "./SparkCoopExitRequestStatus.js";

interface CoopExitRequest {
  /**
   * The unique identifier of this entity across all Lightspark systems. Should be treated as an opaque
   * string.
   **/
  id: string;

  /** The date and time when the entity was first created. **/
  createdAt: string;

  /** The date and time when the entity was last updated. **/
  updatedAt: string;

  /**
   * The fee includes what user pays for the coop exit and the L1 broadcast fee. The amount user will
   * receive on L1 is total_amount - fee.
   **/
  fee: CurrencyAmount;

  /** The status of the request. **/
  status: SparkCoopExitRequestStatus;

  /** The time when the coop exit request expires and the UTXOs are released. **/
  expiresAt: string;

  /** The typename of the object **/
  typename: string;
}

export const CoopExitRequestFromJson = (obj: any): CoopExitRequest => {
  return {
    id: obj["coop_exit_request_id"],
    createdAt: obj["coop_exit_request_created_at"],
    updatedAt: obj["coop_exit_request_updated_at"],
    fee: CurrencyAmountFromJson(obj["coop_exit_request_fee"]),
    status:
      SparkCoopExitRequestStatus[
        obj[
          "coop_exit_request_status"
        ] as keyof typeof SparkCoopExitRequestStatus
      ] ?? SparkCoopExitRequestStatus.FUTURE_VALUE,
    expiresAt: obj["coop_exit_request_expires_at"],
    typename: "CoopExitRequest",
  } as CoopExitRequest;
};
export const CoopExitRequestToJson = (obj: CoopExitRequest): any => {
  return {
    __typename: "CoopExitRequest",
    coop_exit_request_id: obj.id,
    coop_exit_request_created_at: obj.createdAt,
    coop_exit_request_updated_at: obj.updatedAt,
    coop_exit_request_fee: CurrencyAmountToJson(obj.fee),
    coop_exit_request_status: obj.status,
    coop_exit_request_expires_at: obj.expiresAt,
  };
};

export const FRAGMENT = `
fragment CoopExitRequestFragment on CoopExitRequest {
    __typename
    coop_exit_request_id: id
    coop_exit_request_created_at: created_at
    coop_exit_request_updated_at: updated_at
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
}`;

export const getCoopExitRequestQuery = (id: string): Query<CoopExitRequest> => {
  return {
    queryPayload: `
query GetCoopExitRequest($id: ID!) {
    entity(id: $id) {
        ... on CoopExitRequest {
            ...CoopExitRequestFragment
        }
    }
}

${FRAGMENT}    
`,
    variables: { id },
    constructObject: (data: unknown) =>
      isObject(data) && "entity" in data && isObject(data.entity)
        ? CoopExitRequestFromJson(data.entity)
        : null,
  };
};

export default CoopExitRequest;
