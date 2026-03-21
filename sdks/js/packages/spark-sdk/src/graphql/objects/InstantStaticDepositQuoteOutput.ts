// Copyright ©, 2025-present, Lightspark Group, Inc. - All Rights Reserved

import BitcoinNetwork from "./BitcoinNetwork.js";
import CurrencyAmount, { CurrencyAmountFromJson } from "./CurrencyAmount.js";
import InstantStaticDepositPlan, {
  InstantStaticDepositPlanFromJson,
} from "./InstantStaticDepositPlan.js";

interface StaticDepositQuote {
  id: string;
  network: BitcoinNetwork;
  transactionId: string;
  outputIndex: number;
  depositAmount: CurrencyAmount;
  creditAmount: CurrencyAmount;
  quoteSignature: string;
}

interface InstantStaticDepositQuoteOutput {
  quote: StaticDepositQuote;
  fulfillmentPlans: InstantStaticDepositPlan[];
}

export const StaticDepositQuoteFromJson = (obj: any): StaticDepositQuote => {
  return {
    id: obj["static_deposit_quote_id"],
    network:
      BitcoinNetwork[
        obj["static_deposit_quote_network"] as keyof typeof BitcoinNetwork
      ] ?? BitcoinNetwork.FUTURE_VALUE,
    transactionId: obj["static_deposit_quote_transaction_id"],
    outputIndex: obj["static_deposit_quote_output_index"],
    depositAmount: CurrencyAmountFromJson(
      obj["static_deposit_quote_deposit_amount"],
    ),
    creditAmount: CurrencyAmountFromJson(
      obj["static_deposit_quote_credit_amount"],
    ),
    quoteSignature: obj["static_deposit_quote_quote_signature"],
  } as StaticDepositQuote;
};

export const InstantStaticDepositQuoteOutputFromJson = (
  obj: any,
): InstantStaticDepositQuoteOutput => {
  return {
    quote: StaticDepositQuoteFromJson(obj["instant_quote_output_quote"]),
    fulfillmentPlans: (
      (obj["instant_quote_output_fulfillment_plans"] as any[]) ?? []
    ).map(InstantStaticDepositPlanFromJson),
  } as InstantStaticDepositQuoteOutput;
};

export const STATIC_DEPOSIT_QUOTE_FRAGMENT = `
fragment StaticDepositQuoteFragment on StaticDepositQuote {
    __typename
    static_deposit_quote_id: id
    static_deposit_quote_network: network
    static_deposit_quote_transaction_id: transaction_id
    static_deposit_quote_output_index: output_index
    static_deposit_quote_deposit_amount: deposit_amount {
        ...CurrencyAmountFragment
    }
    static_deposit_quote_credit_amount: credit_amount {
        ...CurrencyAmountFragment
    }
    static_deposit_quote_quote_signature: quote_signature
}`;

export const FRAGMENT = `
fragment InstantStaticDepositQuoteOutputFragment on CreateInstantStaticDepositQuoteOutput {
    __typename
    instant_quote_output_quote: quote {
        ...StaticDepositQuoteFragment
    }
    instant_quote_output_fulfillment_plans: fulfillment_plans {
        ...InstantStaticDepositPlanFragment
    }
}`;

export type { StaticDepositQuote };
export default InstantStaticDepositQuoteOutput;
