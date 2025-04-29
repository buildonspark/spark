
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface CompleteCoopExitOutput {


    requestId: string;




}

export const CompleteCoopExitOutputFromJson = (obj: any): CompleteCoopExitOutput => {
    return {
        requestId: obj["complete_coop_exit_output_request"].id,

        } as CompleteCoopExitOutput;

}
export const CompleteCoopExitOutputToJson = (obj: CompleteCoopExitOutput): any => {
return {
complete_coop_exit_output_request: { id: obj.requestId },

        }

}


    export const FRAGMENT = `
fragment CompleteCoopExitOutputFragment on CompleteCoopExitOutput {
    __typename
    complete_coop_exit_output_request: request {
        id
    }
}`;




export default CompleteCoopExitOutput;
