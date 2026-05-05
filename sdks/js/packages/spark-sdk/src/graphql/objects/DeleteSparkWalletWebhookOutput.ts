// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

interface DeleteSparkWalletWebhookOutput {
  success: boolean;
}

export const DeleteSparkWalletWebhookOutputFromJson = (
  obj: any,
): DeleteSparkWalletWebhookOutput => {
  return {
    success: obj["success"],
  };
};

export default DeleteSparkWalletWebhookOutput;
