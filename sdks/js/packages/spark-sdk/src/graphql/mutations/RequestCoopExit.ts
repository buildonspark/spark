import { FRAGMENT as RequestCoopExitOutputFragment } from "../objects/CoopExitRequest.js";

export const RequestCoopExit = `
  mutation RequestCoopExit(
    $leaf_external_ids: [UUID!]!
    $withdrawal_address: String!
    $idempotency_key: String!
  ) {
    request_coop_exit(
      input: {
        leaf_external_ids: $leaf_external_ids
        withdrawal_address: $withdrawal_address
        idempotency_key: $idempotency_key
      }
    ) {
      request {
        ...CoopExitRequestFragment
      }
    }
  }
  ${RequestCoopExitOutputFragment}
`;
