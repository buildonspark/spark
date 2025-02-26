
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface RequestLightningReceiveOutput {


    requestId: string;




}

export const RequestLightningReceiveOutputFromJson = (obj: any): RequestLightningReceiveOutput => {
    return {
        requestId: obj["request_lightning_receive_output_request"].id,

        } as RequestLightningReceiveOutput;

}
export const RequestLightningReceiveOutputToJson = (obj: RequestLightningReceiveOutput): any => {
return {
request_lightning_receive_output_request: { id: obj.requestId },

        }

}


    export const FRAGMENT = `
fragment RequestLightningReceiveOutputFragment on RequestLightningReceiveOutput {
    __typename
    request_lightning_receive_output_request: request {
        id
    }
}`;




export default RequestLightningReceiveOutput;
