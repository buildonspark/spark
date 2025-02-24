// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const RequestLeavesSwapOutputFromJson = (obj) => {
    return {
        requestId: obj["request_leaves_swap_output_request"].id,
    };
};
export const RequestLeavesSwapOutputToJson = (obj) => {
    return {
        request_leaves_swap_output_request: { id: obj.requestId },
    };
};
export const FRAGMENT = `
fragment RequestLeavesSwapOutputFragment on RequestLeavesSwapOutput {
    __typename
    request_leaves_swap_output_request: request {
        id
    }
}`;
//# sourceMappingURL=RequestLeavesSwapOutput.js.map