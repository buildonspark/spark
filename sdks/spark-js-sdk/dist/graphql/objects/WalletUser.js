// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
import { isObject } from "@lightsparkdev/core";
export const WalletUserFromJson = (obj) => {
    return {
        id: obj["wallet_user_id"],
        createdAt: obj["wallet_user_created_at"],
        updatedAt: obj["wallet_user_updated_at"],
        identityPublicKey: obj["wallet_user_identity_public_key"],
        typename: "WalletUser",
    };
};
export const WalletUserToJson = (obj) => {
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
export const getWalletUserQuery = (id) => {
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
        constructObject: (data) => isObject(data) && "entity" in data && isObject(data.entity)
            ? WalletUserFromJson(data.entity)
            : null,
    };
};
//# sourceMappingURL=WalletUser.js.map