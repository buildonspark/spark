import { FRAGMENT as InstantStaticDepositClaimOutputFragment } from "../objects/InstantStaticDepositClaimOutput.js";

export const ClaimInstantStaticDeposit = `
  mutation ClaimInstantStaticDeposit(
    $static_deposit_quote_id: ID!
    $static_deposit_address_private_key_share: String!
    $signature: String!
  ) {
    create_claim_instant_static_deposit(input: {
      static_deposit_quote_id: $static_deposit_quote_id,
      static_deposit_address_private_key_share: $static_deposit_address_private_key_share,
      signature: $signature
    }) {
      ...InstantStaticDepositClaimOutputFragment
    }
  }
  ${InstantStaticDepositClaimOutputFragment}
`;
