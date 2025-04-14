import { FRAGMENT as SparkWalletUserFragment } from "../objects/SparkWalletUser.js";

export const CurrentUser = `
  query CurrentUser {
    current_user {
      ...SparkWalletUserFragment
    }
  }
  ${SparkWalletUserFragment}
`;
