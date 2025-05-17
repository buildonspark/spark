
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import UserRequest from './UserRequest.js';
import Entity from './Entity.js';
import SparkCoopExitRequestStatus from './SparkCoopExitRequestStatus.js';
import CurrencyAmount from './CurrencyAmount.js';
import {CurrencyAmountFromJson} from './CurrencyAmount.js';
import {TransferFromJson} from './Transfer.js';
import BitcoinNetwork from './BitcoinNetwork.js';
import { Query, isObject } from '@lightsparkdev/core';
import type Transfer from './Transfer.js';
import {CurrencyAmountToJson} from './CurrencyAmount.js';


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

    /** The network the lightning send request is on. **/
network: BitcoinNetwork;

    /**
 * The fee includes what user pays for the coop exit and the L1 broadcast fee. The amount user will
 * receive on L1 is total_amount - fee.
**/
fee: CurrencyAmount;

    /** The status of this coop exit request. **/
status: SparkCoopExitRequestStatus;

    /** The time when the coop exit request expires and the UTXOs are released. **/
expiresAt: string;

    /** The raw connector transaction. **/
rawConnectorTransaction: string;

    /** The raw coop exit L1 transaction. **/
rawCoopExitTransaction: string;

    /** The transaction id of the coop exit transaction. **/
coopExitTxid: string;

    /** The typename of the object **/
typename: string;

    /** The swap transfer. **/
transfer?: Transfer | undefined;




}

export const CoopExitRequestFromJson = (obj: any): CoopExitRequest => {
    return {
        id: obj["coop_exit_request_id"],
        createdAt: obj["coop_exit_request_created_at"],
        updatedAt: obj["coop_exit_request_updated_at"],
        network: BitcoinNetwork[obj["coop_exit_request_network"]] ?? BitcoinNetwork.FUTURE_VALUE,
        fee: CurrencyAmountFromJson(obj["coop_exit_request_fee"]),
        status: SparkCoopExitRequestStatus[obj["coop_exit_request_status"]] ?? SparkCoopExitRequestStatus.FUTURE_VALUE,
        expiresAt: obj["coop_exit_request_expires_at"],
        rawConnectorTransaction: obj["coop_exit_request_raw_connector_transaction"],
        rawCoopExitTransaction: obj["coop_exit_request_raw_coop_exit_transaction"],
        coopExitTxid: obj["coop_exit_request_coop_exit_txid"],
typename: "CoopExitRequest",        transfer: (!!obj["coop_exit_request_transfer"] ? TransferFromJson(obj["coop_exit_request_transfer"]) : undefined),

        } as CoopExitRequest;

}
export const CoopExitRequestToJson = (obj: CoopExitRequest): any => {
return {
__typename: "CoopExitRequest",coop_exit_request_id: obj.id,
coop_exit_request_created_at: obj.createdAt,
coop_exit_request_updated_at: obj.updatedAt,
coop_exit_request_network: obj.network,
coop_exit_request_fee: CurrencyAmountToJson(obj.fee),
coop_exit_request_status: obj.status,
coop_exit_request_expires_at: obj.expiresAt,
coop_exit_request_raw_connector_transaction: obj.rawConnectorTransaction,
coop_exit_request_raw_coop_exit_transaction: obj.rawCoopExitTransaction,
coop_exit_request_coop_exit_txid: obj.coopExitTxid,
coop_exit_request_transfer: (obj.transfer ? obj.transfer.toJson() : undefined),

        }

}


    export const FRAGMENT = `
fragment CoopExitRequestFragment on CoopExitRequest {
    __typename
    coop_exit_request_id: id
    coop_exit_request_created_at: created_at
    coop_exit_request_updated_at: updated_at
    coop_exit_request_network: network
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
    coop_exit_request_raw_coop_exit_transaction: raw_coop_exit_transaction
    coop_exit_request_coop_exit_txid: coop_exit_txid
    coop_exit_request_transfer: transfer {
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



    export const getCoopExitRequestQuery = (id: string): Query<CoopExitRequest> => {
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
            variables: {id},
            constructObject: (data: unknown) => isObject(data) && "entity" in data && isObject(data.entity) ? CoopExitRequestFromJson(data.entity) : null,
        }
    }


export default CoopExitRequest;
