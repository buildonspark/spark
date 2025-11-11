// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import { Query, isObject } from "@lightsparkdev/core";
import BitcoinNetwork from "./BitcoinNetwork.js";
import Invoice, { InvoiceFromJson, InvoiceToJson } from "./Invoice.js";
import LightningReceiveRequestStatus from "./LightningReceiveRequestStatus.js";
import Transfer, { TransferFromJson } from "./Transfer.js";

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

  /** The network the lightning send request is on. **/
  network: BitcoinNetwork;

  /** The lightning invoice generated to receive lightning payment. **/
  invoice: Invoice;

  /** The status of the request. **/
  status: LightningReceiveRequestStatus;

  /** The typename of the object **/
  typename: string;

  /** The leaves transfer after lightning payment was received. **/
  transfer?: Transfer | undefined;

  /** The payment preimage of the invoice if retrieved from SE. **/
  paymentPreimage?: string | undefined;

  /** The receiver's identity public key if different from owner of the request. **/
  receiverIdentityPublicKey?: string | undefined;
}

export const LightningReceiveRequestFromJson = (
  obj: any,
): LightningReceiveRequest => {
  return {
    id: obj["lightning_receive_request_id"],
    createdAt: obj["lightning_receive_request_created_at"],
    updatedAt: obj["lightning_receive_request_updated_at"],
    network:
      BitcoinNetwork[obj["lightning_receive_request_network"]] ??
      BitcoinNetwork.FUTURE_VALUE,
    invoice: InvoiceFromJson(obj["lightning_receive_request_invoice"]),
    status:
      LightningReceiveRequestStatus[obj["lightning_receive_request_status"]] ??
      LightningReceiveRequestStatus.FUTURE_VALUE,
    typename: "LightningReceiveRequest",
    transfer: !!obj["lightning_receive_request_transfer"]
      ? TransferFromJson(obj["lightning_receive_request_transfer"])
      : undefined,
    paymentPreimage: obj["lightning_receive_request_payment_preimage"],
    receiverIdentityPublicKey:
      obj["lightning_receive_request_receiver_identity_public_key"],
  } as LightningReceiveRequest;
};
export const LightningReceiveRequestToJson = (
  obj: LightningReceiveRequest,
): any => {
  return {
    __typename: "LightningReceiveRequest",
    lightning_receive_request_id: obj.id,
    lightning_receive_request_created_at: obj.createdAt,
    lightning_receive_request_updated_at: obj.updatedAt,
    lightning_receive_request_network: obj.network,
    lightning_receive_request_invoice: InvoiceToJson(obj.invoice),
    lightning_receive_request_status: obj.status,
    lightning_receive_request_transfer: obj.transfer
      ? obj.transfer.toJson()
      : undefined,
    lightning_receive_request_payment_preimage: obj.paymentPreimage,
    lightning_receive_request_receiver_identity_public_key:
      obj.receiverIdentityPublicKey,
  };
};

export const FRAGMENT = `
fragment LightningReceiveRequestFragment on LightningReceiveRequest {
    __typename
    lightning_receive_request_id: id
    lightning_receive_request_created_at: created_at
    lightning_receive_request_updated_at: updated_at
    lightning_receive_request_network: network
    lightning_receive_request_invoice: invoice {
        __typename
        invoice_encoded_invoice: encoded_invoice
        invoice_bitcoin_network: bitcoin_network
        invoice_payment_hash: payment_hash
        invoice_amount: amount {
            __typename
            currency_amount_original_value: original_value
            currency_amount_original_unit: original_unit
        }
        invoice_created_at: created_at
        invoice_expires_at: expires_at
        invoice_memo: memo
    }
    lightning_receive_request_status: status
    lightning_receive_request_transfer: transfer {
        __typename
        transfer_total_amount: total_amount {
            __typename
            currency_amount_original_value: original_value
            currency_amount_original_unit: original_unit
        }
        transfer_spark_id: spark_id
        transfer_user_request: user_request {
            id
        }
    }
    lightning_receive_request_payment_preimage: payment_preimage
    lightning_receive_request_receiver_identity_public_key: receiver_identity_public_key
}`;

export const getLightningReceiveRequestQuery = (
  id: string,
): Query<LightningReceiveRequest> => {
  return {
    queryPayload: `
query GetLightningReceiveRequest($id: ID!) {
    entity(id: $id) {
        ... on LightningReceiveRequest {
            ...LightningReceiveRequestFragment
        }
    }
}

${FRAGMENT}    
`,
    variables: { id },
    constructObject: (data: unknown) =>
      isObject(data) && "entity" in data && isObject(data.entity)
        ? LightningReceiveRequestFromJson(data.entity)
        : null,
  };
};

export default LightningReceiveRequest;
