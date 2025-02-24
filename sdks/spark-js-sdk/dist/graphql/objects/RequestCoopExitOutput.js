// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const RequestCoopExitOutputFromJson = (obj) => {
    return {
        requestId: obj["request_coop_exit_output_request"].id,
    };
};
export const RequestCoopExitOutputToJson = (obj) => {
    return {
        request_coop_exit_output_request: { id: obj.requestId },
    };
};
export const FRAGMENT = `
fragment RequestCoopExitOutputFragment on RequestCoopExitOutput {
    __typename
    request_coop_exit_output_request: request {
        id
    }
}`;
//# sourceMappingURL=RequestCoopExitOutput.js.map