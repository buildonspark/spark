// Copyright ©, 2025-present, Lightspark Group, Inc. - All Rights Reserved

interface InstantStaticDepositClaimOutput {
  claimId: string;
}

export const InstantStaticDepositClaimOutputFromJson = (
  obj: any,
): InstantStaticDepositClaimOutput => {
  return {
    claimId: obj["instant_claim_output_claim_id"],
  };
};

export const FRAGMENT = `
fragment InstantStaticDepositClaimOutputFragment on CreateClaimInstantStaticDepositOutput {
    __typename
    instant_claim_output_claim_id: claim_id
}`;

export default InstantStaticDepositClaimOutput;
