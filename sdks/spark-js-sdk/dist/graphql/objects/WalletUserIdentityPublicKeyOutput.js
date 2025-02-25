// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const WalletUserIdentityPublicKeyOutputFromJson = (obj) => {
    return {
        identityPublicKey: obj["wallet_user_identity_public_key_output_identity_public_key"],
    };
};
export const WalletUserIdentityPublicKeyOutputToJson = (obj) => {
    return {
        wallet_user_identity_public_key_output_identity_public_key: obj.identityPublicKey,
    };
};
export const FRAGMENT = `
fragment WalletUserIdentityPublicKeyOutputFragment on WalletUserIdentityPublicKeyOutput {
    __typename
    wallet_user_identity_public_key_output_identity_public_key: identity_public_key
}`;
//# sourceMappingURL=WalletUserIdentityPublicKeyOutput.js.map