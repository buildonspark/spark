
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface VerifyChallengeOutput {


    validUntil: string;




}

export const VerifyChallengeOutputFromJson = (obj: any): VerifyChallengeOutput => {
    return {
        validUntil: obj["verify_challenge_output_valid_until"],

        } as VerifyChallengeOutput;

}
export const VerifyChallengeOutputToJson = (obj: VerifyChallengeOutput): any => {
return {
verify_challenge_output_valid_until: obj.validUntil,

        }

}


    export const FRAGMENT = `
fragment VerifyChallengeOutputFragment on VerifyChallengeOutput {
    __typename
    verify_challenge_output_valid_until: valid_until
}`;




export default VerifyChallengeOutput;
