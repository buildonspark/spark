// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import ExitSpeed from "./ExitSpeed.js";

interface RequestCoopExitInput {
  leafExternalIds: string[];

  withdrawalAddress: string;

  idempotencyKey?: string | undefined;

  exitSpeed: ExitSpeed;

  withdrawAll: boolean;

  feeLeafExternalIds?: string[] | undefined;

  feeQuoteId?: string | undefined;

  userOutboundTransferExternalId?: string | undefined;
}

export const RequestCoopExitInputFromJson = (
  obj: any,
): RequestCoopExitInput => {
  return {
    leafExternalIds: obj["request_coop_exit_input_leaf_external_ids"],
    withdrawalAddress: obj["request_coop_exit_input_withdrawal_address"],
    idempotencyKey: obj["request_coop_exit_input_idempotency_key"] ?? undefined,
    exitSpeed:
      ExitSpeed[obj["request_coop_exit_input_exit_speed"]] ??
      ExitSpeed.FUTURE_VALUE,
    withdrawAll: obj["request_coop_exit_input_withdraw_all"],
    feeLeafExternalIds: obj["request_coop_exit_input_fee_leaf_external_ids"],
    feeQuoteId: obj["request_coop_exit_input_fee_quote_id"],
    userOutboundTransferExternalId:
      obj["request_coop_exit_input_user_outbound_transfer_external_id"] ??
      undefined,
  } as RequestCoopExitInput;
};
export const RequestCoopExitInputToJson = (obj: RequestCoopExitInput): any => {
  return {
    request_coop_exit_input_leaf_external_ids: obj.leafExternalIds,
    request_coop_exit_input_withdrawal_address: obj.withdrawalAddress,
    request_coop_exit_input_idempotency_key: obj.idempotencyKey ?? undefined,
    request_coop_exit_input_exit_speed: obj.exitSpeed,
    request_coop_exit_input_withdraw_all: obj.withdrawAll,
    request_coop_exit_input_fee_leaf_external_ids: obj.feeLeafExternalIds,
    request_coop_exit_input_fee_quote_id: obj.feeQuoteId,
    request_coop_exit_input_user_outbound_transfer_external_id:
      obj.userOutboundTransferExternalId ?? undefined,
  };
};

export default RequestCoopExitInput;
