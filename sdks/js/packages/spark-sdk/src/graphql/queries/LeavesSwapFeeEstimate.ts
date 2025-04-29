import { FRAGMENT as LeavesSwapFeeEstimateOutputFragment } from "../objects/LeavesSwapFeeEstimateOutput.js";

export const LeavesSwapFeeEstimate = `
  query LeavesSwapFeeEstimate(
    $total_amount_sats: Int!
  ) {
    leaves_swap_fee_estimate(
      input: {
        total_amount_sats: $total_amount_sats
      }
    ) {
      ...LeavesSwapFeeEstimateOutputFragment
    }
  }
  ${LeavesSwapFeeEstimateOutputFragment}
`;
