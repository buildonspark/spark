import { FRAGMENT as LightningReceiveFeeEstimateOutputFragment } from "../objects/LightningReceiveFeeEstimateOutput.js";

export const LightningReceiveFeeEstimate = `
  query LightningReceiveFeeEstimate(
    $network: BitcoinNetwork!
    $amount_sats: Long!
  ) {
    lightning_receive_fee_estimate(
      input: {
        network: $network
        amount_sats: $amount_sats
      }
    ) {
      ...LightningReceiveFeeEstimateOutputFragment
    }
  }
  ${LightningReceiveFeeEstimateOutputFragment}
`;
