import { FRAGMENT as CompleteCoopExitOutputFragment } from "../objects/CompleteCoopExitOutput";

export const CompleteCoopExit = `
  mutation CompleteCoopExit(
    $user_outbound_transfer_external_id: String!
    $coop_exit_request_id: String!
  ) {
    complete_coop_exit(input: {
      user_outbound_transfer_external_id: $user_outbound_transfer_external_id
      coop_exit_request_id: $coop_exit_request_id
    }) {
      ...CompleteCoopExitOutputFragment
    }
  }
    
  ${CompleteCoopExitOutputFragment}
`;
