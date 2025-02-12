// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import BitcoinNetwork from "./BitcoinNetwork.js";

interface RequestLeavesSwapInput {
  adaptorPubkey: string;

  totalAmountSats: number;

  targetAmountSats: number;

  network: BitcoinNetwork;
}

export const RequestLeavesSwapInputFromJson = (
  obj: any
): RequestLeavesSwapInput => {
  return {
    adaptorPubkey: obj["request_leaves_swap_input_adaptor_pubkey"],
    totalAmountSats: obj["request_leaves_swap_input_total_amount_sats"],
    targetAmountSats: obj["request_leaves_swap_input_target_amount_sats"],
    network:
      BitcoinNetwork[
        obj["request_leaves_swap_input_network"] as keyof typeof BitcoinNetwork
      ] ?? BitcoinNetwork.FUTURE_VALUE,
  } as RequestLeavesSwapInput;
};
export const RequestLeavesSwapInputToJson = (
  obj: RequestLeavesSwapInput
): any => {
  return {
    request_leaves_swap_input_adaptor_pubkey: obj.adaptorPubkey,
    request_leaves_swap_input_total_amount_sats: obj.totalAmountSats,
    request_leaves_swap_input_target_amount_sats: obj.targetAmountSats,
    request_leaves_swap_input_network: obj.network,
  };
};

export default RequestLeavesSwapInput;
