import { FRAGMENT as RequestCoopExitOutputFragment } from "../objects/CoopExitRequest.js";

export const RequestCoopExit = `
  mutation RequestCoopExit(
    $leaf_external_ids: [UUID!]!
    $withdrawal_address: String!
    $idempotency_key: String!
    $exit_speed: ExitSpeed!
  ) {
    request_coop_exit(
      input: {
        leaf_external_ids: $leaf_external_ids
        withdrawal_address: $withdrawal_address
        idempotency_key: $idempotency_key
        exit_speed: $exit_speed
      }
    ) {
      request {
        ...CoopExitRequestFragment
      }
    }
  }
  ${RequestCoopExitOutputFragment}
`;
