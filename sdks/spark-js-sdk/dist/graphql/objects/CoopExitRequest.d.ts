import SparkCoopExitRequestStatus from './SparkCoopExitRequestStatus.js';
import CurrencyAmount from './CurrencyAmount.js';
import { Query } from '@lightsparkdev/core';
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
    /** The raw connector transaction. **/
    rawConnectorTransaction: string;
    /** The typename of the object **/
    typename: string;
}
export declare const CoopExitRequestFromJson: (obj: any) => CoopExitRequest;
export declare const CoopExitRequestToJson: (obj: CoopExitRequest) => any;
export declare const FRAGMENT = "\nfragment CoopExitRequestFragment on CoopExitRequest {\n    __typename\n    coop_exit_request_id: id\n    coop_exit_request_created_at: created_at\n    coop_exit_request_updated_at: updated_at\n    coop_exit_request_fee: fee {\n        __typename\n        currency_amount_original_value: original_value\n        currency_amount_original_unit: original_unit\n        currency_amount_preferred_currency_unit: preferred_currency_unit\n        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n    }\n    coop_exit_request_status: status\n    coop_exit_request_expires_at: expires_at\n    coop_exit_request_raw_connector_transaction: raw_connector_transaction\n}";
export declare const getCoopExitRequestQuery: (id: string) => Query<CoopExitRequest>;
export default CoopExitRequest;
