// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const CompleteSeedReleaseOutputFromJson = (obj) => {
    return {
        seed: obj["complete_seed_release_output_seed"],
    };
};
export const CompleteSeedReleaseOutputToJson = (obj) => {
    return {
        complete_seed_release_output_seed: obj.seed,
    };
};
export const FRAGMENT = `
fragment CompleteSeedReleaseOutputFragment on CompleteSeedReleaseOutput {
    __typename
    complete_seed_release_output_seed: seed
}`;
//# sourceMappingURL=CompleteSeedReleaseOutput.js.map