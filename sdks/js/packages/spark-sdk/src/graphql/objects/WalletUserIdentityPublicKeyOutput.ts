
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface WalletUserIdentityPublicKeyOutput {


    identityPublicKey: string;




}

export const WalletUserIdentityPublicKeyOutputFromJson = (obj: any): WalletUserIdentityPublicKeyOutput => {
    return {
        identityPublicKey: obj["wallet_user_identity_public_key_output_identity_public_key"],

        } as WalletUserIdentityPublicKeyOutput;

}
export const WalletUserIdentityPublicKeyOutputToJson = (obj: WalletUserIdentityPublicKeyOutput): any => {
return {
wallet_user_identity_public_key_output_identity_public_key: obj.identityPublicKey,

        }

}


    export const FRAGMENT = `
fragment WalletUserIdentityPublicKeyOutputFragment on WalletUserIdentityPublicKeyOutput {
    __typename
    wallet_user_identity_public_key_output_identity_public_key: identity_public_key
}`;




export default WalletUserIdentityPublicKeyOutput;
