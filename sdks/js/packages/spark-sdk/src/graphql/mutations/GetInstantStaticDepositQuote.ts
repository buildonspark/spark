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
    $partner_id: String
  ) {
    create_instant_static_deposit_quote(input: {
      transaction_id: $transaction_id,
      output_index: $output_index,
      network: $network,
      partner_id: $partner_id
    }) {
      ...InstantStaticDepositQuoteOutputFragment
    }
  }
  ${InstantStaticDepositQuoteOutputFragment}
  ${StaticDepositQuoteFragment}
  ${InstantStaticDepositPlanFragment}
  ${CurrencyAmountFragment}
`;
