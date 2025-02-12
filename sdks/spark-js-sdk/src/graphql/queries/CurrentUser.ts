import { FRAGMENT as WalletUserFragment } from "../objects/WalletUser";

export const CurrentUser = `
  query CurrentUser {
    current_user {
      ...WalletUserFragment
    }
  }
  ${WalletUserFragment}
`;
