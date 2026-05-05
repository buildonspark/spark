// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import type SparkWalletWebhookEventType from "./SparkWalletWebhookEventType.js";

interface SparkWalletWebhookEntry {
  webhook_id: string;
  url: string;
  event_types: SparkWalletWebhookEventType[];
}

export const SparkWalletWebhookEntryFromJson = (
  obj: any,
): SparkWalletWebhookEntry => {
  return {
    webhook_id: obj["webhook_id"],
    url: obj["url"],
    event_types: obj["event_types"],
  };
};

export default SparkWalletWebhookEntry;
