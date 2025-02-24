// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const RequestLightningReceiveOutputFromJson = (obj) => {
    return {
        requestId: obj["request_lightning_receive_output_request"].id,
    };
};
export const RequestLightningReceiveOutputToJson = (obj) => {
    return {
        request_lightning_receive_output_request: { id: obj.requestId },
    };
};
export const FRAGMENT = `
fragment RequestLightningReceiveOutputFragment on RequestLightningReceiveOutput {
    __typename
    request_lightning_receive_output_request: request {
        id
    }
}`;
//# sourceMappingURL=RequestLightningReceiveOutput.js.map