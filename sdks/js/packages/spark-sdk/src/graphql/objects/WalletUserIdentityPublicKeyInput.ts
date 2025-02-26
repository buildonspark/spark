
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface WalletUserIdentityPublicKeyInput {


    phoneNumber: string;




}

export const WalletUserIdentityPublicKeyInputFromJson = (obj: any): WalletUserIdentityPublicKeyInput => {
    return {
        phoneNumber: obj["wallet_user_identity_public_key_input_phone_number"],

        } as WalletUserIdentityPublicKeyInput;

}
export const WalletUserIdentityPublicKeyInputToJson = (obj: WalletUserIdentityPublicKeyInput): any => {
return {
wallet_user_identity_public_key_input_phone_number: obj.phoneNumber,

        }

}





export default WalletUserIdentityPublicKeyInput;
