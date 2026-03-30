export const DeleteSparkWalletWebhook = `
  mutation DeleteSparkWalletWebhook($input: DeleteSparkWalletWebhookInput!) {
    delete_wallet_webhook(input: $input) {
      success
    }
  }
`;
