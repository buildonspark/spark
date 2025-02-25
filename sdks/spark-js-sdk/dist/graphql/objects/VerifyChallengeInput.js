// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
import { ProviderToJson } from './Provider.js';
import { ProviderFromJson } from './Provider.js';
export const VerifyChallengeInputFromJson = (obj) => {
    return {
        protectedChallenge: obj["verify_challenge_input_protected_challenge"],
        signature: obj["verify_challenge_input_signature"],
        identityPublicKey: obj["verify_challenge_input_identity_public_key"],
        provider: (!!obj["verify_challenge_input_provider"] ? ProviderFromJson(obj["verify_challenge_input_provider"]) : undefined),
    };
};
export const VerifyChallengeInputToJson = (obj) => {
    return {
        verify_challenge_input_protected_challenge: obj.protectedChallenge,
        verify_challenge_input_signature: obj.signature,
        verify_challenge_input_identity_public_key: obj.identityPublicKey,
        verify_challenge_input_provider: (obj.provider ? ProviderToJson(obj.provider) : undefined),
    };
};
//# sourceMappingURL=VerifyChallengeInput.js.map