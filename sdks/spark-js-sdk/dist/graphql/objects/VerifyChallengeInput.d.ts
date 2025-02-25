import Provider from './Provider.js';
interface VerifyChallengeInput {
    protectedChallenge: string;
    signature: string;
    identityPublicKey: string;
    provider?: Provider | undefined;
}
export declare const VerifyChallengeInputFromJson: (obj: any) => VerifyChallengeInput;
export declare const VerifyChallengeInputToJson: (obj: VerifyChallengeInput) => any;
export default VerifyChallengeInput;
