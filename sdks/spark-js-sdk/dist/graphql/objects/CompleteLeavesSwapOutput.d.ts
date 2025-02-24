interface CompleteLeavesSwapOutput {
    requestId: string;
}
export declare const CompleteLeavesSwapOutputFromJson: (obj: any) => CompleteLeavesSwapOutput;
export declare const CompleteLeavesSwapOutputToJson: (obj: CompleteLeavesSwapOutput) => any;
export declare const FRAGMENT = "\nfragment CompleteLeavesSwapOutputFragment on CompleteLeavesSwapOutput {\n    __typename\n    complete_leaves_swap_output_request: request {\n        id\n    }\n}";
export default CompleteLeavesSwapOutput;
