interface RequestLeavesSwapOutput {
    requestId: string;
}
export declare const RequestLeavesSwapOutputFromJson: (obj: any) => RequestLeavesSwapOutput;
export declare const RequestLeavesSwapOutputToJson: (obj: RequestLeavesSwapOutput) => any;
export declare const FRAGMENT = "\nfragment RequestLeavesSwapOutputFragment on RequestLeavesSwapOutput {\n    __typename\n    request_leaves_swap_output_request: request {\n        id\n    }\n}";
export default RequestLeavesSwapOutput;
