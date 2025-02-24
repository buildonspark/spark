import { FRAGMENT as RequestCoopExitOutputFragment } from "../objects/CoopExitRequest.js";
export const RequestCoopExit = `
  mutation RequestCoopExit(
    $leaf_external_ids: [UUID!]!
    $withdrawal_address: String!
  ) {
    request_coop_exit(
      input: {
        leaf_external_ids: $leaf_external_ids
        withdrawal_address: $withdrawal_address
      }
    ) {
      request {
        ...CoopExitRequestFragment
      }
    }
  }
  ${RequestCoopExitOutputFragment}
`;
//# sourceMappingURL=RequestCoopExit.js.map