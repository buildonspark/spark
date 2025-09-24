import { FRAGMENT as RequestCoopExitOutputFragment } from "../objects/CoopExitRequest.js";

export const RequestCoopExit = `
  mutation RequestCoopExit(
    $leaf_external_ids: [UUID!]!
    $withdrawal_address: String!
    $idempotency_key: String
    $exit_speed: ExitSpeed!
    $withdraw_all: Boolean
    $fee_leaf_external_ids: [UUID!]
    $fee_quote_id: ID
    $user_outbound_transfer_external_id: UUID
  ) {
    request_coop_exit(
      input: {
        leaf_external_ids: $leaf_external_ids
        withdrawal_address: $withdrawal_address
        idempotency_key: $idempotency_key
        exit_speed: $exit_speed
        withdraw_all: $withdraw_all
        fee_leaf_external_ids: $fee_leaf_external_ids
        fee_quote_id: $fee_quote_id
        user_outbound_transfer_external_id: $user_outbound_transfer_external_id
      }
    ) {
      request {
        ...CoopExitRequestFragment
      }
    }
  }
  ${RequestCoopExitOutputFragment}
`;
