export const RegisterSparkWalletWebhook = `
  mutation RegisterSparkWalletWebhook($input: RegisterSparkWalletWebhookInput!) {
    register_wallet_webhook(input: $input) {
      webhook_id
    }
  }
`;
