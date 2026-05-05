
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface StartSeedReleaseInput {


    phoneNumber: string;




}

export const StartSeedReleaseInputFromJson = (obj: any): StartSeedReleaseInput => {
    return {
        phoneNumber: obj["start_seed_release_input_phone_number"],

        };

}
export const StartSeedReleaseInputToJson = (obj: StartSeedReleaseInput): any => {
return {
start_seed_release_input_phone_number: obj.phoneNumber,

        }

}





export default StartSeedReleaseInput;
