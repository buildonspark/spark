import { FRAGMENT as LeavesSwapRequestFragment } from "../objects/LeavesSwapRequest.js";

export const CompleteLeavesSwap = `
  mutation CompleteLeavesSwap(
    $adaptor_secret_key: String!
    $user_outbound_transfer_external_id: UUID!
    $leaves_swap_request_id: ID!
  ) {
    complete_leaves_swap(input: { adaptor_secret_key: $adaptor_secret_key, user_outbound_transfer_external_id: $user_outbound_transfer_external_id, leaves_swap_request_id: $leaves_swap_request_id }) {
      request {
        ...LeavesSwapRequestFragment
      }
    }
  }

  ${LeavesSwapRequestFragment}
`;
