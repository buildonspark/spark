// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import { Query, isObject } from "@lightsparkdev/core";
import BitcoinNetwork from "./BitcoinNetwork.js";
import ClaimStaticDepositStatus from "./ClaimStaticDepositStatus.js";
import CurrencyAmount, {
  CurrencyAmountFromJson,
  CurrencyAmountToJson,
} from "./CurrencyAmount.js";

interface ClaimStaticDeposit {
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

  /** The amount of credit to be added to the user's balance. **/
  creditAmount: CurrencyAmount;

  /** The maximum fee that the user is willing to pay. **/
  maxFee: CurrencyAmount;

  /** The status of the request. **/
  status: ClaimStaticDepositStatus;

  /** The transaction id of the deposit. **/
  transactionId: string;

  /** The output index of the deposit. **/
  outputIndex: number;

  /** The Bitcoin network of the deposit. **/
  bitcoinNetwork: BitcoinNetwork;

  /** The typename of the object **/
  typename: string;

  /** The id of the transfer. **/
  transferSparkId?: string | undefined;
}

export const ClaimStaticDepositFromJson = (obj: any): ClaimStaticDeposit => {
  return {
    id: obj["claim_static_deposit_id"],
    createdAt: obj["claim_static_deposit_created_at"],
    updatedAt: obj["claim_static_deposit_updated_at"],
    network:
      BitcoinNetwork[obj["claim_static_deposit_network"]] ??
      BitcoinNetwork.FUTURE_VALUE,
    creditAmount: CurrencyAmountFromJson(
      obj["claim_static_deposit_credit_amount"],
    ),
    maxFee: CurrencyAmountFromJson(obj["claim_static_deposit_max_fee"]),
    status:
      ClaimStaticDepositStatus[obj["claim_static_deposit_status"]] ??
      ClaimStaticDepositStatus.FUTURE_VALUE,
    transactionId: obj["claim_static_deposit_transaction_id"],
    outputIndex: obj["claim_static_deposit_output_index"],
    bitcoinNetwork:
      BitcoinNetwork[obj["claim_static_deposit_bitcoin_network"]] ??
      BitcoinNetwork.FUTURE_VALUE,
    typename: "ClaimStaticDeposit",
    transferSparkId: obj["claim_static_deposit_transfer_spark_id"],
  } as ClaimStaticDeposit;
};
export const ClaimStaticDepositToJson = (obj: ClaimStaticDeposit): any => {
  return {
    __typename: "ClaimStaticDeposit",
    claim_static_deposit_id: obj.id,
    claim_static_deposit_created_at: obj.createdAt,
    claim_static_deposit_updated_at: obj.updatedAt,
    claim_static_deposit_network: obj.network,
    claim_static_deposit_credit_amount: CurrencyAmountToJson(obj.creditAmount),
    claim_static_deposit_max_fee: CurrencyAmountToJson(obj.maxFee),
    claim_static_deposit_status: obj.status,
    claim_static_deposit_transaction_id: obj.transactionId,
    claim_static_deposit_output_index: obj.outputIndex,
    claim_static_deposit_bitcoin_network: obj.bitcoinNetwork,
    claim_static_deposit_transfer_spark_id: obj.transferSparkId,
  };
};

export const FRAGMENT = `
fragment ClaimStaticDepositFragment on ClaimStaticDeposit {
    __typename
    claim_static_deposit_id: id
    claim_static_deposit_created_at: created_at
    claim_static_deposit_updated_at: updated_at
    claim_static_deposit_network: network
    claim_static_deposit_credit_amount: credit_amount {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
    }
    claim_static_deposit_max_fee: max_fee {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
    }
    claim_static_deposit_status: status
    claim_static_deposit_transaction_id: transaction_id
    claim_static_deposit_output_index: output_index
    claim_static_deposit_bitcoin_network: bitcoin_network
    claim_static_deposit_transfer_spark_id: transfer_spark_id
}`;

export const getClaimStaticDepositQuery = (
  id: string,
): Query<ClaimStaticDeposit> => {
  return {
    queryPayload: `
query GetClaimStaticDeposit($id: ID!) {
    entity(id: $id) {
        ... on ClaimStaticDeposit {
            ...ClaimStaticDepositFragment
        }
    }
}

${FRAGMENT}    
`,
    variables: { id },
    constructObject: (data: unknown) =>
      isObject(data) && "entity" in data && isObject(data.entity)
        ? ClaimStaticDepositFromJson(data.entity)
        : null,
  };
};

export default ClaimStaticDeposit;
