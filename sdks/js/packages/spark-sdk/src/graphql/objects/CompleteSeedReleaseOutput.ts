
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface CompleteSeedReleaseOutput {


    seed: string;




}

export const CompleteSeedReleaseOutputFromJson = (obj: any): CompleteSeedReleaseOutput => {
    return {
        seed: obj["complete_seed_release_output_seed"],

        } as CompleteSeedReleaseOutput;

}
export const CompleteSeedReleaseOutputToJson = (obj: CompleteSeedReleaseOutput): any => {
return {
complete_seed_release_output_seed: obj.seed,

        }

}


    export const FRAGMENT = `
fragment CompleteSeedReleaseOutputFragment on CompleteSeedReleaseOutput {
    __typename
    complete_seed_release_output_seed: seed
}`;




export default CompleteSeedReleaseOutput;
