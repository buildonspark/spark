
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface ClaimStaticDepositOutput {


    transferId: string;




}

export const ClaimStaticDepositOutputFromJson = (obj: any): ClaimStaticDepositOutput => {
    return {
        transferId: obj["claim_static_deposit_output_transfer_id"],

        } as ClaimStaticDepositOutput;

}
export const ClaimStaticDepositOutputToJson = (obj: ClaimStaticDepositOutput): any => {
return {
claim_static_deposit_output_transfer_id: obj.transferId,

        }

}


    export const FRAGMENT = `
fragment ClaimStaticDepositOutputFragment on ClaimStaticDepositOutput {
    __typename
    claim_static_deposit_output_transfer_id: transfer_id
}`;




export default ClaimStaticDepositOutput;
