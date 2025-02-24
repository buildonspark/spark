interface RequestCoopExitOutput {
    requestId: string;
}
export declare const RequestCoopExitOutputFromJson: (obj: any) => RequestCoopExitOutput;
export declare const RequestCoopExitOutputToJson: (obj: RequestCoopExitOutput) => any;
export declare const FRAGMENT = "\nfragment RequestCoopExitOutputFragment on RequestCoopExitOutput {\n    __typename\n    request_coop_exit_output_request: request {\n        id\n    }\n}";
export default RequestCoopExitOutput;
