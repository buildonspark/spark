// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const CompleteCoopExitOutputFromJson = (obj) => {
    return {
        requestId: obj["complete_coop_exit_output_request"].id,
    };
};
export const CompleteCoopExitOutputToJson = (obj) => {
    return {
        complete_coop_exit_output_request: { id: obj.requestId },
    };
};
export const FRAGMENT = `
fragment CompleteCoopExitOutputFragment on CompleteCoopExitOutput {
    __typename
    complete_coop_exit_output_request: request {
        id
    }
}`;
//# sourceMappingURL=CompleteCoopExitOutput.js.map