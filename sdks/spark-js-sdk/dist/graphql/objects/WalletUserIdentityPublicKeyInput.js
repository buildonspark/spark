// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const WalletUserIdentityPublicKeyInputFromJson = (obj) => {
    return {
        phoneNumber: obj["wallet_user_identity_public_key_input_phone_number"],
    };
};
export const WalletUserIdentityPublicKeyInputToJson = (obj) => {
    return {
        wallet_user_identity_public_key_input_phone_number: obj.phoneNumber,
    };
};
//# sourceMappingURL=WalletUserIdentityPublicKeyInput.js.map