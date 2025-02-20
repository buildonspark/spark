// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import { Query, isObject } from "@lightsparkdev/core";

interface WalletUser {
  /**
   * The unique identifier of this entity across all Lightspark systems. Should be treated as an opaque
   * string.
   **/
  id: string;

  /** The date and time when the entity was first created. **/
  createdAt: string;

  /** The date and time when the entity was last updated. **/
  updatedAt: string;

  /** The identity public key of the user. **/
  identityPublicKey: string;

  /** The typename of the object **/
  typename: string;
}

export const WalletUserFromJson = (obj: any): WalletUser => {
  return {
    id: obj["wallet_user_id"],
    createdAt: obj["wallet_user_created_at"],
    updatedAt: obj["wallet_user_updated_at"],
    identityPublicKey: obj["wallet_user_identity_public_key"],
    typename: "WalletUser",
  } as WalletUser;
};
export const WalletUserToJson = (obj: WalletUser): any => {
  return {
    __typename: "WalletUser",
    wallet_user_id: obj.id,
    wallet_user_created_at: obj.createdAt,
    wallet_user_updated_at: obj.updatedAt,
    wallet_user_identity_public_key: obj.identityPublicKey,
  };
};

export const FRAGMENT = `
fragment WalletUserFragment on WalletUser {
    __typename
    wallet_user_id: id
    wallet_user_created_at: created_at
    wallet_user_updated_at: updated_at
    wallet_user_identity_public_key: identity_public_key
}`;

export const getWalletUserQuery = (id: string): Query<WalletUser> => {
  return {
    queryPayload: `
query GetWalletUser($id: ID!) {
    entity(id: $id) {
        ... on WalletUser {
            ...WalletUserFragment
        }
    }
}

${FRAGMENT}    
`,
    variables: { id },
    constructObject: (data: unknown) =>
      isObject(data) && "entity" in data && isObject(data.entity)
        ? WalletUserFromJson(data.entity)
        : null,
  };
};

export default WalletUser;
