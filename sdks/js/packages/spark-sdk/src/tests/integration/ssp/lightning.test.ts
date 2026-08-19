import { describe, expect, it, jest } from "@jest/globals";
import { bytesToHex } from "@noble/curves/utils";
import { sha256 } from "@noble/hashes/sha2";
import { SparkValidationError } from "../../../errors/types.js";
import { decodeInvoice } from "../../../services/bolt11-spark.js";
import { type ConfigOptions } from "../../../services/wallet-config.js";
import { SparkWallet } from "../../../spark-wallet/spark-wallet.node.js";
import {
  CurrencyUnit,
  type LightningReceiveRequest,
  LightningReceiveRequestStatus,
  type LightningSendRequest,
  LightningSendRequestStatus,
} from "../../../types/index.js";
import {
  decodeSparkAddress,
  type SparkAddressFormat,
  validateSparkInvoiceSignature,
} from "../../../utils/address.js";
import { SparkWalletTestingWithStream } from "../../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../../utils/test-faucet.js";
import {
  retryUntilSuccess,
  waitForBalance,
  waitForClaim,
} from "../../utils/utils.js";

const DEPOSIT_AMOUNT = 10000n;
const INVOICE_AMOUNT = 1000;
const APP_MINIKUBE_HOST = "app.minikube.local";
const INTERNAL_GRAPHQL_PATH = "/graphql/internal";
const FIRST_PARTY_GRAPHQL_PATH = "/graphql/frontend";
const HERMETIC_SSP_USER_PASSWORD = "t3st1246!@_1!";
const GRAPHQL_REQUEST_TIMEOUT_MS = 30_000;
const EXPIRY_REFUND_KNOBS = {
  "spark.ssp.lightning_receive.min_invoice_expiry_secs": 5,
  "spark.ssp.lightning_receive.expiry_buffer_secs": 5,
  "spark.ssp.internal_lightning_payment.max_preimage_swap_expiry_secs": 45,
  "spark.ssp.internal_lightning_payment.state_machine.rollout_pct": 100,
  "spark.ssp.internal_lightning_payment.wait_on_missing_preimage": 1,
};
const SSP_RELEASE_KNOBS = {
  "spark.ssp.internal_lightning_payment.release_preimage_enqueue_enabled": 1,
  "spark.ssp.internal_lightning_payment.state_machine.rollout_pct": 100,
  "spark.ssp.internal_lightning_payment.wait_on_missing_preimage": 1,
};
// Sparkcore polls parked payments every three minutes, while SOs only return
// preimage swaps after they have been stuck for five minutes.
const PREIMAGE_SWAP_EXPIRY_TIMEOUT_MS = 9 * 60 * 1000;

jest.retryTimes(0);

const options: ConfigOptions = {
  network: "LOCAL",
};

type GraphqlError = {
  message: string;
};

type KnobSnapshot = {
  name: string;
  target?: string | null;
  value: number;
};

function randomPreimage(): Uint8Array {
  const preimage = new Uint8Array(32);
  crypto.getRandomValues(preimage);
  return preimage;
}

function paymentHashForPreimage(preimage: Uint8Array): string {
  return bytesToHex(sha256(preimage));
}

function endpointForPath(path: string): {
  url: string;
  headers: Record<string, string>;
} {
  const internalEndpoint =
    process.env.LIGHTSPARK_INTERNAL_API_ENDPOINT ??
    process.env.SPARK_INTERNAL_GRAPHQL_ENDPOINT;
  if (path === INTERNAL_GRAPHQL_PATH && internalEndpoint) {
    return { url: internalEndpoint, headers: {} };
  }

  const baseUrl = process.env.SPARKCORE_BASE_URL;
  if (baseUrl) {
    return { url: `${baseUrl.replace(/\/$/, "")}${path}`, headers: {} };
  }

  if (
    path.startsWith("/graphql/server") &&
    process.env.LIGHTSPARK_API_ENDPOINT
  ) {
    return { url: process.env.LIGHTSPARK_API_ENDPOINT, headers: {} };
  }

  if (path === INTERNAL_GRAPHQL_PATH && process.env.LIGHTSPARK_API_ENDPOINT) {
    return {
      url: new URL(path, process.env.LIGHTSPARK_API_ENDPOINT).toString(),
      headers: {},
    };
  }

  const ingressHost = process.env.SPARK_LOCAL_INGRESS_HOST;
  if (ingressHost) {
    return {
      url: `http://${ingressHost}${path}`,
      headers: { Host: APP_MINIKUBE_HOST },
    };
  }

  if (path === INTERNAL_GRAPHQL_PATH) {
    throw new Error(
      "Missing internal GraphQL endpoint env. Set LIGHTSPARK_INTERNAL_API_ENDPOINT, SPARKCORE_BASE_URL, LIGHTSPARK_API_ENDPOINT, or SPARK_LOCAL_INGRESS_HOST so /graphql/internal can resolve in hermetic tests.",
    );
  }

  return { url: `http://${APP_MINIKUBE_HOST}${path}`, headers: {} };
}

async function executeGraphql<T>({
  path,
  query,
  variables,
  additionalHeaders,
}: {
  path: string;
  query: string;
  variables: Record<string, unknown>;
  additionalHeaders?: Record<string, string>;
}): Promise<T> {
  const endpoint = endpointForPath(path);
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...endpoint.headers,
    ...additionalHeaders,
  };

  const response = await fetch(endpoint.url, {
    method: "POST",
    headers,
    body: JSON.stringify({ query, variables }),
    signal: AbortSignal.timeout(GRAPHQL_REQUEST_TIMEOUT_MS),
  });
  const body = await response.text();
  if (!response.ok) {
    throw new Error(`GraphQL HTTP ${response.status}: ${body}`);
  }

  const result = JSON.parse(body) as { data?: T; errors?: GraphqlError[] };
  if (result.errors?.length) {
    throw new Error(
      `GraphQL errors: ${result.errors.map((e) => e.message).join("; ")}`,
    );
  }
  if (!result.data) {
    throw new Error(`GraphQL response missing data: ${body}`);
  }
  return result.data;
}

function envValue(...names: string[]): string | undefined {
  return names.map((name) => process.env[name]).find(Boolean);
}

function sspApiTokenAuthorization(): string | undefined {
  const clientId = envValue(
    "SPARK_SSP_API_TOKEN_CLIENT_ID",
    "LIGHTSPARK_SSP_API_TOKEN_CLIENT_ID",
    "LIGHTSPARK_API_TOKEN_CLIENT_ID",
  );
  const clientSecret = envValue(
    "SPARK_SSP_API_TOKEN_CLIENT_SECRET",
    "LIGHTSPARK_SSP_API_TOKEN_CLIENT_SECRET",
    "LIGHTSPARK_API_TOKEN_CLIENT_SECRET",
  );
  if (!clientId || !clientSecret) {
    return undefined;
  }
  return `Basic ${Buffer.from(`${clientId}:${clientSecret}`, "utf8").toString("base64")}`;
}

async function hermeticSspAuthorization(invoiceId: string): Promise<{
  authorization: string;
  apiTokenId: string;
  sessionCookie: string;
}> {
  const userData = await executeGraphql<{
    entity?: {
      data?: {
        destination?: {
          owner?: {
            users?: { entities?: Array<{ email?: string }> };
          };
        };
      };
    };
  }>({
    path: INTERNAL_GRAPHQL_PATH,
    query: `
      query GetSparkInvoiceOwnerUser($id: ID!) {
        entity(id: $id) {
          ... on Invoice {
            data {
              destination {
                ... on LightsparkNode {
                  owner {
                    ... on Account {
                      users(first: 1) {
                      entities {
                          email
                        }
                      }
                    }
                  }
                }
              }
            }
          }
        }
      }
    `,
    variables: { id: invoiceId },
  });
  const email =
    userData.entity?.data?.destination?.owner?.users?.entities?.[0]?.email;
  if (!email) {
    throw new Error(`invoice ${invoiceId} did not expose an owner email`);
  }

  const endpoint = endpointForPath(FIRST_PARTY_GRAPHQL_PATH);
  const loginResponse = await fetch(endpoint.url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...endpoint.headers,
    },
    body: JSON.stringify({
      query: `
        mutation LoginAsSparkInvoiceOwner($email: String!, $password: String!) {
          login_with_password(input: {
            email: $email
            password: $password
            recaptcha_token: ""
          }) {
            ... on LoginWithPasswordOutput {
              account_id
            }
          }
        }
      `,
      variables: { email, password: HERMETIC_SSP_USER_PASSWORD },
    }),
    signal: AbortSignal.timeout(GRAPHQL_REQUEST_TIMEOUT_MS),
  });
  const loginBody = await loginResponse.text();
  if (!loginResponse.ok) {
    throw new Error(`GraphQL HTTP ${loginResponse.status}: ${loginBody}`);
  }
  const loginResult = JSON.parse(loginBody) as { errors?: GraphqlError[] };
  if (loginResult.errors?.length) {
    throw new Error(
      `GraphQL errors: ${loginResult.errors.map((error) => error.message).join("; ")}`,
    );
  }
  const sessionCookie = loginResponse.headers
    .get("set-cookie")
    ?.match(/(?:^|,\s*)(session=[^;,]+)/)?.[1];
  if (!sessionCookie) {
    throw new Error("SSP user login did not return a session cookie");
  }

  const tokenData = await executeGraphql<{
    create_api_token?: {
      api_token?: { id?: string; client_id?: string };
      client_secret?: string;
    };
  }>({
    path: FIRST_PARTY_GRAPHQL_PATH,
    query: `
      mutation CreateSparkIntegrationTestApiToken {
        create_api_token(input: {
          name: "Spark hermetic integration test"
          permissions: [ALL]
        }) {
          api_token {
            id
            client_id
          }
          client_secret
        }
      }
    `,
    variables: {},
    additionalHeaders: { Cookie: sessionCookie },
  });
  const apiTokenId = tokenData.create_api_token?.api_token?.id;
  const clientId = tokenData.create_api_token?.api_token?.client_id;
  const clientSecret = tokenData.create_api_token?.client_secret;
  if (!apiTokenId || !clientId || !clientSecret) {
    throw new Error("SSP API token mutation did not return credentials");
  }
  return {
    authorization: `Basic ${Buffer.from(`${clientId}:${clientSecret}`, "utf8").toString("base64")}`,
    apiTokenId,
    sessionCookie,
  };
}

async function deleteHermeticSspApiToken(
  apiTokenId: string,
  sessionCookie: string,
): Promise<void> {
  await executeGraphql<{ delete_api_token?: { __typename?: string } }>({
    path: FIRST_PARTY_GRAPHQL_PATH,
    query: `
      mutation DeleteSparkIntegrationTestApiToken($apiTokenId: ID!) {
        delete_api_token(input: { api_token_id: $apiTokenId }) {
          __typename
        }
      }
    `,
    variables: { apiTokenId },
    additionalHeaders: { Cookie: sessionCookie },
  });
}

async function withSspAuthorization<T>(
  invoiceId: string,
  fn: (headers: Record<string, string>) => Promise<T>,
): Promise<T> {
  const authorization = sspApiTokenAuthorization();
  if (authorization) {
    return await fn({ Authorization: authorization });
  }
  // The dev-cli hermetic variant reaches the SSP via SPARKCORE_BASE_URL and
  // never sets HERMETIC_TEST; both stacks can mint the throwaway token.
  if (process.env.HERMETIC_TEST === "true" || process.env.SPARKCORE_BASE_URL) {
    const hermeticAuthorization = await hermeticSspAuthorization(invoiceId);
    try {
      return await fn({ Authorization: hermeticAuthorization.authorization });
    } finally {
      await deleteHermeticSspApiToken(
        hermeticAuthorization.apiTokenId,
        hermeticAuthorization.sessionCookie,
      );
    }
  }
  throw new Error(
    "Missing SSP API token env. Set SPARK_SSP_API_TOKEN_CLIENT_ID and SPARK_SSP_API_TOKEN_CLIENT_SECRET for the account that owns the SSP Spark Lightning node.",
  );
}

async function getLightningInvoiceId(
  receiveRequestId: string,
): Promise<string> {
  const data = await executeGraphql<{
    entity?: {
      __typename?: string;
      lightning_invoice?: { id?: string };
    };
  }>({
    path: INTERNAL_GRAPHQL_PATH,
    query: `
      query GetSparkReceiveRequestInvoice($id: ID!) {
        entity(id: $id) {
          __typename
          ... on LightningReceiveRequest {
            lightning_invoice {
              id
            }
          }
        }
      }
    `,
    variables: { id: receiveRequestId },
  });

  const invoiceId = data.entity?.lightning_invoice?.id;
  if (!invoiceId) {
    throw new Error(
      `receive request ${receiveRequestId} did not expose an internal lightning invoice id`,
    );
  }
  return invoiceId;
}

async function getInternalLightningReceiveRequestStatus(
  receiveRequestId: string,
): Promise<string> {
  const data = await executeGraphql<{
    entity?: {
      __typename?: string;
      status?: string;
    };
  }>({
    path: INTERNAL_GRAPHQL_PATH,
    query: `
      query GetSparkReceiveRequestStatus($id: ID!) {
        entity(id: $id) {
          __typename
          ... on LightningReceiveRequest {
            status
          }
        }
      }
    `,
    variables: { id: receiveRequestId },
  });

  const status = data.entity?.status;
  if (!status) {
    throw new Error(
      `receive request ${receiveRequestId} did not expose a status`,
    );
  }
  return status;
}

async function releasePaymentPreimage(
  invoiceId: string,
  preimage: Uint8Array,
  additionalHeaders: Record<string, string>,
): Promise<string> {
  const data = await executeGraphql<{
    release_payment_preimage?: {
      invoice?: { id?: string };
    };
  }>({
    path: "/graphql/server/rc",
    query: `
      mutation ReleasePaymentPreimage($invoiceId: ID!, $paymentPreimage: Hash32!) {
        release_payment_preimage(input: {
          invoice_id: $invoiceId
          payment_preimage: $paymentPreimage
        }) {
          invoice {
            id
          }
        }
      }
    `,
    variables: {
      invoiceId,
      paymentPreimage: bytesToHex(preimage),
    },
    additionalHeaders,
  });

  const releasedInvoiceId = data.release_payment_preimage?.invoice?.id;
  if (!releasedInvoiceId) {
    throw new Error("release_payment_preimage did not return an invoice id");
  }
  return releasedInvoiceId;
}

async function getSparkcoreKnob(
  name: string,
): Promise<KnobSnapshot | undefined> {
  const data = await executeGraphql<{
    OPS_knobs?: {
      entities?: KnobSnapshot[];
    };
  }>({
    path: INTERNAL_GRAPHQL_PATH,
    query: `
      query GetSparkcoreKnob($name: String!) {
        OPS_knobs(first: 20, name_contains: $name) {
          entities {
            name
            target
            value
          }
        }
      }
    `,
    variables: { name },
  });

  return data.OPS_knobs?.entities?.find(
    (knob) => knob.name === name && !knob.target,
  );
}

async function setSparkcoreKnob(name: string, value: number): Promise<void> {
  await executeGraphql<{ OPS_set_knob?: null }>({
    path: INTERNAL_GRAPHQL_PATH,
    query: `
      mutation SetSparkcoreKnob($name: String!, $value: Float!) {
        OPS_set_knob(input: {
          name: $name
          target: null
          value: $value
        })
      }
    `,
    variables: { name, value },
  });
}

async function deleteSparkcoreKnob(name: string): Promise<void> {
  await executeGraphql<{ OPS_delete_knob?: null }>({
    path: INTERNAL_GRAPHQL_PATH,
    query: `
      mutation DeleteSparkcoreKnob($name: String!) {
        OPS_delete_knob(input: {
          name: $name
          target: null
        })
      }
    `,
    variables: { name },
  });
}

async function withSparkcoreKnobs<T>(
  knobs: Record<string, number>,
  fn: () => Promise<T>,
): Promise<T> {
  const snapshots = new Map<string, KnobSnapshot | undefined>();
  const attemptedKnobs = new Set<string>();
  for (const name of Object.keys(knobs)) {
    snapshots.set(name, await getSparkcoreKnob(name));
  }

  let outcome: { success: true; value: T } | { success: false; error: unknown };
  try {
    for (const [name, value] of Object.entries(knobs)) {
      attemptedKnobs.add(name);
      await setSparkcoreKnob(name, value);
    }
    outcome = { success: true, value: await fn() };
  } catch (error) {
    outcome = { success: false, error };
  }

  const restorationErrors: unknown[] = [];
  for (const [name, snapshot] of snapshots) {
    try {
      if (snapshot) {
        await setSparkcoreKnob(name, snapshot.value);
      } else if (attemptedKnobs.has(name)) {
        await deleteSparkcoreKnob(name);
      }
    } catch (error) {
      restorationErrors.push(error);
    }
  }
  if (restorationErrors.length > 0) {
    const errors = outcome.success
      ? restorationErrors
      : [outcome.error, ...restorationErrors];
    throw new AggregateError(errors, "Failed to restore sparkcore knobs");
  }
  if (!outcome.success) {
    throw outcome.error;
  }
  return outcome.value;
}

async function fundWallet(wallet: SparkWalletTestingWithStream): Promise<void> {
  const faucet = BitcoinFaucet.getInstance();

  const depositAddress = await wallet.getSingleUseDepositAddress();
  expect(depositAddress).toBeDefined();

  const signedTx = await faucet.sendToAddress(depositAddress, DEPOSIT_AMOUNT);
  await faucet.mineBlocksAndWaitForMiningToComplete(6);
  await wallet.claimDeposit(signedTx.id);
  await waitForBalance(wallet, DEPOSIT_AMOUNT);
}

async function waitForSendRequestStatus(
  wallet: SparkWalletTestingWithStream,
  requestId: string,
  status: LightningSendRequestStatus,
  retryOptions?: {
    maxAttempts?: number;
    delayMs?: number;
    timeoutMs?: number;
  },
): Promise<LightningSendRequest> {
  return await retryUntilSuccess(async () => {
    const req = await wallet.getLightningSendRequest(requestId);
    if (req?.status !== status) {
      throw new Error(
        `send request ${requestId} not ${status} yet: ${req?.status}`,
      );
    }
    return req;
  }, retryOptions);
}

async function waitForReceiveRequestStatus(
  wallet: SparkWalletTestingWithStream,
  requestId: string,
  status: LightningReceiveRequestStatus,
  retryOptions?: {
    maxAttempts?: number;
    delayMs?: number;
    timeoutMs?: number;
  },
): Promise<LightningReceiveRequest> {
  return await retryUntilSuccess(async () => {
    const req = await wallet.getLightningReceiveRequest(requestId);
    if (req?.status !== status) {
      throw new Error(
        `receive request ${requestId} not ${status} yet: ${req?.status}`,
      );
    }
    return req;
  }, retryOptions);
}

async function waitForInternalReceiveRequestStatus(
  requestId: string,
  status: string,
  retryOptions?: {
    maxAttempts?: number;
    delayMs?: number;
    timeoutMs?: number;
  },
): Promise<string> {
  return await retryUntilSuccess(async () => {
    const currentStatus =
      await getInternalLightningReceiveRequestStatus(requestId);
    if (currentStatus !== status) {
      throw new Error(
        `receive request ${requestId} not ${status} yet: ${currentStatus}`,
      );
    }
    return currentStatus;
  }, retryOptions);
}

const { wallet: walletStatic } = await SparkWallet.initialize({
  mnemonicOrSeed:
    "logic ripple layer execute smart disease marine hero monster talent crucial unfair horror shadow maze abuse avoid story loop jaguar sphere trap decrease turn",
  options,
});

describe("Lightning Network provider", () => {
  describe("should create lightning invoice", () => {
    test.concurrent.each([
      [0],
      [1],
      [10],
      [4260],
      [100000000000],
      [100000000001],
    ])(
      `.amount(%s)`,
      async (amountSats) => {
        const invoice = await walletStatic.createLightningInvoice({
          amountSats: amountSats,
          memo: "test",
          expirySeconds: 500,
        });

        expect(invoice).toBeDefined();
        expect(invoice.invoice).toBeDefined();
        expect(invoice.invoice.encodedInvoice.length).toBeGreaterThanOrEqual(
          401,
        );
        expect(invoice.invoice.paymentHash.length).toEqual(64);
        expect(invoice.invoice.amount.originalValue).toEqual(amountSats * 1000);
        expect(invoice.invoice.amount.originalUnit).toEqual(
          CurrencyUnit.MILLISATOSHI,
        );
        expect(invoice.status).toEqual(
          LightningReceiveRequestStatus.INVOICE_CREATED,
        );
        expect(invoice.transfer).toBeUndefined();
      },
      30000,
    );
  });

  describe("should pay lightning invoice", () => {
    it("should pay lightning invoice created by another wallet", async () => {
      const faucet = BitcoinFaucet.getInstance();

      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
        });

      const { wallet: bobWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
        });

      const depositAddress = await aliceWallet.getSingleUseDepositAddress();
      expect(depositAddress).toBeDefined();

      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

      // Wait for the transaction to be mined
      await faucet.mineBlocksAndWaitForMiningToComplete(6);

      await aliceWallet.claimDeposit(signedTx.id);
      await waitForBalance(aliceWallet, DEPOSIT_AMOUNT);

      const invoice = await bobWallet.createLightningInvoice({
        amountSats: INVOICE_AMOUNT,
        memo: "test",
        expirySeconds: 500,
      });

      expect(invoice).toBeDefined();

      // Register listener before payment so we don't miss the stream event.
      const bobClaimed = waitForClaim({ wallet: bobWallet });
      const request = (await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
      })) as LightningSendRequest;

      // wait for the claim event, we care about the transfer completing...
      await bobClaimed;

      const { balance: bobBalance } = await bobWallet.getBalance();
      expect(bobBalance).toBe(BigInt(INVOICE_AMOUNT));

      const { balance: aliceBalance } = await aliceWallet.getBalance();
      expect(aliceBalance).toBeLessThan(
        DEPOSIT_AMOUNT - BigInt(INVOICE_AMOUNT),
      );

      // Verify that payment preimage is still set for spark -> spark lightning payments
      const lightningSendRequest = await aliceWallet.getLightningSendRequest(
        request.id,
      );
      expect(lightningSendRequest?.paymentPreimage).toBeDefined();
    }, 120000);
  });

  describe("should fail to create lightning invoice", () => {
    it(`should fail to create lightning invoice with invalid amount`, async () => {
      await expect(
        walletStatic.createLightningInvoice({
          amountSats: -1,
          memo: "test",
        }),
      ).rejects.toMatchObject({
        name: SparkValidationError.name,
        message: expect.stringContaining("Invalid amount"),
        context: expect.objectContaining({
          field: "amountSats",
          value: -1,
        }),
      });
    }, 30000);

    it(`should fail to create lightning invoice with invalid expiration time`, async () => {
      await expect(
        walletStatic.createLightningInvoice({
          amountSats: 1000,
          memo: "test",
          expirySeconds: -1,
        }),
      ).rejects.toMatchObject({
        name: SparkValidationError.name,
        message: expect.stringContaining("Invalid expiration time"),
        context: expect.objectContaining({
          field: "expirySeconds",
          value: -1,
        }),
      });
    }, 30000);

    it(`should fail to create lightning invoice with invalid memo size`, async () => {
      await expect(
        walletStatic.createLightningInvoice({
          amountSats: 1000,
          memo: "test".repeat(1000),
        }),
      ).rejects.toMatchObject({
        name: SparkValidationError.name,
        message: expect.stringContaining("Invalid memo size"),
        context: expect.objectContaining({
          field: "memo",
          value: "test".repeat(1000),
        }),
      });
    }, 30000);

    it(`should fail when both includeSparkAddress and includeSparkInvoice are true`, async () => {
      await expect(
        walletStatic.createLightningInvoice({
          amountSats: 1000,
          memo: "test",
          expirySeconds: 300,
          includeSparkAddress: true,
          includeSparkInvoice: true,
        }),
      ).rejects.toMatchObject({
        name: SparkValidationError.name,
        message: expect.stringContaining("mutually exclusive"),
        context: expect.objectContaining({
          field: "includeSparkInvoice",
        }),
      });
    }, 30000);
  });

  describe("should create lightning invoice with embedded spark invoice", () => {
    it("should embed spark invoice in fallback address", async () => {
      const invoice = await walletStatic.createLightningInvoice({
        amountSats: 5000,
        memo: "test spark invoice roundtrip",
        expirySeconds: 600,
        includeSparkInvoice: true,
      });

      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);

      // Verify spark invoice is present and valid
      expect(decodedInvoice.fallbackAddress).toBeDefined();
      const sparkInvoice = decodedInvoice.fallbackAddress!;

      expect(sparkInvoice).toMatch(/^(spark[lrts]?|sp[lrts]?)1/);

      // The spark invoice should be reasonable length (150-300 bytes typical)
      expect(sparkInvoice.length).toBeGreaterThan(50);
      expect(sparkInvoice.length).toBeLessThan(400);
    }, 30000);

    it("should pay invoice with embedded spark invoice using preferSpark", async () => {
      const faucet = BitcoinFaucet.getInstance();

      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      const { wallet: bobWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      // Fund Alice's wallet
      const depositAddress = await aliceWallet.getSingleUseDepositAddress();
      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );
      await faucet.mineBlocksAndWaitForMiningToComplete(6);
      await aliceWallet.claimDeposit(signedTx.id);
      await waitForBalance(aliceWallet, DEPOSIT_AMOUNT);

      // Bob creates invoice with embedded spark invoice
      const invoice = await bobWallet.createLightningInvoice({
        amountSats: INVOICE_AMOUNT,
        memo: "test preferSpark with spark invoice",
        expirySeconds: 300,
        includeSparkInvoice: true,
      });

      expect(invoice).toBeDefined();

      // Verify spark invoice is embedded
      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);
      expect(decodedInvoice.fallbackAddress).toBeDefined();
      expect(decodedInvoice.fallbackAddress).toMatch(
        /^(spark[lrts]?|sp[lrts]?)1/,
      );

      // Alice pays with preferSpark - should use embedded spark invoice
      // Register listener before payment so we don't miss the stream event.
      const bobClaimed = waitForClaim({ wallet: bobWallet });
      await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
        preferSpark: true,
      });

      await bobClaimed;

      // Verify Bob received the payment
      const { balance: bobBalance } = await bobWallet.getBalance();
      expect(bobBalance).toBe(BigInt(INVOICE_AMOUNT));

      // Verify Alice's balance decreased (no Lightning fees when using Spark)
      const { balance: aliceBalance } = await aliceWallet.getBalance();
      expect(aliceBalance).toBe(DEPOSIT_AMOUNT - BigInt(INVOICE_AMOUNT));
    }, 120000);

    it("should pay zero-amount lightning invoice with embedded zero-amount spark invoice using preferSpark", async () => {
      const faucet = BitcoinFaucet.getInstance();

      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      const { wallet: bobWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      // Fund Alice's wallet
      const depositAddress = await aliceWallet.getSingleUseDepositAddress();
      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );
      await faucet.mineBlocksAndWaitForMiningToComplete(6);
      await aliceWallet.claimDeposit(signedTx.id);
      await waitForBalance(aliceWallet, DEPOSIT_AMOUNT);

      // Bob creates zero-amount invoice with embedded spark invoice
      const invoice = await bobWallet.createLightningInvoice({
        amountSats: 0,
        memo: "test zero-amount invoice with spark invoice",
        expirySeconds: 300,
        includeSparkInvoice: true,
      });

      expect(invoice).toBeDefined();

      // Verify spark invoice is embedded and is zero-amount
      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);
      expect(decodedInvoice.fallbackAddress).toBeDefined();
      expect(decodedInvoice.fallbackAddress).toMatch(
        /^(spark[lrts]?|sp[lrts]?)1/,
      );
      expect(decodedInvoice.amountMSats).toBe(null);

      const paymentAmount = 5000;

      // Alice pays zero-amount invoice with preferSpark and amountSatsToSend
      // Register listener before payment so we don't miss the stream event.
      const bobClaimed = waitForClaim({ wallet: bobWallet });
      await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
        preferSpark: true,
        amountSatsToSend: paymentAmount,
      });

      await bobClaimed;

      // Verify Bob received the payment
      const { balance: bobBalance } = await bobWallet.getBalance();
      expect(bobBalance).toBe(BigInt(paymentAmount));

      // Verify Alice's balance decreased
      const { balance: aliceBalance } = await aliceWallet.getBalance();
      expect(aliceBalance).toBe(DEPOSIT_AMOUNT - BigInt(paymentAmount));
    }, 120000);
  });

  describe("should validate zero-amount invoice matching", () => {
    it("should successfully pay zero-amount lightning invoice with zero-amount embedded spark invoice", async () => {
      const faucet = BitcoinFaucet.getInstance();

      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      const { wallet: bobWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      // Fund Alice's wallet
      const depositAddress = await aliceWallet.getSingleUseDepositAddress();
      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );
      await faucet.mineBlocksAndWaitForMiningToComplete(6);
      await aliceWallet.claimDeposit(signedTx.id);
      await waitForBalance(aliceWallet, DEPOSIT_AMOUNT);

      // Bob creates zero-amount lightning invoice with embedded zero-amount spark invoice
      const invoice = await bobWallet.createLightningInvoice({
        amountSats: 0,
        memo: "zero-amount test",
        expirySeconds: 300,
        includeSparkInvoice: true,
      });

      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);
      expect(decodedInvoice.amountMSats).toBe(null);
      expect(decodedInvoice.fallbackAddress).toBeDefined();

      const paymentAmount = 3000;

      // Paying with preferSpark should validate that both invoices are zero-amount
      // Register listener before payment so we don't miss the stream event.
      const bobClaimed = waitForClaim({ wallet: bobWallet });
      await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
        preferSpark: true,
        amountSatsToSend: paymentAmount,
      });

      await bobClaimed;

      const { balance: bobBalance } = await bobWallet.getBalance();
      expect(bobBalance).toBe(BigInt(paymentAmount));
    }, 120000);

    it("should successfully pay non-zero lightning invoice with matching non-zero embedded spark invoice", async () => {
      const faucet = BitcoinFaucet.getInstance();

      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      const { wallet: bobWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      // Fund Alice's wallet
      const depositAddress = await aliceWallet.getSingleUseDepositAddress();
      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );
      await faucet.mineBlocksAndWaitForMiningToComplete(6);
      await aliceWallet.claimDeposit(signedTx.id);
      await waitForBalance(aliceWallet, DEPOSIT_AMOUNT);

      const invoiceAmount = 2000;

      // Bob creates non-zero lightning invoice with embedded matching non-zero spark invoice
      const invoice = await bobWallet.createLightningInvoice({
        amountSats: invoiceAmount,
        memo: "non-zero matching test",
        expirySeconds: 300,
        includeSparkInvoice: true,
      });

      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);
      expect(decodedInvoice.amountMSats).toBe(BigInt(invoiceAmount * 1000));
      expect(decodedInvoice.fallbackAddress).toBeDefined();

      // Paying with preferSpark should validate that amounts match
      // Register listener before payment so we don't miss the stream event.
      const bobClaimed = waitForClaim({ wallet: bobWallet });
      await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
        preferSpark: true,
      });

      await bobClaimed;

      const { balance: bobBalance } = await bobWallet.getBalance();
      expect(bobBalance).toBe(BigInt(invoiceAmount));
    }, 120000);
  });

  describe("should create lightning invoice with receiverIdentityPubkey", () => {
    it("should create signed spark invoice when receiverIdentityPubkey is not provided", async () => {
      const { wallet } = await SparkWalletTestingWithStream.initialize({
        options: { network: "LOCAL" },
      });

      const invoice = await wallet.createLightningInvoice({
        amountSats: 5000,
        memo: "test receiverIdentityPubkey default",
        expirySeconds: 600,
        includeSparkInvoice: true,
      });

      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);
      expect(decodedInvoice.fallbackAddress).toBeDefined();
      const sparkInvoice = decodedInvoice.fallbackAddress!;
      expect(invoice.sparkInvoice).toBe(sparkInvoice);

      const decodedSparkInvoice = decodeSparkAddress(sparkInvoice, "LOCAL");

      const creatorIdentityPubkey = await wallet.getIdentityPublicKey();

      expect(decodedSparkInvoice.identityPublicKey).toBe(creatorIdentityPubkey);
      expect(decodedSparkInvoice.signature).toBeDefined();

      validateSparkInvoiceSignature(sparkInvoice as SparkAddressFormat);
    }, 30000);

    it("should create signed spark invoice when receiverIdentityPubkey matches creator", async () => {
      const { wallet } = await SparkWalletTestingWithStream.initialize({
        options: { network: "LOCAL" },
      });

      const creatorIdentityPubkey = await wallet.getIdentityPublicKey();

      const invoice = await wallet.createLightningInvoice({
        amountSats: 5000,
        memo: "test receiverIdentityPubkey same as creator",
        expirySeconds: 600,
        includeSparkInvoice: true,
        receiverIdentityPubkey: creatorIdentityPubkey,
      });

      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);
      expect(decodedInvoice.fallbackAddress).toBeDefined();
      const sparkInvoice = decodedInvoice.fallbackAddress!;
      expect(invoice.sparkInvoice).toBe(sparkInvoice);

      const decodedSparkInvoice = decodeSparkAddress(sparkInvoice, "LOCAL");

      expect(decodedSparkInvoice.identityPublicKey).toBe(creatorIdentityPubkey);
      expect(decodedSparkInvoice.signature).toBeDefined();

      validateSparkInvoiceSignature(sparkInvoice as SparkAddressFormat);
    }, 30000);

    it("should create unsigned spark invoice when receiverIdentityPubkey differs from creator", async () => {
      const { wallet: creatorWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      const { wallet: receiverWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      const creatorIdentityPubkey = await creatorWallet.getIdentityPublicKey();
      const receiverIdentityPubkey =
        await receiverWallet.getIdentityPublicKey();

      expect(creatorIdentityPubkey).not.toBe(receiverIdentityPubkey);

      const invoice = await creatorWallet.createLightningInvoice({
        amountSats: 5000,
        memo: "test receiverIdentityPubkey different from creator",
        expirySeconds: 600,
        includeSparkInvoice: true,
        receiverIdentityPubkey: receiverIdentityPubkey,
      });

      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);
      expect(decodedInvoice.fallbackAddress).toBeDefined();
      const sparkInvoice = decodedInvoice.fallbackAddress!;
      expect(invoice.sparkInvoice).toBe(sparkInvoice);

      const decodedSparkInvoice = decodeSparkAddress(sparkInvoice, "LOCAL");

      expect(decodedSparkInvoice.identityPublicKey).toBe(
        receiverIdentityPubkey,
      );
      expect(decodedSparkInvoice.signature).toBeUndefined();

      expect(() =>
        validateSparkInvoiceSignature(sparkInvoice as SparkAddressFormat),
      ).toThrow(SparkValidationError);
    }, 30000);
  });

  describe("creating an invoice with receiverIdentityPubkey", () => {
    it("should successfully create and pay an invoice with receiverIdentityPubkey", async () => {
      const faucet = BitcoinFaucet.getInstance();

      const { wallet: alice } = await SparkWalletTestingWithStream.initialize({
        options: { network: "LOCAL" },
      });

      const { wallet: bob } = await SparkWalletTestingWithStream.initialize({
        options: { network: "LOCAL" },
      });

      const depositAddress = await alice.getSingleUseDepositAddress();
      expect(depositAddress).toBeDefined();

      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

      // Wait for the transaction to be mined
      await faucet.mineBlocksAndWaitForMiningToComplete(6);

      await alice.claimDeposit(signedTx.id);
      await waitForBalance(alice, DEPOSIT_AMOUNT);

      const invoice = await alice.createLightningInvoice({
        amountSats: 1000,
        memo: "test invoice",
        expirySeconds: 600,
        receiverIdentityPubkey: await bob.getIdentityPublicKey(),
      });

      expect(invoice).toBeDefined();

      // Register listener before payment so we don't miss the stream event.
      const bobClaimed = waitForClaim({ wallet: bob });
      await alice.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
      });

      await bobClaimed;

      const { balance: bobBalance } = await bob.getBalance();
      expect(bobBalance).toBe(BigInt(1000));
    }, 120_000);
  });

  // A Spark wallet paying another Spark wallet's locally-issued invoice is an
  // "internal" payment: both legs live on this SSP. The SSP may serve it via the
  // unified internal-payment state machine (when the sparkcore knob
  // spark.ssp.internal_lightning_payment.state_machine.enabled is on) or via the
  // legacy split send/receive flow (when off). This test asserts only the
  // user-visible outcome — the receiver is paid and the sender's send request
  // reaches TRANSFER_COMPLETED — so it must pass identically in both knob states.
  //
  // The sender-status assertion is the load-bearing part. The internal flow never
  // advances the SparkLightningSendRequest row itself (the internal-payment row is
  // the source of truth), so the send request's status must be *surfaced* from
  // that row. Without that surfacing the send request sits at CREATED forever even
  // after the payment settles, and this poll times out — which is exactly why the
  // status surfacing must land before the knob is enabled. Internal COMPLETED and
  // the legacy flow both map onto TRANSFER_COMPLETED, so the assertion is
  // knob-agnostic.
  describe("internal (Spark-to-Spark) lightning payment", () => {
    it("pays a locally-issued Spark invoice; receiver is paid and sender request completes", async () => {
      const faucet = BitcoinFaucet.getInstance();

      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });
      const { wallet: bobWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      // Fund Alice so she has leaves to pay with.
      const depositAddress = await aliceWallet.getSingleUseDepositAddress();
      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );
      await faucet.mineBlocksAndWaitForMiningToComplete(6);
      await aliceWallet.claimDeposit(signedTx.id);
      await waitForBalance(aliceWallet, DEPOSIT_AMOUNT);

      // Bob issues a locally-issued Spark invoice (SIGNING_OPERATOR_SWAP
      // receive), which is what makes Alice's payment eligible for the internal
      // flow.
      const invoice = await bobWallet.createLightningInvoice({
        amountSats: INVOICE_AMOUNT,
        memo: "internal payment test",
        expirySeconds: 500,
      });
      expect(invoice.status).toEqual(
        LightningReceiveRequestStatus.INVOICE_CREATED,
      );

      // Register the receiver's claim listener before paying so we don't miss it.
      // throwOnTimeout so a missing claim surfaces as a clear timeout rather than a
      // misleading "expected 1000n, got 0n" balance assertion downstream.
      const bobClaimed = waitForClaim({
        wallet: bobWallet,
        throwOnTimeout: true,
      });

      const payResult = await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
      });

      // Paying over Lightning (no preferSpark, no embedded spark invoice)
      // returns the SparkLightningSendRequest, not a settled Spark WalletTransfer
      // (which uniquely carries transferDirection).
      expect("transferDirection" in payResult).toBe(false);
      const sendRequest = payResult as LightningSendRequest;
      expect(sendRequest.id).toBeDefined();

      // Receiver leg: Bob is credited the invoice amount.
      await bobClaimed;
      const { balance: bobBalance } = await bobWallet.getBalance();
      expect(bobBalance).toBe(BigInt(INVOICE_AMOUNT));

      // Sender leg: the send request surfaces terminal success. TRANSFER_COMPLETED
      // is reached by both the legacy flow and the internal flow, so this holds
      // regardless of the knob.
      const completed = await waitForSendRequestStatus(
        aliceWallet,
        sendRequest.id,
        LightningSendRequestStatus.TRANSFER_COMPLETED,
        { maxAttempts: 30, delayMs: 2000 },
      );
      expect(completed.status).toEqual(
        LightningSendRequestStatus.TRANSFER_COMPLETED,
      );
      const completedReceive = await waitForReceiveRequestStatus(
        bobWallet,
        invoice.id,
        LightningReceiveRequestStatus.TRANSFER_COMPLETED,
        { maxAttempts: 30, delayMs: 2000 },
      );
      expect(completedReceive.status).toEqual(
        LightningReceiveRequestStatus.TRANSFER_COMPLETED,
      );

      // Sender was debited the payment amount (plus the SSP fee).
      const { balance: aliceBalance } = await aliceWallet.getBalance();
      expect(aliceBalance).toBeLessThan(
        DEPOSIT_AMOUNT - BigInt(INVOICE_AMOUNT),
      );
    }, 180_000);

    it("advances an idempotent SSP release after the receiver supplies SO proof", async () => {
      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });
      const { wallet: bobWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      await fundWallet(aliceWallet);

      await withSparkcoreKnobs(SSP_RELEASE_KNOBS, async () => {
        const preimage = randomPreimage();
        const wrongPreimage = new Uint8Array(preimage);
        wrongPreimage.set([wrongPreimage[0]! ^ 1], 0);
        const invoice = await bobWallet.createLightningHodlInvoice({
          amountSats: INVOICE_AMOUNT,
          paymentHash: paymentHashForPreimage(preimage),
          memo: "internal hodl ssp release test",
          expirySeconds: 500,
        });
        expect(invoice.status).toEqual(
          LightningReceiveRequestStatus.INVOICE_CREATED,
        );

        const payResult = await aliceWallet.payLightningInvoice({
          invoice: invoice.invoice.encodedInvoice,
          maxFeeSats: 100,
        });
        expect("transferDirection" in payResult).toBe(false);

        const sendRequest = payResult as LightningSendRequest;
        expect(sendRequest.id).toBeDefined();

        const initiated = await waitForSendRequestStatus(
          aliceWallet,
          sendRequest.id,
          LightningSendRequestStatus.LIGHTNING_PAYMENT_INITIATED,
          { maxAttempts: 30, delayMs: 2000, timeoutMs: 60_000 },
        );
        expect(initiated.status).toEqual(
          LightningSendRequestStatus.LIGHTNING_PAYMENT_INITIATED,
        );

        const invoiceId = await getLightningInvoiceId(invoice.id);
        await withSspAuthorization(invoiceId, async (headers) => {
          await expect(
            releasePaymentPreimage(invoiceId, wrongPreimage, headers),
          ).rejects.toThrow("Preimage does not match payment hash");

          const releasedInvoiceId = await releasePaymentPreimage(
            invoiceId,
            preimage,
            headers,
          );
          expect(releasedInvoiceId).toBe(invoiceId);

          const { balance: bobBalanceBeforeProof } =
            await bobWallet.getBalance();
          expect(bobBalanceBeforeProof).toBe(0n);

          // The SSP's invoice preimage is a release signal, not proof that the
          // receiver-side SO swap can settle.
          await retryUntilSuccess(
            () => bobWallet.claimHTLC(bytesToHex(preimage)),
            {
              maxAttempts: 30,
              delayMs: 2000,
              timeoutMs: 60_000,
            },
          );

          const retriedInvoiceId = await releasePaymentPreimage(
            invoiceId,
            preimage,
            headers,
          );
          expect(retriedInvoiceId).toBe(invoiceId);
          await waitForBalance(bobWallet, BigInt(INVOICE_AMOUNT), 90_000);

          const { balance: bobBalance } = await bobWallet.getBalance();
          expect(bobBalance).toBe(BigInt(INVOICE_AMOUNT));

          const [completed, completedReceive] = await Promise.all([
            waitForSendRequestStatus(
              aliceWallet,
              sendRequest.id,
              LightningSendRequestStatus.TRANSFER_COMPLETED,
              { maxAttempts: 90, delayMs: 1000, timeoutMs: 90_000 },
            ),
            waitForReceiveRequestStatus(
              bobWallet,
              invoice.id,
              LightningReceiveRequestStatus.TRANSFER_COMPLETED,
              { maxAttempts: 90, delayMs: 1000, timeoutMs: 90_000 },
            ),
          ]);
          expect(completed.status).toEqual(
            LightningSendRequestStatus.TRANSFER_COMPLETED,
          );
          expect(completedReceive.status).toEqual(
            LightningReceiveRequestStatus.TRANSFER_COMPLETED,
          );
        });
      });
    }, 240_000);

    it("refunds the sender after a locally-issued HODL swap expires", async () => {
      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });
      const { wallet: bobWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      await fundWallet(aliceWallet);

      const preimage = randomPreimage();
      await withSparkcoreKnobs(EXPIRY_REFUND_KNOBS, async () => {
        const invoice = await bobWallet.createLightningHodlInvoice({
          amountSats: INVOICE_AMOUNT,
          paymentHash: paymentHashForPreimage(preimage),
          memo: "internal hodl expiry refund test",
          expirySeconds: 60,
        });
        expect(invoice.status).toEqual(
          LightningReceiveRequestStatus.INVOICE_CREATED,
        );

        const payResult = await aliceWallet.payLightningInvoice({
          invoice: invoice.invoice.encodedInvoice,
          maxFeeSats: 100,
        });
        expect("transferDirection" in payResult).toBe(false);

        const sendRequest = payResult as LightningSendRequest;
        expect(sendRequest.id).toBeDefined();

        const initiated = await waitForSendRequestStatus(
          aliceWallet,
          sendRequest.id,
          LightningSendRequestStatus.LIGHTNING_PAYMENT_INITIATED,
          { maxAttempts: 30, delayMs: 2000, timeoutMs: 60_000 },
        );
        expect(initiated.status).toEqual(
          LightningSendRequestStatus.LIGHTNING_PAYMENT_INITIATED,
        );

        const { balance: bobBalanceBeforeRefund } =
          await bobWallet.getBalance();
        expect(bobBalanceBeforeRefund).toBe(0n);

        const canceledReceive = await waitForInternalReceiveRequestStatus(
          invoice.id,
          "TRANSFER_CANCELED",
          {
            maxAttempts: PREIMAGE_SWAP_EXPIRY_TIMEOUT_MS / 1000,
            delayMs: 1000,
            timeoutMs: PREIMAGE_SWAP_EXPIRY_TIMEOUT_MS,
          },
        );
        expect(canceledReceive).toBe("TRANSFER_CANCELED");

        // Server-driven cancellation may complete without a sender stream event,
        // so refresh from the SO before checking the returned leaves.
        await retryUntilSuccess(
          async () => {
            await aliceWallet.experimental_syncWallet();
            const { balance } = await aliceWallet.getBalance();
            if (balance !== DEPOSIT_AMOUNT) {
              throw new Error(
                `sender refund is not available yet: expected ${DEPOSIT_AMOUNT}, got ${balance}`,
              );
            }
          },
          { maxAttempts: 45, delayMs: 2000, timeoutMs: 90_000 },
        );

        const { balance: bobBalance } = await bobWallet.getBalance();
        expect(bobBalance).toBe(0n);

        const { balance: aliceBalance } = await aliceWallet.getBalance();
        expect(aliceBalance).toBe(DEPOSIT_AMOUNT);

        const terminalSend = await aliceWallet.getLightningSendRequest(
          sendRequest.id,
        );
        // The legacy observer can race the unified cancellation when stamping
        // this compatibility status; the restored balance proves the refund.
        expect([
          LightningSendRequestStatus.USER_SWAP_RETURNED,
          LightningSendRequestStatus.LIGHTNING_PAYMENT_FAILED,
        ]).toContain(terminalSend?.status);
      });
    }, 660_000);
  });
});
