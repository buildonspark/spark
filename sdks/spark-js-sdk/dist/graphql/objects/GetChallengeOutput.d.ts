interface GetChallengeOutput {
    protectedChallenge: string;
}
export declare const GetChallengeOutputFromJson: (obj: any) => GetChallengeOutput;
export declare const GetChallengeOutputToJson: (obj: GetChallengeOutput) => any;
export declare const FRAGMENT = "\nfragment GetChallengeOutputFragment on GetChallengeOutput {\n    __typename\n    get_challenge_output_protected_challenge: protected_challenge\n}";
export default GetChallengeOutput;
