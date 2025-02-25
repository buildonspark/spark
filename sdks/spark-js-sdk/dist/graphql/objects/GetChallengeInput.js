// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const GetChallengeInputFromJson = (obj) => {
    return {
        publicKey: obj["get_challenge_input_public_key"],
    };
};
export const GetChallengeInputToJson = (obj) => {
    return {
        get_challenge_input_public_key: obj.publicKey,
    };
};
//# sourceMappingURL=GetChallengeInput.js.map