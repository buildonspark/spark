import { FRAGMENT as TransferFragment } from "../objects/Transfer";

export const RequestSwapLeaves = `
  mutation RequestSwapLeaves(
    $adaptorPubkey: String!
    $totalAmountSats: Int!
    $targetAmountSats: Int!
    $network: BitcoinNetwork!
  ) {
    request_leaves_swap(input: {
      adaptor_pubkey: $adaptorPubkey
      total_amount_sats: $totalAmountSats
      target_amount_sats: $targetAmountSats
      network: $network
    }) {
      ...RequestSwapLeavesOutputFragment
    }
  }
  ${TransferFragment}
`;
