import Invoice from './Invoice.js';
import LightningReceiveRequestStatus from './LightningReceiveRequestStatus.js';
import CurrencyAmount from './CurrencyAmount.js';
import { Query } from '@lightsparkdev/core';
import Transfer from './Transfer.js';
interface LightningReceiveRequest {
    /**
 * The unique identifier of this entity across all Lightspark systems. Should be treated as an opaque
 * string.
**/
    id: string;
    /** The date and time when the entity was first created. **/
    createdAt: string;
    /** The date and time when the entity was last updated. **/
    updatedAt: string;
    /** The lightning invoice generated to receive lightning payment. **/
    invoice: Invoice;
    /** The fee charged for receiving the lightning invoice. **/
    fee: CurrencyAmount;
    /** The status of the request. **/
    status: LightningReceiveRequestStatus;
    /** The typename of the object **/
    typename: string;
    /** The leaves transfer after lightning payment was received. **/
    transfer?: Transfer | undefined;
}
export declare const LightningReceiveRequestFromJson: (obj: any) => LightningReceiveRequest;
export declare const LightningReceiveRequestToJson: (obj: LightningReceiveRequest) => any;
export declare const FRAGMENT = "\nfragment LightningReceiveRequestFragment on LightningReceiveRequest {\n    __typename\n    lightning_receive_request_id: id\n    lightning_receive_request_created_at: created_at\n    lightning_receive_request_updated_at: updated_at\n    lightning_receive_request_invoice: invoice {\n        __typename\n        invoice_encoded_envoice: encoded_envoice\n        invoice_bitcoin_network: bitcoin_network\n        invoice_payment_hash: payment_hash\n        invoice_amount: amount {\n            __typename\n            currency_amount_original_value: original_value\n            currency_amount_original_unit: original_unit\n            currency_amount_preferred_currency_unit: preferred_currency_unit\n            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n        }\n        invoice_created_at: created_at\n        invoice_expires_at: expires_at\n        invoice_memo: memo\n    }\n    lightning_receive_request_fee: fee {\n        __typename\n        currency_amount_original_value: original_value\n        currency_amount_original_unit: original_unit\n        currency_amount_preferred_currency_unit: preferred_currency_unit\n        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n    }\n    lightning_receive_request_status: status\n    lightning_receive_request_transfer: transfer {\n        __typename\n        transfer_total_amount: total_amount {\n            __typename\n            currency_amount_original_value: original_value\n            currency_amount_original_unit: original_unit\n            currency_amount_preferred_currency_unit: preferred_currency_unit\n            currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n            currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n        }\n        transfer_spark_id: spark_id\n    }\n}";
export declare const getLightningReceiveRequestQuery: (id: string) => Query<LightningReceiveRequest>;
export default LightningReceiveRequest;
