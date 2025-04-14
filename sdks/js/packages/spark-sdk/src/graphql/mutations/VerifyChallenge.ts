import { FRAGMENT as VerifyChallengeOutputFragment } from "../objects/VerifyChallengeOutput.js";

export const VerifyChallenge = `
  mutation VerifyChallenge(
    $protected_challenge: String!
    $signature: String!
    $identity_public_key: PublicKey!
    $provider: Provider
  ) {
    verify_challenge(input: {
      protected_challenge: $protected_challenge
      signature: $signature
      identity_public_key: $identity_public_key
      provider: $provider
    }) {
      ...VerifyChallengeOutputFragment
    }
  }
    
    ${VerifyChallengeOutputFragment}
`;
