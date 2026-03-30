export const ListSparkWalletWebhooks = `
  query ListSparkWalletWebhooks {
    wallet_webhooks {
      webhooks {
        webhook_id
        url
        event_types
      }
    }
  }
`;
