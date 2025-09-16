import { setCrypto } from "../utils/crypto.js";
import {
  setFetch,
  SparkFetch,
  SparkHeadersConstructor,
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
/* Use Browser otel wrapper for now (more compatible with bare-fetch): */
export { SparkWallet } from "../spark-wallet/spark-wallet.bare.js";
export { getLatestDepositTxId } from "../utils/mempool.js";
export * from "../utils/index.js";
export {
  DefaultSparkSigner,
  TaprootOutputKeysGenerator,
  TaprootSparkSigner,
  UnsafeStatelessSparkSigner,
  type SparkSigner,
} from "../signer/signer.js";
export { type IKeyPackage } from "../spark_bindings/types.js";
export {
  type SignFrostParams,
  type AggregateFrostParams,
} from "../signer/types.js";
