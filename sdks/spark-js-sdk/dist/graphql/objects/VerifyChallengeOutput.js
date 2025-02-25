// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const VerifyChallengeOutputFromJson = (obj) => {
    return {
        validUntil: obj["verify_challenge_output_valid_until"],
    };
};
export const VerifyChallengeOutputToJson = (obj) => {
    return {
        verify_challenge_output_valid_until: obj.validUntil,
    };
};
export const FRAGMENT = `
fragment VerifyChallengeOutputFragment on VerifyChallengeOutput {
    __typename
    verify_challenge_output_valid_until: valid_until
}`;
//# sourceMappingURL=VerifyChallengeOutput.js.map