// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import { Query, isObject } from "@lightsparkdev/core";
import BitcoinNetwork from "./BitcoinNetwork.js";
import CurrencyAmount, {
  CurrencyAmountFromJson,
  CurrencyAmountToJson,
} from "./CurrencyAmount.js";

interface CoopExitFeeQuote {
  /**
   * The unique identifier of this entity across all Lightspark systems. Should be treated as an opaque
   * string.
   **/
  id: string;

  /** The date and time when the entity was first created. **/
  createdAt: string;

  /** The date and time when the entity was last updated. **/
  updatedAt: string;

  /** The network the coop exit fee quote is on. **/
  network: BitcoinNetwork;

  /** The total currency amount of all the nodes user swapped for the coop exit quote. **/
  totalAmount: CurrencyAmount;

  /** The fee user pays for the coop exit not including the L1 broadcast fee when exit speed is fast. **/
  userFeeFast: CurrencyAmount;

  /** The fee user pays for the coop exit not including the L1 broadcast fee when exit speed is medium. **/
  userFeeMedium: CurrencyAmount;

  /** The fee user pays for the coop exit not including the L1 broadcast fee when exit speed is slow. **/
  userFeeSlow: CurrencyAmount;

  /** The L1 broadcast fee user pays for the coop exit when exit speed is fast. **/
  l1BroadcastFeeFast: CurrencyAmount;

  /** The L1 broadcast fee user pays for the coop exit when exit speed is medium. **/
  l1BroadcastFeeMedium: CurrencyAmount;

  /** The L1 broadcast fee user pays for the coop exit when exit speed is slow. **/
  l1BroadcastFeeSlow: CurrencyAmount;

  /** The time when the coop exit fee quote expires. **/
  expiresAt: string;

  /** The typename of the object **/
  typename: string;
}

export const CoopExitFeeQuoteFromJson = (obj: any): CoopExitFeeQuote => {
  return {
    id: obj["coop_exit_fee_quote_id"],
    createdAt: obj["coop_exit_fee_quote_created_at"],
    updatedAt: obj["coop_exit_fee_quote_updated_at"],
    network:
      BitcoinNetwork[obj["coop_exit_fee_quote_network"]] ??
      BitcoinNetwork.FUTURE_VALUE,
    totalAmount: CurrencyAmountFromJson(
      obj["coop_exit_fee_quote_total_amount"],
    ),
    userFeeFast: CurrencyAmountFromJson(
      obj["coop_exit_fee_quote_user_fee_fast"],
    ),
    userFeeMedium: CurrencyAmountFromJson(
      obj["coop_exit_fee_quote_user_fee_medium"],
    ),
    userFeeSlow: CurrencyAmountFromJson(
      obj["coop_exit_fee_quote_user_fee_slow"],
    ),
    l1BroadcastFeeFast: CurrencyAmountFromJson(
      obj["coop_exit_fee_quote_l1_broadcast_fee_fast"],
    ),
    l1BroadcastFeeMedium: CurrencyAmountFromJson(
      obj["coop_exit_fee_quote_l1_broadcast_fee_medium"],
    ),
    l1BroadcastFeeSlow: CurrencyAmountFromJson(
      obj["coop_exit_fee_quote_l1_broadcast_fee_slow"],
    ),
    expiresAt: obj["coop_exit_fee_quote_expires_at"],
    typename: "CoopExitFeeQuote",
  } as CoopExitFeeQuote;
};
export const CoopExitFeeQuoteToJson = (obj: CoopExitFeeQuote): any => {
  return {
    __typename: "CoopExitFeeQuote",
    coop_exit_fee_quote_id: obj.id,
    coop_exit_fee_quote_created_at: obj.createdAt,
    coop_exit_fee_quote_updated_at: obj.updatedAt,
    coop_exit_fee_quote_network: obj.network,
    coop_exit_fee_quote_total_amount: CurrencyAmountToJson(obj.totalAmount),
    coop_exit_fee_quote_user_fee_fast: CurrencyAmountToJson(obj.userFeeFast),
    coop_exit_fee_quote_user_fee_medium: CurrencyAmountToJson(
      obj.userFeeMedium,
    ),
    coop_exit_fee_quote_user_fee_slow: CurrencyAmountToJson(obj.userFeeSlow),
    coop_exit_fee_quote_l1_broadcast_fee_fast: CurrencyAmountToJson(
      obj.l1BroadcastFeeFast,
    ),
    coop_exit_fee_quote_l1_broadcast_fee_medium: CurrencyAmountToJson(
      obj.l1BroadcastFeeMedium,
    ),
    coop_exit_fee_quote_l1_broadcast_fee_slow: CurrencyAmountToJson(
      obj.l1BroadcastFeeSlow,
    ),
    coop_exit_fee_quote_expires_at: obj.expiresAt,
  };
};

export const FRAGMENT = `
fragment CoopExitFeeQuoteFragment on CoopExitFeeQuote {
    __typename
    coop_exit_fee_quote_id: id
    coop_exit_fee_quote_created_at: created_at
    coop_exit_fee_quote_updated_at: updated_at
    coop_exit_fee_quote_network: network
    coop_exit_fee_quote_total_amount: total_amount {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
    }
    coop_exit_fee_quote_user_fee_fast: user_fee_fast {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
    }
    coop_exit_fee_quote_user_fee_medium: user_fee_medium {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
    }
    coop_exit_fee_quote_user_fee_slow: user_fee_slow {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
    }
    coop_exit_fee_quote_l1_broadcast_fee_fast: l1_broadcast_fee_fast {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
    }
    coop_exit_fee_quote_l1_broadcast_fee_medium: l1_broadcast_fee_medium {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
    }
    coop_exit_fee_quote_l1_broadcast_fee_slow: l1_broadcast_fee_slow {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
    }
    coop_exit_fee_quote_expires_at: expires_at
}`;

export const getCoopExitFeeQuoteQuery = (
  id: string,
): Query<CoopExitFeeQuote> => {
  return {
    queryPayload: `
query GetCoopExitFeeQuote($id: ID!) {
    entity(id: $id) {
        ... on CoopExitFeeQuote {
            ...CoopExitFeeQuoteFragment
        }
    }
}

${FRAGMENT}    
`,
    variables: { id },
    constructObject: (data: unknown) =>
      isObject(data) && "entity" in data && isObject(data.entity)
        ? CoopExitFeeQuoteFromJson(data.entity)
        : null,
  };
};

export default CoopExitFeeQuote;
