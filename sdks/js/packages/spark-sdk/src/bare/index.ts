import { setCrypto } from "../utils/crypto.js";
import {
  setFetch,
  type SparkFetch,
  type SparkHeadersConstructor,
} from "../utils/fetch.js";
import {
  AbortController,
  abortableFetch,
} from "abortcontroller-polyfill/dist/cjs-ponyfill.js";
import { webcrypto } from "bare-crypto";
import bareFetch from "bare-fetch";
import { default as BareHeaders } from "bare-fetch/headers";

declare const Bare: {
  on: (event: string, listener: (...args: unknown[]) => void) => void;
};

globalThis.AbortController = AbortController;

const Headers = BareHeaders as SparkHeadersConstructor;

const { fetch: abortableBareFetch } = abortableFetch(bareFetch);
const sparkBareFetch: SparkFetch = async (input, init = {}) => {
  if (!init.headers) {
    init.headers = new Headers();
  }

  const result = await abortableBareFetch(input, init);
  return result;
};

setCrypto(webcrypto);
setFetch(sparkBareFetch, Headers);

export * from "../errors/index.js";
export { SparkWallet } from "../spark-wallet/spark-wallet.bare.js";
export { getLatestDepositTxId } from "../utils/mempool.js";
export { decodeSparkAddress } from "../utils/address.js";
export { Network, type NetworkType } from "../utils/network.js";
export {
  createLocalSigningOperators,
  getLocalSigningOperators,
  getLocalSigningThreshold,
  getSspIdentityPublicKey,
  getSspSchemaEndpoint,
  mergeConfigOptionsForNetwork,
  normalizeNetworkType,
  rewriteSigningOperatorAddresses,
} from "../services/wallet-config.js";
export {
  DefaultSparkSigner,
  UnsafeStatelessSparkSigner,
  type SparkSigner,
} from "../signer/signer.js";
export { type IKeyPackage } from "../spark-bindings/types.js";
export { SparkReadonlyClientBare as SparkReadonlyClient } from "../spark-readonly-client/spark-readonly-client.bare.js";
export { SparkFrostBase } from "../spark-bindings/spark-bindings.js";
export {
  type SignFrostParams,
  type AggregateFrostParams,
} from "../signer/types.js";
export {
  setSparkFrostOnce,
  getSparkFrost,
} from "../spark-bindings/spark-bindings.js";
export {
  type SignFrostBindingParams,
  type AggregateFrostBindingParams,
} from "../spark-bindings/types.js";
