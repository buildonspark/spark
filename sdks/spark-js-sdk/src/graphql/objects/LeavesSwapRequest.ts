
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import Entity from './Entity.js';
import { Query, isObject } from '@lightsparkdev/core';
import {TransferFromJson} from './Transfer.js';
import Transfer from './Transfer.js';
import SparkLeavesSwapRequestStatus from './SparkLeavesSwapRequestStatus.js';


interface LeavesSwapRequest {


    /**
 * The unique identifier of this entity across all Lightspark systems. Should be treated as an opaque
 * string.
**/
id: string;

    /** The date and time when the entity was first created. **/
createdAt: string;

    /** The date and time when the entity was last updated. **/
updatedAt: string;

    /** The status of the request. **/
status: SparkLeavesSwapRequestStatus;

    /** The leaves transfer to user. **/
inboundTransfer: Transfer;

    /** The time when the leaves swap request expires. **/
expiresAt: string;

    /** The typename of the object **/
typename: string;

    /** The leaves transfer out from user. **/
outboundTransfer?: Transfer | undefined;




}

export const LeavesSwapRequestFromJson = (obj: any): LeavesSwapRequest => {
    return {
        id: obj["leaves_swap_request_id"],
        createdAt: obj["leaves_swap_request_created_at"],
        updatedAt: obj["leaves_swap_request_updated_at"],
        status: SparkLeavesSwapRequestStatus[obj["leaves_swap_request_status"]] ?? SparkLeavesSwapRequestStatus.FUTURE_VALUE,
        inboundTransfer: TransferFromJson(obj["leaves_swap_request_inbound_transfer"]),
        expiresAt: obj["leaves_swap_request_expires_at"],
typename: "LeavesSwapRequest",        outboundTransfer: (!!obj["leaves_swap_request_outbound_transfer"] ? TransferFromJson(obj["leaves_swap_request_outbound_transfer"]) : undefined),

        } as LeavesSwapRequest;

}
export const LeavesSwapRequestToJson = (obj: LeavesSwapRequest): any => {
return {
__typename: "LeavesSwapRequest",leaves_swap_request_id: obj.id,
leaves_swap_request_created_at: obj.createdAt,
leaves_swap_request_updated_at: obj.updatedAt,
leaves_swap_request_status: obj.status,
leaves_swap_request_inbound_transfer: obj.inboundTransfer.toJson(),
leaves_swap_request_outbound_transfer: (obj.outboundTransfer ? obj.outboundTransfer.toJson() : undefined),
leaves_swap_request_expires_at: obj.expiresAt,

        }

}


    export const FRAGMENT = `
fragment LeavesSwapRequestFragment on LeavesSwapRequest {
    __typename
    leaves_swap_request_id: id
    leaves_swap_request_created_at: created_at
    leaves_swap_request_updated_at: updated_at
    leaves_swap_request_status: status
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
}`;



    export const getLeavesSwapRequestQuery = (id: string): Query<LeavesSwapRequest> => {
        return {
            queryPayload: `
query GetLeavesSwapRequest($id: ID!) {
    entity(id: $id) {
        ... on LeavesSwapRequest {
            ...LeavesSwapRequestFragment
        }
    }
}

${FRAGMENT}    
`,
            variables: {id},
            constructObject: (data: unknown) => isObject(data) && "entity" in data && isObject(data.entity) ? LeavesSwapRequestFromJson(data.entity) : null,
        }
    }


export default LeavesSwapRequest;
