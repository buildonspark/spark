interface RequestLightningReceiveOutput {
    requestId: string;
}
export declare const RequestLightningReceiveOutputFromJson: (obj: any) => RequestLightningReceiveOutput;
export declare const RequestLightningReceiveOutputToJson: (obj: RequestLightningReceiveOutput) => any;
export declare const FRAGMENT = "\nfragment RequestLightningReceiveOutputFragment on RequestLightningReceiveOutput {\n    __typename\n    request_lightning_receive_output_request: request {\n        id\n    }\n}";
export default RequestLightningReceiveOutput;
