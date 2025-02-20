
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import Entity from './Entity.js';
import {CurrencyAmountFromJson} from './CurrencyAmount.js';
import Transfer from './Transfer.js';
import CurrencyAmount from './CurrencyAmount.js';
import { Query, isObject } from '@lightsparkdev/core';
import {TransferFromJson} from './Transfer.js';
import {CurrencyAmountToJson} from './CurrencyAmount.js';
import LightningSendRequestStatus from './LightningSendRequestStatus.js';


interface LightningSendRequest {


    /**
 * The unique identifier of this entity across all Lightspark systems. Should be treated as an opaque
 * string.
**/
id: string;

    /** The date and time when the entity was first created. **/
createdAt: string;

    /** The date and time when the entity was last updated. **/
updatedAt: string;

    /** The lightning invoice user requested to pay. **/
encodedInvoice: string;

    /** The fee charged for paying the lightning invoice. **/
fee: CurrencyAmount;

    /** The idempotency key of the request. **/
idempotencyKey: string;

    /** The status of the request. **/
status: LightningSendRequestStatus;

    /** The typename of the object **/
typename: string;

    /** The leaves transfer after lightning payment was sent. **/
transfer?: Transfer | undefined;




}

export const LightningSendRequestFromJson = (obj: any): LightningSendRequest => {
    return {
        id: obj["lightning_send_request_id"],
        createdAt: obj["lightning_send_request_created_at"],
        updatedAt: obj["lightning_send_request_updated_at"],
        encodedInvoice: obj["lightning_send_request_encoded_invoice"],
        fee: CurrencyAmountFromJson(obj["lightning_send_request_fee"]),
        idempotencyKey: obj["lightning_send_request_idempotency_key"],
        status: LightningSendRequestStatus[obj["lightning_send_request_status"]] ?? LightningSendRequestStatus.FUTURE_VALUE,
typename: "LightningSendRequest",        transfer: (!!obj["lightning_send_request_transfer"] ? TransferFromJson(obj["lightning_send_request_transfer"]) : undefined),

        } as LightningSendRequest;

}
export const LightningSendRequestToJson = (obj: LightningSendRequest): any => {
return {
__typename: "LightningSendRequest",lightning_send_request_id: obj.id,
lightning_send_request_created_at: obj.createdAt,
lightning_send_request_updated_at: obj.updatedAt,
lightning_send_request_encoded_invoice: obj.encodedInvoice,
lightning_send_request_fee: CurrencyAmountToJson(obj.fee),
lightning_send_request_idempotency_key: obj.idempotencyKey,
lightning_send_request_status: obj.status,
lightning_send_request_transfer: (obj.transfer ? obj.transfer.toJson() : undefined),

        }

}


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



    export const getLightningSendRequestQuery = (id: string): Query<LightningSendRequest> => {
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
            variables: {id},
            constructObject: (data: unknown) => isObject(data) && "entity" in data && isObject(data.entity) ? LightningSendRequestFromJson(data.entity) : null,
        }
    }


export default LightningSendRequest;
