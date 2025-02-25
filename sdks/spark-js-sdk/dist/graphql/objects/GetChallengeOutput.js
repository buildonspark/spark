// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const GetChallengeOutputFromJson = (obj) => {
    return {
        protectedChallenge: obj["get_challenge_output_protected_challenge"],
    };
};
export const GetChallengeOutputToJson = (obj) => {
    return {
        get_challenge_output_protected_challenge: obj.protectedChallenge,
    };
};
export const FRAGMENT = `
fragment GetChallengeOutputFragment on GetChallengeOutput {
    __typename
    get_challenge_output_protected_challenge: protected_challenge
}`;
//# sourceMappingURL=GetChallengeOutput.js.map