// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const CompleteLeavesSwapOutputFromJson = (obj) => {
    return {
        requestId: obj["complete_leaves_swap_output_request"].id,
    };
};
export const CompleteLeavesSwapOutputToJson = (obj) => {
    return {
        complete_leaves_swap_output_request: { id: obj.requestId },
    };
};
export const FRAGMENT = `
fragment CompleteLeavesSwapOutputFragment on CompleteLeavesSwapOutput {
    __typename
    complete_leaves_swap_output_request: request {
        id
    }
}`;
//# sourceMappingURL=CompleteLeavesSwapOutput.js.map