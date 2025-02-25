import { Query } from '@lightsparkdev/core';
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
export declare const SparkWalletUserFromJson: (obj: any) => SparkWalletUser;
export declare const SparkWalletUserToJson: (obj: SparkWalletUser) => any;
export declare const FRAGMENT = "\nfragment SparkWalletUserFragment on SparkWalletUser {\n    __typename\n    spark_wallet_user_id: id\n    spark_wallet_user_created_at: created_at\n    spark_wallet_user_updated_at: updated_at\n    spark_wallet_user_identity_public_key: identity_public_key\n}";
export declare const getSparkWalletUserQuery: (id: string) => Query<SparkWalletUser>;
export default SparkWalletUser;
