
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface GetChallengeInput {


    publicKey: string;




}

export const GetChallengeInputFromJson = (obj: any): GetChallengeInput => {
    return {
        publicKey: obj["get_challenge_input_public_key"],

        } as GetChallengeInput;

}
export const GetChallengeInputToJson = (obj: GetChallengeInput): any => {
return {
get_challenge_input_public_key: obj.publicKey,

        }

}





export default GetChallengeInput;
