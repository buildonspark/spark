import { FRAGMENT as GetChallengeOutputFragment } from "../objects/GetChallengeOutput.js";

export const GetChallenge = `
  mutation GetChallenge(
    $public_key: PublicKey!
  ) {
    get_challenge(input: {
      public_key: $public_key
    }) {
      ...GetChallengeOutputFragment
    }
  }
    
    ${GetChallengeOutputFragment}
`;
