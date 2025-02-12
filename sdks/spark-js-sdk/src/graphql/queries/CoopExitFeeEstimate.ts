import { FRAGMENT as CoopExitFeeEstimateOutputFragment } from "../objects/CoopExitFeeEstimateOutput";

export const CoopExitFeeEstimate = `
  query CoopExitFeeEstimate(
    $leaf_external_ids: [UUID!]!
    $withdrawal_address: String!
  ) {
    coop_exit_fee_estimate(
      input: {
        leaf_external_ids: $leaf_external_ids
        withdrawal_address: $withdrawal_address
      }
    ) {
      ...CoopExitFeeEstimateOutputFragment
    }
  }
  ${CoopExitFeeEstimateOutputFragment}
`;
