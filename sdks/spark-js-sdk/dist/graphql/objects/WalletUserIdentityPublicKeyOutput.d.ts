interface WalletUserIdentityPublicKeyOutput {
    identityPublicKey: string;
}
export declare const WalletUserIdentityPublicKeyOutputFromJson: (obj: any) => WalletUserIdentityPublicKeyOutput;
export declare const WalletUserIdentityPublicKeyOutputToJson: (obj: WalletUserIdentityPublicKeyOutput) => any;
export declare const FRAGMENT = "\nfragment WalletUserIdentityPublicKeyOutputFragment on WalletUserIdentityPublicKeyOutput {\n    __typename\n    wallet_user_identity_public_key_output_identity_public_key: identity_public_key\n}";
export default WalletUserIdentityPublicKeyOutput;
