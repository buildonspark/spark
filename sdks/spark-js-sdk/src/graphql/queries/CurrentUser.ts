import { FRAGMENT as WalletUserFragment } from "../objects/WalletUser.js";

export const CurrentUser = `
  query CurrentUser {
    current_user {
      ...WalletUserFragment
    }
  }
  ${WalletUserFragment}
`;
