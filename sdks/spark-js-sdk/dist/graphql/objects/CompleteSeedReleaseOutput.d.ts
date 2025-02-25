interface CompleteSeedReleaseOutput {
    seed: string;
}
export declare const CompleteSeedReleaseOutputFromJson: (obj: any) => CompleteSeedReleaseOutput;
export declare const CompleteSeedReleaseOutputToJson: (obj: CompleteSeedReleaseOutput) => any;
export declare const FRAGMENT = "\nfragment CompleteSeedReleaseOutputFragment on CompleteSeedReleaseOutput {\n    __typename\n    complete_seed_release_output_seed: seed\n}";
export default CompleteSeedReleaseOutput;
