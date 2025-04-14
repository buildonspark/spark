
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface RequestLightningSendOutput {


    requestId: string;




}

export const RequestLightningSendOutputFromJson = (obj: any): RequestLightningSendOutput => {
    return {
        requestId: obj["request_lightning_send_output_request"].id,

        } as RequestLightningSendOutput;

}
export const RequestLightningSendOutputToJson = (obj: RequestLightningSendOutput): any => {
return {
request_lightning_send_output_request: { id: obj.requestId },

        }

}


    export const FRAGMENT = `
fragment RequestLightningSendOutputFragment on RequestLightningSendOutput {
    __typename
    request_lightning_send_output_request: request {
        id
    }
}`;




export default RequestLightningSendOutput;
