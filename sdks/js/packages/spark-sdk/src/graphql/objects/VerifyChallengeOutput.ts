
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface VerifyChallengeOutput {


    validUntil: string;

    sessionToken: string;




}

export const VerifyChallengeOutputFromJson = (obj: any): VerifyChallengeOutput => {
    return {
        validUntil: obj["verify_challenge_output_valid_until"],
        sessionToken: obj["verify_challenge_output_session_token"],

        } as VerifyChallengeOutput;

}
export const VerifyChallengeOutputToJson = (obj: VerifyChallengeOutput): any => {
return {
verify_challenge_output_valid_until: obj.validUntil,
verify_challenge_output_session_token: obj.sessionToken,

        }

}


    export const FRAGMENT = `
fragment VerifyChallengeOutputFragment on VerifyChallengeOutput {
    __typename
    verify_challenge_output_valid_until: valid_until
    verify_challenge_output_session_token: session_token
}`;




export default VerifyChallengeOutput;
