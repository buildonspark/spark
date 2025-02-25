// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const StartSeedReleaseInputFromJson = (obj) => {
    return {
        phoneNumber: obj["start_seed_release_input_phone_number"],
    };
};
export const StartSeedReleaseInputToJson = (obj) => {
    return {
        start_seed_release_input_phone_number: obj.phoneNumber,
    };
};
//# sourceMappingURL=StartSeedReleaseInput.js.map