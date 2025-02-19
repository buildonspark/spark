import { FRAGMENT as LeavesSwapRequestFragment } from "../objects/LeavesSwapRequest";

export const RequestSwapLeaves = `
  mutation RequestSwapLeaves(
    $adaptor_pubkey: String!
    $total_amount_sats: Int!
    $target_amount_sats: Int!
    $fee_sats: Int!
    $network: BitcoinNetwork!
  ) {
    request_leaves_swap(input: {
      adaptor_pubkey: $adaptor_pubkey
      total_amount_sats: $total_amount_sats
      target_amount_sats: $target_amount_sats
      fee_sats: $fee_sats
      network: $network
    }) {
      request {
        ...LeavesSwapRequestFragment
      }
    }
  }
  ${LeavesSwapRequestFragment}
`;
