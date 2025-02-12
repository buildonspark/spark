// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import BitcoinNetwork from "./BitcoinNetwork.js";

interface LightningReceiveFeeEstimateInput {
  network: BitcoinNetwork;

  amountSats: number;
}

export const LightningReceiveFeeEstimateInputFromJson = (
  obj: any
): LightningReceiveFeeEstimateInput => {
  return {
    network:
      BitcoinNetwork[
        obj[
          "lightning_receive_fee_estimate_input_network"
        ] as keyof typeof BitcoinNetwork
      ] ?? BitcoinNetwork.FUTURE_VALUE,
    amountSats: obj["lightning_receive_fee_estimate_input_amount_sats"],
  } as LightningReceiveFeeEstimateInput;
};
export const LightningReceiveFeeEstimateInputToJson = (
  obj: LightningReceiveFeeEstimateInput
): any => {
  return {
    lightning_receive_fee_estimate_input_network: obj.network,
    lightning_receive_fee_estimate_input_amount_sats: obj.amountSats,
  };
};

export default LightningReceiveFeeEstimateInput;
