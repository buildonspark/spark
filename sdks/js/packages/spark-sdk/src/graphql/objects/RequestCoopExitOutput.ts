
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface RequestCoopExitOutput {


    requestId: string;




}

export const RequestCoopExitOutputFromJson = (obj: any): RequestCoopExitOutput => {
    return {
        requestId: obj["request_coop_exit_output_request"].id,

        } as RequestCoopExitOutput;

}
export const RequestCoopExitOutputToJson = (obj: RequestCoopExitOutput): any => {
return {
request_coop_exit_output_request: { id: obj.requestId },

        }

}


    export const FRAGMENT = `
fragment RequestCoopExitOutputFragment on RequestCoopExitOutput {
    __typename
    request_coop_exit_output_request: request {
        id
    }
}`;




export default RequestCoopExitOutput;
