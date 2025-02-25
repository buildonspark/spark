// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
import SparkLeavesSwapRequestStatus from './SparkLeavesSwapRequestStatus.js';
import { CurrencyAmountToJson } from './CurrencyAmount.js';
import { CurrencyAmountFromJson } from './CurrencyAmount.js';
import { TransferFromJson } from './Transfer.js';
import { SwapLeafToJson } from './SwapLeaf.js';
import { isObject } from '@lightsparkdev/core';
import { SwapLeafFromJson } from './SwapLeaf.js';
export const LeavesSwapRequestFromJson = (obj) => {
    return {
        id: obj["leaves_swap_request_id"],
        createdAt: obj["leaves_swap_request_created_at"],
        updatedAt: obj["leaves_swap_request_updated_at"],
        status: SparkLeavesSwapRequestStatus[obj["leaves_swap_request_status"]] ?? SparkLeavesSwapRequestStatus.FUTURE_VALUE,
        totalAmount: CurrencyAmountFromJson(obj["leaves_swap_request_total_amount"]),
        targetAmount: CurrencyAmountFromJson(obj["leaves_swap_request_target_amount"]),
        fee: CurrencyAmountFromJson(obj["leaves_swap_request_fee"]),
        inboundTransfer: TransferFromJson(obj["leaves_swap_request_inbound_transfer"]),
        expiresAt: obj["leaves_swap_request_expires_at"],
        swapLeaves: obj["leaves_swap_request_swap_leaves"].map((e) => SwapLeafFromJson(e)),
        typename: "LeavesSwapRequest", outboundTransfer: (!!obj["leaves_swap_request_outbound_transfer"] ? TransferFromJson(obj["leaves_swap_request_outbound_transfer"]) : undefined),
    };
};
export const LeavesSwapRequestToJson = (obj) => {
    return {
        __typename: "LeavesSwapRequest", leaves_swap_request_id: obj.id,
        leaves_swap_request_created_at: obj.createdAt,
        leaves_swap_request_updated_at: obj.updatedAt,
        leaves_swap_request_status: obj.status,
        leaves_swap_request_total_amount: CurrencyAmountToJson(obj.totalAmount),
        leaves_swap_request_target_amount: CurrencyAmountToJson(obj.targetAmount),
        leaves_swap_request_fee: CurrencyAmountToJson(obj.fee),
        leaves_swap_request_inbound_transfer: obj.inboundTransfer.toJson(),
        leaves_swap_request_outbound_transfer: (obj.outboundTransfer ? obj.outboundTransfer.toJson() : undefined),
        leaves_swap_request_expires_at: obj.expiresAt,
        leaves_swap_request_swap_leaves: obj.swapLeaves.map((e) => SwapLeafToJson(e)),
    };
};
export const FRAGMENT = `
fragment LeavesSwapRequestFragment on LeavesSwapRequest {
    __typename
    leaves_swap_request_id: id
    leaves_swap_request_created_at: created_at
    leaves_swap_request_updated_at: updated_at
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
export const getLeavesSwapRequestQuery = (id) => {
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
        variables: { id },
        constructObject: (data) => isObject(data) && "entity" in data && isObject(data.entity) ? LeavesSwapRequestFromJson(data.entity) : null,
    };
};
//# sourceMappingURL=LeavesSwapRequest.js.map