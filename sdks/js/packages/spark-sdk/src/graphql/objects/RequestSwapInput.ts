// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import UserLeafInput, {
  UserLeafInputFromJson,
  UserLeafInputToJson,
} from "./UserLeafInput.js";

interface RequestSwapInput {
  adaptorPubkey: string;

  totalAmountSats: number;

  targetAmountSats: number[];

  feeSats: number;

  userLeaves: UserLeafInput[];

  userOutboundTransferExternalId: string;
}

export const RequestSwapInputFromJson = (obj: any): RequestSwapInput => {
  return {
    adaptorPubkey: obj["request_swap_input_adaptor_pubkey"],
    totalAmountSats: obj["request_swap_input_total_amount_sats"],
    targetAmountSats: obj["request_swap_input_target_amount_sats"],
    feeSats: obj["request_swap_input_fee_sats"],
    userLeaves: obj["request_swap_input_user_leaves"].map((e) =>
      UserLeafInputFromJson(e),
    ),
    userOutboundTransferExternalId:
      obj["request_swap_input_user_outbound_transfer_external_id"],
  } as RequestSwapInput;
};
export const RequestSwapInputToJson = (obj: RequestSwapInput): any => {
  return {
    request_swap_input_adaptor_pubkey: obj.adaptorPubkey,
    request_swap_input_total_amount_sats: obj.totalAmountSats,
    request_swap_input_target_amount_sats: obj.targetAmountSats,
    request_swap_input_fee_sats: obj.feeSats,
    request_swap_input_user_leaves: obj.userLeaves.map((e) =>
      UserLeafInputToJson(e),
    ),
    request_swap_input_user_outbound_transfer_external_id:
      obj.userOutboundTransferExternalId,
  };
};

export default RequestSwapInput;
