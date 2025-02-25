interface Provider {
    accountId: string;
    jwt: string;
}
export declare const ProviderFromJson: (obj: any) => Provider;
export declare const ProviderToJson: (obj: Provider) => any;
export default Provider;
