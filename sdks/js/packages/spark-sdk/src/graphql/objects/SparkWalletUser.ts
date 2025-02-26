
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import Entity from './Entity.js';
import { Query, isObject } from '@lightsparkdev/core';


interface SparkWalletUser {


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

export const SparkWalletUserFromJson = (obj: any): SparkWalletUser => {
    return {
        id: obj["spark_wallet_user_id"],
        createdAt: obj["spark_wallet_user_created_at"],
        updatedAt: obj["spark_wallet_user_updated_at"],
        identityPublicKey: obj["spark_wallet_user_identity_public_key"],
typename: "SparkWalletUser",
        } as SparkWalletUser;

}
export const SparkWalletUserToJson = (obj: SparkWalletUser): any => {
return {
__typename: "SparkWalletUser",spark_wallet_user_id: obj.id,
spark_wallet_user_created_at: obj.createdAt,
spark_wallet_user_updated_at: obj.updatedAt,
spark_wallet_user_identity_public_key: obj.identityPublicKey,

        }

}


    export const FRAGMENT = `
fragment SparkWalletUserFragment on SparkWalletUser {
    __typename
    spark_wallet_user_id: id
    spark_wallet_user_created_at: created_at
    spark_wallet_user_updated_at: updated_at
    spark_wallet_user_identity_public_key: identity_public_key
}`;



    export const getSparkWalletUserQuery = (id: string): Query<SparkWalletUser> => {
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
            variables: {id},
            constructObject: (data: unknown) => isObject(data) && "entity" in data && isObject(data.entity) ? SparkWalletUserFromJson(data.entity) : null,
        }
    }


export default SparkWalletUser;
