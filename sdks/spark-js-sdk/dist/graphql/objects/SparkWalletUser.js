// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
import { isObject } from '@lightsparkdev/core';
export const SparkWalletUserFromJson = (obj) => {
    return {
        id: obj["spark_wallet_user_id"],
        createdAt: obj["spark_wallet_user_created_at"],
        updatedAt: obj["spark_wallet_user_updated_at"],
        identityPublicKey: obj["spark_wallet_user_identity_public_key"],
        typename: "SparkWalletUser",
    };
};
export const SparkWalletUserToJson = (obj) => {
    return {
        __typename: "SparkWalletUser", spark_wallet_user_id: obj.id,
        spark_wallet_user_created_at: obj.createdAt,
        spark_wallet_user_updated_at: obj.updatedAt,
        spark_wallet_user_identity_public_key: obj.identityPublicKey,
    };
};
export const FRAGMENT = `
fragment SparkWalletUserFragment on SparkWalletUser {
    __typename
    spark_wallet_user_id: id
    spark_wallet_user_created_at: created_at
    spark_wallet_user_updated_at: updated_at
    spark_wallet_user_identity_public_key: identity_public_key
}`;
export const getSparkWalletUserQuery = (id) => {
    return {
        queryPayload: `
query GetSparkWalletUser($id: ID!) {
    entity(id: $id) {
        ... on SparkWalletUser {
            ...SparkWalletUserFragment
        }
    }
}

${FRAGMENT}    
`,
        variables: { id },
        constructObject: (data) => isObject(data) && "entity" in data && isObject(data.entity) ? SparkWalletUserFromJson(data.entity) : null,
    };
};
//# sourceMappingURL=SparkWalletUser.js.map