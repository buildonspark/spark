// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const CompleteSeedReleaseInputFromJson = (obj) => {
    return {
        phoneNumber: obj["complete_seed_release_input_phone_number"],
        code: obj["complete_seed_release_input_code"],
    };
};
export const CompleteSeedReleaseInputToJson = (obj) => {
    return {
        complete_seed_release_input_phone_number: obj.phoneNumber,
        complete_seed_release_input_code: obj.code,
    };
};
//# sourceMappingURL=CompleteSeedReleaseInput.js.map