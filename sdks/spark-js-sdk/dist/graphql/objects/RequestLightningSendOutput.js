// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const RequestLightningSendOutputFromJson = (obj) => {
    return {
        requestId: obj["request_lightning_send_output_request"].id,
    };
};
export const RequestLightningSendOutputToJson = (obj) => {
    return {
        request_lightning_send_output_request: { id: obj.requestId },
    };
};
export const FRAGMENT = `
fragment RequestLightningSendOutputFragment on RequestLightningSendOutput {
    __typename
    request_lightning_send_output_request: request {
        id
    }
}`;
//# sourceMappingURL=RequestLightningSendOutput.js.map