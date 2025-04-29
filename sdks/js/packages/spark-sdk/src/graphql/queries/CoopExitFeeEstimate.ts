import { FRAGMENT as CoopExitFeeEstimatesOutputFragment } from "../objects/CoopExitFeeEstimatesOutput.js";

export const CoopExitFeeEstimate = `
  query CoopExitFeeEstimate(
    $leaf_external_ids: [UUID!]!
    $withdrawal_address: String!
  ) {
    coop_exit_fee_estimates(
      input: {
        leaf_external_ids: $leaf_external_ids
        withdrawal_address: $withdrawal_address
      }
    ) {
      ...CoopExitFeeEstimatesOutputFragment
    }
  }
  ${CoopExitFeeEstimatesOutputFragment}
`;
