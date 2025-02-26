import { FRAGMENT as CompleteCoopExitOutputFragment } from "../objects/CoopExitRequest.js";

export const CompleteCoopExit = `
  mutation CompleteCoopExit(
    $user_outbound_transfer_external_id: UUID!
    $coop_exit_request_id: ID!
  ) {
    complete_coop_exit(input: {
      user_outbound_transfer_external_id: $user_outbound_transfer_external_id
      coop_exit_request_id: $coop_exit_request_id
    }) {
      request {
        ...CoopExitRequestFragment
      }
    }
  }
    
  ${CompleteCoopExitOutputFragment}
`;
