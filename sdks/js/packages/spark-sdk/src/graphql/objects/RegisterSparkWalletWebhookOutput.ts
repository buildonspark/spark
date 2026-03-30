// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

interface RegisterSparkWalletWebhookOutput {
  webhook_id: string;
}

export const RegisterSparkWalletWebhookOutputFromJson = (
  obj: any,
): RegisterSparkWalletWebhookOutput => {
  return {
    webhook_id: obj["webhook_id"],
  } as RegisterSparkWalletWebhookOutput;
};

export default RegisterSparkWalletWebhookOutput;
