
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface CompleteLeavesSwapOutput {


    requestId: string;




}

export const CompleteLeavesSwapOutputFromJson = (obj: any): CompleteLeavesSwapOutput => {
    return {
        requestId: obj["complete_leaves_swap_output_request"].id,

        } as CompleteLeavesSwapOutput;

}
export const CompleteLeavesSwapOutputToJson = (obj: CompleteLeavesSwapOutput): any => {
return {
complete_leaves_swap_output_request: { id: obj.requestId },

        }

}


    export const FRAGMENT = `
fragment CompleteLeavesSwapOutputFragment on CompleteLeavesSwapOutput {
    __typename
    complete_leaves_swap_output_request: request {
        id
    }
}`;




export default CompleteLeavesSwapOutput;
