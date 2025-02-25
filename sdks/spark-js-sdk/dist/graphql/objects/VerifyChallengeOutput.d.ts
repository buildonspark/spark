interface VerifyChallengeOutput {
    validUntil: string;
}
export declare const VerifyChallengeOutputFromJson: (obj: any) => VerifyChallengeOutput;
export declare const VerifyChallengeOutputToJson: (obj: VerifyChallengeOutput) => any;
export declare const FRAGMENT = "\nfragment VerifyChallengeOutputFragment on VerifyChallengeOutput {\n    __typename\n    verify_challenge_output_valid_until: valid_until\n}";
export default VerifyChallengeOutput;
