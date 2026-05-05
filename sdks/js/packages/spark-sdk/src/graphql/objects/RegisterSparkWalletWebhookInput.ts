// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import type SparkWalletWebhookEventType from "./SparkWalletWebhookEventType.js";

interface RegisterSparkWalletWebhookInput {
  secret: string;
  url: string;
  event_types: SparkWalletWebhookEventType[];
}

export default RegisterSparkWalletWebhookInput;
