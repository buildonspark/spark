import { FRAGMENT as LightningSendFeeEstimateOutputFragment } from "../objects/LightningSendFeeEstimateOutput";

export const LightningSendFeeEstimate = `
  query LightningSendFeeEstimate(
    $encoded_invoice: String!
  ) {
    lightning_send_fee_estimate(
      input: {
        encoded_invoice: $encoded_invoice
      }
    ) {
      ...LightningSendFeeEstimateOutputFragment
    }
  }
  ${LightningSendFeeEstimateOutputFragment}
`;
