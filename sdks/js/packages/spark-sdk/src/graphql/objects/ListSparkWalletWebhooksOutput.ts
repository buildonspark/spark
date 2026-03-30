// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import SparkWalletWebhookEntry, {
  SparkWalletWebhookEntryFromJson,
} from "./SparkWalletWebhookEntry.js";

interface ListSparkWalletWebhooksOutput {
  webhooks: SparkWalletWebhookEntry[];
}

export const ListSparkWalletWebhooksOutputFromJson = (
  obj: any,
): ListSparkWalletWebhooksOutput => {
  return {
    webhooks: obj["webhooks"].map(SparkWalletWebhookEntryFromJson),
  } as ListSparkWalletWebhooksOutput;
};

export default ListSparkWalletWebhooksOutput;
