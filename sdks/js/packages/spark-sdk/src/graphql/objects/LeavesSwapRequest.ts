
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import UserRequest from './UserRequest.js';
import Entity from './Entity.js';
import {SwapLeafFromJson} from './SwapLeaf.js';
import CurrencyAmount from './CurrencyAmount.js';
import SwapLeaf from './SwapLeaf.js';
import {CurrencyAmountFromJson} from './CurrencyAmount.js';
import {TransferFromJson} from './Transfer.js';
import SparkLeavesSwapRequestStatus from './SparkLeavesSwapRequestStatus.js';
import BitcoinNetwork from './BitcoinNetwork.js';
import {SwapLeafToJson} from './SwapLeaf.js';
import { Query, isObject } from '@lightsparkdev/core';
import Transfer from './Transfer.js';
import {CurrencyAmountToJson} from './CurrencyAmount.js';


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

    /** The network the lightning send request is on. **/
network: BitcoinNetwork;

    /** The status of the request. **/
status: SparkLeavesSwapRequestStatus;

    /** The total amount of leaves user sent for swap. **/
totalAmount: CurrencyAmount;

    /** The target amount of leaves user wanted to get from the swap. **/
targetAmount: CurrencyAmount;

    /** The fee user needs to pay for swap. **/
fee: CurrencyAmount;

    /** The leaves transfer to user. **/
inboundTransfer: Transfer;

    /** The time when the leaves swap request expires. **/
expiresAt: string;

    /** The swap leaves returned to the user **/
swapLeaves: SwapLeaf[];

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
        network: BitcoinNetwork[obj["leaves_swap_request_network"]] ?? BitcoinNetwork.FUTURE_VALUE,
        status: SparkLeavesSwapRequestStatus[obj["leaves_swap_request_status"]] ?? SparkLeavesSwapRequestStatus.FUTURE_VALUE,
        totalAmount: CurrencyAmountFromJson(obj["leaves_swap_request_total_amount"]),
        targetAmount: CurrencyAmountFromJson(obj["leaves_swap_request_target_amount"]),
        fee: CurrencyAmountFromJson(obj["leaves_swap_request_fee"]),
        inboundTransfer: TransferFromJson(obj["leaves_swap_request_inbound_transfer"]),
        expiresAt: obj["leaves_swap_request_expires_at"],
        swapLeaves: obj["leaves_swap_request_swap_leaves"].map((e) => SwapLeafFromJson(e)),
typename: "LeavesSwapRequest",        outboundTransfer: (!!obj["leaves_swap_request_outbound_transfer"] ? TransferFromJson(obj["leaves_swap_request_outbound_transfer"]) : undefined),

        } as LeavesSwapRequest;

}
export const LeavesSwapRequestToJson = (obj: LeavesSwapRequest): any => {
return {
__typename: "LeavesSwapRequest",leaves_swap_request_id: obj.id,
leaves_swap_request_created_at: obj.createdAt,
leaves_swap_request_updated_at: obj.updatedAt,
leaves_swap_request_network: obj.network,
leaves_swap_request_status: obj.status,
leaves_swap_request_total_amount: CurrencyAmountToJson(obj.totalAmount),
leaves_swap_request_target_amount: CurrencyAmountToJson(obj.targetAmount),
leaves_swap_request_fee: CurrencyAmountToJson(obj.fee),
leaves_swap_request_inbound_transfer: obj.inboundTransfer.toJson(),
leaves_swap_request_outbound_transfer: (obj.outboundTransfer ? obj.outboundTransfer.toJson() : undefined),
leaves_swap_request_expires_at: obj.expiresAt,
leaves_swap_request_swap_leaves: obj.swapLeaves.map((e) => SwapLeafToJson(e)),

        }

}


    export const FRAGMENT = `
fragment LeavesSwapRequestFragment on LeavesSwapRequest {
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
