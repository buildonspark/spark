// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const ProviderFromJson = (obj) => {
    return {
        accountId: obj["provider_account_id"],
        jwt: obj["provider_jwt"],
    };
};
export const ProviderToJson = (obj) => {
    return {
        provider_account_id: obj.accountId,
        provider_jwt: obj.jwt,
    };
};
//# sourceMappingURL=Provider.js.map