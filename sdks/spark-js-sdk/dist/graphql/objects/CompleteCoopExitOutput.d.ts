interface CompleteCoopExitOutput {
    requestId: string;
}
export declare const CompleteCoopExitOutputFromJson: (obj: any) => CompleteCoopExitOutput;
export declare const CompleteCoopExitOutputToJson: (obj: CompleteCoopExitOutput) => any;
export declare const FRAGMENT = "\nfragment CompleteCoopExitOutputFragment on CompleteCoopExitOutput {\n    __typename\n    complete_coop_exit_output_request: request {\n        id\n    }\n}";
export default CompleteCoopExitOutput;
