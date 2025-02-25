// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
import { CurrencyAmountToJson } from './CurrencyAmount.js';
import { CurrencyAmountFromJson } from './CurrencyAmount.js';
import LightningSendRequestStatus from './LightningSendRequestStatus.js';
import { TransferFromJson } from './Transfer.js';
import { isObject } from '@lightsparkdev/core';
export const LightningSendRequestFromJson = (obj) => {
    return {
        id: obj["lightning_send_request_id"],
        createdAt: obj["lightning_send_request_created_at"],
        updatedAt: obj["lightning_send_request_updated_at"],
        encodedInvoice: obj["lightning_send_request_encoded_invoice"],
        fee: CurrencyAmountFromJson(obj["lightning_send_request_fee"]),
        idempotencyKey: obj["lightning_send_request_idempotency_key"],
        status: LightningSendRequestStatus[obj["lightning_send_request_status"]] ?? LightningSendRequestStatus.FUTURE_VALUE,
        typename: "LightningSendRequest", transfer: (!!obj["lightning_send_request_transfer"] ? TransferFromJson(obj["lightning_send_request_transfer"]) : undefined),
    };
};
export const LightningSendRequestToJson = (obj) => {
    return {
        __typename: "LightningSendRequest", lightning_send_request_id: obj.id,
        lightning_send_request_created_at: obj.createdAt,
        lightning_send_request_updated_at: obj.updatedAt,
        lightning_send_request_encoded_invoice: obj.encodedInvoice,
        lightning_send_request_fee: CurrencyAmountToJson(obj.fee),
        lightning_send_request_idempotency_key: obj.idempotencyKey,
        lightning_send_request_status: obj.status,
        lightning_send_request_transfer: (obj.transfer ? obj.transfer.toJson() : undefined),
    };
};
export const FRAGMENT = `
fragment LightningSendRequestFragment on LightningSendRequest {
    __typename
    lightning_send_request_id: id
    lightning_send_request_created_at: created_at
    lightning_send_request_updated_at: updated_at
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
}`;
export const getLightningSendRequestQuery = (id) => {
    return {
        queryPayload: `
query GetLightningSendRequest($id: ID!) {
    entity(id: $id) {
        ... on LightningSendRequest {
            ...LightningSendRequestFragment
        }
    }
}

${FRAGMENT}    
`,
        variables: { id },
        constructObject: (data) => isObject(data) && "entity" in data && isObject(data.entity) ? LightningSendRequestFromJson(data.entity) : null,
    };
};
//# sourceMappingURL=LightningSendRequest.js.map