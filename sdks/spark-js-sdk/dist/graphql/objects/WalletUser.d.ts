import { Query } from "@lightsparkdev/core";
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
export declare const WalletUserFromJson: (obj: any) => WalletUser;
export declare const WalletUserToJson: (obj: WalletUser) => any;
export declare const FRAGMENT = "\nfragment WalletUserFragment on WalletUser {\n    __typename\n    wallet_user_id: id\n    wallet_user_created_at: created_at\n    wallet_user_updated_at: updated_at\n    wallet_user_identity_public_key: identity_public_key\n}";
export declare const getWalletUserQuery: (id: string) => Query<WalletUser>;
export default WalletUser;
