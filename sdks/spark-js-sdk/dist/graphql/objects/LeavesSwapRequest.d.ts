import SparkLeavesSwapRequestStatus from './SparkLeavesSwapRequestStatus.js';
import SwapLeaf from './SwapLeaf.js';
import CurrencyAmount from './CurrencyAmount.js';
import { Query } from '@lightsparkdev/core';
import Transfer from './Transfer.js';
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
export declare const LeavesSwapRequestFromJson: (obj: any) => LeavesSwapRequest;
export declare const LeavesSwapRequestToJson: (obj: LeavesSwapRequest) => any;
export declare const FRAGMENT = "\nfragment LeavesSwapRequestFragment on LeavesSwapRequest {\n    __typename\n    leaves_swap_request_id: id\n    leaves_swap_request_created_at: created_at\n    leaves_swap_request_updated_at: updated_at\n    leaves_swap_request_status: status\n    leaves_swap_request_total_amount: total_amount {\n        __typename\n        currency_amount_original_value: original_value\n        currency_amount_original_unit: original_unit\n        currency_amount_preferred_currency_unit: preferred_currency_unit\n        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n    }\n    leaves_swap_request_target_amount: target_amount {\n        __typename\n        currency_amount_original_value: original_value\n        currency_amount_original_unit: original_unit\n        currency_amount_preferred_currency_unit: preferred_currency_unit\n        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n    }\n    leaves_swap_request_fee: fee {\n        __typename\n        currency_amount_original_value: original_value\n        currency_amount_original_unit: original_unit\n        currency_amount_preferred_currency_unit: preferred_currency_unit\n        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n    }\n    leaves_swap_request_inbound_transfer: inbound_transfer {\n        __typename\n        transfer_total_amount: total_amount {\n            __typename\n            currency_amount_original_value: original_value\n            currency_amount_original_unit: original_unit\n            currency_amount_preferred_currency_unit: preferred_currency_unit\n            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n        }\n        transfer_spark_id: spark_id\n    }\n    leaves_swap_request_outbound_transfer: outbound_transfer {\n        __typename\n        transfer_total_amount: total_amount {\n            __typename\n            currency_amount_original_value: original_value\n            currency_amount_original_unit: original_unit\n            currency_amount_preferred_currency_unit: preferred_currency_unit\n            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n        }\n        transfer_spark_id: spark_id\n    }\n    leaves_swap_request_expires_at: expires_at\n    leaves_swap_request_swap_leaves: swap_leaves {\n        __typename\n        swap_leaf_leaf_id: leaf_id\n        swap_leaf_raw_unsigned_refund_transaction: raw_unsigned_refund_transaction\n        swap_leaf_adaptor_signed_signature: adaptor_signed_signature\n    }\n}";
export declare const getLeavesSwapRequestQuery: (id: string) => Query<LeavesSwapRequest>;
export default LeavesSwapRequest;
