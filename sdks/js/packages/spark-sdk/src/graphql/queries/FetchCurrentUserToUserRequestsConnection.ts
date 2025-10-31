import { FRAGMENT as UserRequestFragment } from "../objects/UserRequest.js";

export const FetchCurrentUserToUserRequestsConnection = `
  query FetchCurrentUserToUserRequestsConnection($first: Int, $after: String, $types: [SparkUserRequestType!], $statuses: [SparkUserRequestStatus!], $networks: [BitcoinNetwork!]) {
    current_user {
      ... on SparkWalletUser {
        user_requests(first: $first, after: $after, types: $types, statuses: $statuses, networks: $networks) {
          __typename
          spark_wallet_user_to_user_requests_connection_count: count
          spark_wallet_user_to_user_requests_connection_page_info: page_info {
            __typename
            page_info_has_next_page: has_next_page
            page_info_has_previous_page: has_previous_page
            page_info_start_cursor: start_cursor
            page_info_end_cursor: end_cursor
          }
          spark_wallet_user_to_user_requests_connection_entities: entities {
            ...UserRequestFragment
          }
        }
      }
    }
  }
  ${UserRequestFragment}
`;
