
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface GetChallengeOutput {


    protectedChallenge: string;




}

export const GetChallengeOutputFromJson = (obj: any): GetChallengeOutput => {
    return {
        protectedChallenge: obj["get_challenge_output_protected_challenge"],

        } as GetChallengeOutput;

}
export const GetChallengeOutputToJson = (obj: GetChallengeOutput): any => {
return {
get_challenge_output_protected_challenge: obj.protectedChallenge,

        }

}


    export const FRAGMENT = `
fragment GetChallengeOutputFragment on GetChallengeOutput {
    __typename
    get_challenge_output_protected_challenge: protected_challenge
}`;




export default GetChallengeOutput;
