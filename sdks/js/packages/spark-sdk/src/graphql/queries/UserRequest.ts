import { FRAGMENT as UserRequestFragment } from "../objects/UserRequest.js";

export const UserRequest = `
  query UserRequest($request_id: ID!) {
    user_request(request_id: $request_id) {
      ...UserRequestFragment
    }
  }
  ${UserRequestFragment}
`;
