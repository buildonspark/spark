import { FRAGMENT as CurrencyAmountFragment } from "../objects/CurrencyAmount.js";
import { FRAGMENT as InstantStaticDepositPlanFragment } from "../objects/InstantStaticDepositPlan.js";
import {
  FRAGMENT as InstantStaticDepositQuoteOutputFragment,
  STATIC_DEPOSIT_QUOTE_FRAGMENT as StaticDepositQuoteFragment,
} from "../objects/InstantStaticDepositQuoteOutput.js";

export const GetInstantStaticDepositQuote = `
  mutation CreateInstantStaticDepositQuote(
    $transaction_id: String!
    $output_index: Int!
    $network: BitcoinNetwork!
  ) {
    create_instant_static_deposit_quote(input: {
      transaction_id: $transaction_id,
      output_index: $output_index,
      network: $network
    }) {
      ...InstantStaticDepositQuoteOutputFragment
    }
  }
  ${InstantStaticDepositQuoteOutputFragment}
  ${StaticDepositQuoteFragment}
  ${InstantStaticDepositPlanFragment}
  ${CurrencyAmountFragment}
`;
