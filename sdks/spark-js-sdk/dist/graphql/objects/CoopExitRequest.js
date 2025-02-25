// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
import { CurrencyAmountToJson } from './CurrencyAmount.js';
import { CurrencyAmountFromJson } from './CurrencyAmount.js';
import SparkCoopExitRequestStatus from './SparkCoopExitRequestStatus.js';
import { isObject } from '@lightsparkdev/core';
export const CoopExitRequestFromJson = (obj) => {
    return {
        id: obj["coop_exit_request_id"],
        createdAt: obj["coop_exit_request_created_at"],
        updatedAt: obj["coop_exit_request_updated_at"],
        fee: CurrencyAmountFromJson(obj["coop_exit_request_fee"]),
        status: SparkCoopExitRequestStatus[obj["coop_exit_request_status"]] ?? SparkCoopExitRequestStatus.FUTURE_VALUE,
        expiresAt: obj["coop_exit_request_expires_at"],
        rawConnectorTransaction: obj["coop_exit_request_raw_connector_transaction"],
        typename: "CoopExitRequest",
    };
};
export const CoopExitRequestToJson = (obj) => {
    return {
        __typename: "CoopExitRequest", coop_exit_request_id: obj.id,
        coop_exit_request_created_at: obj.createdAt,
        coop_exit_request_updated_at: obj.updatedAt,
        coop_exit_request_fee: CurrencyAmountToJson(obj.fee),
        coop_exit_request_status: obj.status,
        coop_exit_request_expires_at: obj.expiresAt,
        coop_exit_request_raw_connector_transaction: obj.rawConnectorTransaction,
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
    coop_exit_request_raw_connector_transaction: raw_connector_transaction
}`;
export const getCoopExitRequestQuery = (id) => {
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
        constructObject: (data) => isObject(data) && "entity" in data && isObject(data.entity) ? CoopExitRequestFromJson(data.entity) : null,
    };
};
//# sourceMappingURL=CoopExitRequest.js.map