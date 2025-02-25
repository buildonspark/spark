import LightningSendRequestStatus from './LightningSendRequestStatus.js';
import CurrencyAmount from './CurrencyAmount.js';
import { Query } from '@lightsparkdev/core';
import Transfer from './Transfer.js';
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
export declare const LightningSendRequestFromJson: (obj: any) => LightningSendRequest;
export declare const LightningSendRequestToJson: (obj: LightningSendRequest) => any;
export declare const FRAGMENT = "\nfragment LightningSendRequestFragment on LightningSendRequest {\n    __typename\n    lightning_send_request_id: id\n    lightning_send_request_created_at: created_at\n    lightning_send_request_updated_at: updated_at\n    lightning_send_request_encoded_invoice: encoded_invoice\n    lightning_send_request_fee: fee {\n        __typename\n        currency_amount_original_value: original_value\n        currency_amount_original_unit: original_unit\n        currency_amount_preferred_currency_unit: preferred_currency_unit\n        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n    }\n    lightning_send_request_idempotency_key: idempotency_key\n    lightning_send_request_status: status\n    lightning_send_request_transfer: transfer {\n        __typename\n        transfer_total_amount: total_amount {\n            __typename\n            currency_amount_original_value: original_value\n            currency_amount_original_unit: original_unit\n            currency_amount_preferred_currency_unit: preferred_currency_unit\n            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n        }\n        transfer_spark_id: spark_id\n    }\n}";
export declare const getLightningSendRequestQuery: (id: string) => Query<LightningSendRequest>;
export default LightningSendRequest;
