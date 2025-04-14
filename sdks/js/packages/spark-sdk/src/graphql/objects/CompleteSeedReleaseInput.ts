
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface CompleteSeedReleaseInput {


    phoneNumber: string;

    code: string;




}

export const CompleteSeedReleaseInputFromJson = (obj: any): CompleteSeedReleaseInput => {
    return {
        phoneNumber: obj["complete_seed_release_input_phone_number"],
        code: obj["complete_seed_release_input_code"],

        } as CompleteSeedReleaseInput;

}
export const CompleteSeedReleaseInputToJson = (obj: CompleteSeedReleaseInput): any => {
return {
complete_seed_release_input_phone_number: obj.phoneNumber,
complete_seed_release_input_code: obj.code,

        }

}





export default CompleteSeedReleaseInput;
