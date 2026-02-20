import type { ConfigOptions } from '@buildonspark/spark-sdk';

/**
 * In the internal repository, the private dev config JSON is resolved normally.
 * In the public repository the file doesn't exist, so Metro's custom resolver
 * (see metro.config.js) returns an empty module and we fall back to defaults.
 */
// eslint-disable-next-line @typescript-eslint/no-var-requires
const privateConfig = require('../../../../private/config/dev-regtest-config.json');

const DEFAULT_CONFIG = {
  network: 'REGTEST',
} as ConfigOptions;

export const CONFIG: ConfigOptions = privateConfig?.network
  ? privateConfig
  : DEFAULT_CONFIG;
