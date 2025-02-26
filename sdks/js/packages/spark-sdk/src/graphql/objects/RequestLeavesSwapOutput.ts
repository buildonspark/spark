
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface RequestLeavesSwapOutput {


    requestId: string;




}

export const RequestLeavesSwapOutputFromJson = (obj: any): RequestLeavesSwapOutput => {
    return {
        requestId: obj["request_leaves_swap_output_request"].id,

        } as RequestLeavesSwapOutput;

}
export const RequestLeavesSwapOutputToJson = (obj: RequestLeavesSwapOutput): any => {
return {
request_leaves_swap_output_request: { id: obj.requestId },

        }

}


    export const FRAGMENT = `
fragment RequestLeavesSwapOutputFragment on RequestLeavesSwapOutput {
    __typename
    request_leaves_swap_output_request: request {
        id
    }
}`;




export default RequestLeavesSwapOutput;
