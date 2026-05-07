import type { ConfigOptions } from '@buildonspark/spark-sdk';

/**
 * In the internal repository, the private dev config JSON is resolved normally.
 * In the public repository the file doesn't exist, so Metro's custom resolver
 * (see metro.config.js) returns an empty module and the app throws to prevent
 * tests from accidentally hitting production.
 */
const privateConfig = require('../../../private/config/dev-regtest-config.json');

if (!privateConfig?.network) {
  throw new Error(
    'Private config not loaded — tests would fall back to SDK defaults (prod). ' +
      'Ensure private/config/dev-regtest-config.json exists and the require path is correct.',
  );
}

export const CONFIG: ConfigOptions = privateConfig;
