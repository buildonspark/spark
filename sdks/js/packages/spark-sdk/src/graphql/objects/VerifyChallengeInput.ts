
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import {ProviderFromJson} from './Provider.js';
import {ProviderToJson} from './Provider.js';
import Provider from './Provider.js';


interface VerifyChallengeInput {


    protectedChallenge: string;

    signature: string;

    identityPublicKey: string;

    provider?: Provider | undefined;




}

export const VerifyChallengeInputFromJson = (obj: any): VerifyChallengeInput => {
    return {
        protectedChallenge: obj["verify_challenge_input_protected_challenge"],
        signature: obj["verify_challenge_input_signature"],
        identityPublicKey: obj["verify_challenge_input_identity_public_key"],
        provider: (!!obj["verify_challenge_input_provider"] ? ProviderFromJson(obj["verify_challenge_input_provider"]) : undefined),

        } as VerifyChallengeInput;

}
export const VerifyChallengeInputToJson = (obj: VerifyChallengeInput): any => {
return {
verify_challenge_input_protected_challenge: obj.protectedChallenge,
verify_challenge_input_signature: obj.signature,
verify_challenge_input_identity_public_key: obj.identityPublicKey,
verify_challenge_input_provider: (obj.provider ? ProviderToJson(obj.provider) : undefined),

        }

}





export default VerifyChallengeInput;
