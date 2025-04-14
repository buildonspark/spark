
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface Provider {


    accountId: string;

    jwt: string;




}

export const ProviderFromJson = (obj: any): Provider => {
    return {
        accountId: obj["provider_account_id"],
        jwt: obj["provider_jwt"],

        } as Provider;

}
export const ProviderToJson = (obj: Provider): any => {
return {
provider_account_id: obj.accountId,
provider_jwt: obj.jwt,

        }

}





export default Provider;
