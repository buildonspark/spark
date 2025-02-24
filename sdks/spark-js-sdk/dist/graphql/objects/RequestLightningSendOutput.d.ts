interface RequestLightningSendOutput {
    requestId: string;
}
export declare const RequestLightningSendOutputFromJson: (obj: any) => RequestLightningSendOutput;
export declare const RequestLightningSendOutputToJson: (obj: RequestLightningSendOutput) => any;
export declare const FRAGMENT = "\nfragment RequestLightningSendOutputFragment on RequestLightningSendOutput {\n    __typename\n    request_lightning_send_output_request: request {\n        id\n    }\n}";
export default RequestLightningSendOutput;
