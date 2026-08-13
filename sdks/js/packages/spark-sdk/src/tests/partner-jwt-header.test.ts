import { describe, expect, it, jest } from "@jest/globals";
import { Mutex } from "async-mutex";
import SspClient from "../graphql/client.js";
import { setFetch, type SparkFetch } from "../utils/fetch.js";
import type { SparkSigner } from "../signer/signer.js";

// The partner token is a third-party bearer credential with no per-request
// channel through the shared requester, so it is held on the client for the
// duration of one quote call. These pin what that has to keep true: the token
// reaches the quote request, nothing else, and never another caller.

const SSP_ORIGIN = "https://ssp.example.com/graphql";

function harness() {
  const client = Object.create(SspClient.prototype) as SspClient;
  const internals = client as unknown as {
    withPartnerJwt: (
      input: RequestInfo | URL,
      init?: RequestInit,
    ) => RequestInit | undefined;
    executeRawQuery: () => Promise<null>;
    partnerJwtLock: Mutex;
    partnerJwt?: string;
    sspBaseUrl: string;
    headersImpl: typeof Headers;
  };
  // Object.create skips field initializers, so the lock and the configured
  // origin the gate compares against have to be supplied.
  internals.partnerJwtLock = new Mutex();
  internals.sspBaseUrl = SSP_ORIGIN;
  // Tagged so a test can prove the injected constructor is the one used: bare
  // supplies its own through setFetch, and reaching for globalThis.Headers
  // breaks that runtime while passing everywhere else.
  internals.headersImpl = class TaggedHeaders extends Headers {
    readonly injected = true;
  };

  const jwtFor = (operation: string) =>
    new Headers(
      internals.withPartnerJwt(SSP_ORIGIN, {
        headers: { "X-GraphQL-Operation": operation },
      })?.headers,
    ).get("x-partner-jwt");

  return { client, internals, jwtFor };
}

describe("partner JWT header scoping", () => {
  it("reaches the quote operation and nothing else", () => {
    const { internals, jwtFor } = harness();
    internals.partnerJwt = "token";

    expect(jwtFor("LightningReceiveQuote")).toBe("token");
    expect(jwtFor("RequestLightningReceive")).toBeNull();
    expect(jwtFor("RequestLightningSend")).toBeNull();
  });

  it("refuses redirects wherever the token is attached", () => {
    // A redirect would replay the credential onto whatever it names, so the
    // refusal is not carved out by scheme — including for a plaintext endpoint,
    // which is already putting a bearer token on the wire in cleartext.
    const { internals } = harness();
    internals.partnerJwt = "token";

    const initFor = (baseUrl: string) => {
      internals.sspBaseUrl = baseUrl;
      return internals.withPartnerJwt(baseUrl, {
        headers: { "X-GraphQL-Operation": "LightningReceiveQuote" },
      });
    };

    for (const base of ["https://ssp.example.com", "http://127.0.0.1:5000"]) {
      expect(initFor(base)?.redirect).toBe("error");
      expect(new Headers(initFor(base)?.headers).get("x-partner-jwt")).toBe(
        "token",
      );
    }
  });

  it("leaves a request it does not attach to untouched", () => {
    // No redirect mode imposed on anything the token does not ride on.
    const { internals } = harness();
    internals.partnerJwt = "token";
    internals.sspBaseUrl = "https://ssp.example.com";

    const other = internals.withPartnerJwt("https://ssp.example.com", {
      headers: { "X-GraphQL-Operation": "RequestLightningReceive" },
    });

    expect(other?.redirect).toBeUndefined();
  });

  it("builds the header set with the runtime's constructor", () => {
    // globalThis.Headers is present under Node and absent under bare, so
    // reaching for it fails only in the runtime no test here exercises.
    const { internals } = harness();
    internals.partnerJwt = "token";

    const attached = internals.withPartnerJwt(SSP_ORIGIN, {
      headers: { "X-GraphQL-Operation": "LightningReceiveQuote" },
    });

    expect((attached?.headers as { injected?: boolean }).injected).toBe(true);
  });

  it("matches an SSP base url whose scheme is upper or mixed case", () => {
    // URL schemes are case-insensitive; a case-sensitive test would prefix
    // https:// again and silently stop attaching the token.
    const { internals, jwtFor } = harness();
    internals.partnerJwt = "token";
    internals.sspBaseUrl = "HTTPS://ssp.example.com";

    expect(jwtFor("LightningReceiveQuote")).toBe("token");
  });

  it("matches an SSP base url that carries no protocol", () => {
    // The requester prepends https:// before sending, so an origin comparison
    // that skips the same normalization silently drops attribution. Checked
    // here rather than over a real request: a protocol-less base url does not
    // complete one in this harness, which would hide the gate under a retry.
    const { internals, jwtFor } = harness();
    internals.sspBaseUrl = "ssp.example.com";
    internals.partnerJwt = "token";

    expect(jwtFor("LightningReceiveQuote")).toBe("token");
  });

  it("clears the token when the call that set it returns", async () => {
    const { client, internals, jwtFor } = harness();
    internals.executeRawQuery = () => Promise.resolve(null);

    await client.lightningReceiveQuote({
      amountSats: 1,
      network: "REGTEST" as never,
      partnerJwt: "token",
    });

    expect(jwtFor("LightningReceiveQuote")).toBeNull();
  });

  it("clears the token when the call that set it throws", async () => {
    const { client, internals, jwtFor } = harness();
    internals.executeRawQuery = () => Promise.reject(new Error("ssp down"));

    await expect(
      client.lightningReceiveQuote({
        amountSats: 1,
        network: "REGTEST" as never,
        partnerJwt: "token",
      }),
    ).rejects.toThrow("ssp down");

    expect(jwtFor("LightningReceiveQuote")).toBeNull();
  });

  it("attaches each call's own token and never another's", async () => {
    // Attribution, not counts: a clear moved into the wrong place hands the
    // token to the NEXT queued caller, which a tally of tokens cannot see.
    const seen: { amountSats: number; jwt: string | null }[] = [];
    const { client, internals, jwtFor } = harness();
    let inFlight = 0;
    internals.executeRawQuery = async () => {
      const amountSats = ++inFlight;
      await new Promise((resolve) => setTimeout(resolve, 0));
      seen.push({ amountSats, jwt: jwtFor("LightningReceiveQuote") });
      return null;
    };

    await Promise.all([
      client.lightningReceiveQuote({
        amountSats: 1,
        network: "REGTEST" as never,
        partnerJwt: "first",
      }),
      client.lightningReceiveQuote({
        amountSats: 2,
        network: "REGTEST" as never,
      }),
      client.lightningReceiveQuote({
        amountSats: 3,
        network: "REGTEST" as never,
        partnerJwt: "third",
      }),
    ]);

    expect(seen).toEqual([
      { amountSats: 1, jwt: "first" },
      { amountSats: 2, jwt: null },
      { amountSats: 3, jwt: "third" },
    ]);
  });
});

describe("partner JWT over a real request", () => {
  const SSP_URL = "https://ssp.example.com";

  const quoteResponse = () =>
    new Response(
      JSON.stringify({
        data: {
          lightning_receive_quote: {
            issued_quote: {
              serialized_manifest: "00",
              issuer_signature: "aa",
            },
            attribution_status: "ATTRIBUTED",
          },
        },
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    );

  const clientOverFetch = (mockFetch: jest.Mock<SparkFetch>) => {
    setFetch(mockFetch, globalThis.Headers);
    const client = new SspClient({
      signer: {} as SparkSigner,
      sspClientOptions: {
        baseUrl: SSP_URL,
        identityPublicKey: "02".padEnd(66, "a"),
      },
    });
    // Authentication is out of scope here; the quote request itself is.
    (
      client as unknown as {
        authProvider: { isAuthorized: () => Promise<boolean> };
      }
    ).authProvider.isAuthorized = () => Promise.resolve(true);
    return client;
  };

  const sentPartnerJwt = (mockFetch: jest.Mock<SparkFetch>) => {
    const headers = mockFetch.mock.calls[0]?.[1]?.headers;
    return new Headers(headers as HeadersInit | undefined).get("x-partner-jwt");
  };

  it("puts the token on the wire for an attributed quote", async () => {
    // Drives the whole path — including the operation name the requester derives
    // from the built document, which the gate matches on. A hand-set header
    // would keep passing if that name or the attach point drifted.
    const mockFetch = jest
      .fn<SparkFetch>()
      .mockImplementation(() => Promise.resolve(quoteResponse()));

    await clientOverFetch(mockFetch).lightningReceiveQuote({
      amountSats: 1000,
      network: "REGTEST" as never,
      partnerJwt: "token",
    });

    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(sentPartnerJwt(mockFetch)).toBe("token");
  });

  it("sends no token when none was supplied", async () => {
    const mockFetch = jest
      .fn<SparkFetch>()
      .mockImplementation(() => Promise.resolve(quoteResponse()));

    await clientOverFetch(mockFetch).lightningReceiveQuote({
      amountSats: 1000,
      network: "REGTEST" as never,
    });

    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(sentPartnerJwt(mockFetch)).toBeNull();
  });

  it("sends no token to an origin that is not the configured SSP", async () => {
    const mockFetch = jest
      .fn<SparkFetch>()
      .mockImplementation(() => Promise.resolve(quoteResponse()));
    // Built through the helper so the mock is installed BEFORE the client
    // captures a fetch: constructing first leaves the request going to some
    // earlier mock, and an uncalled mock reports no header either way.
    const client = clientOverFetch(mockFetch);
    (client as unknown as { sspBaseUrl: string }).sspBaseUrl =
      "https://elsewhere.example.com";

    await client.lightningReceiveQuote({
      amountSats: 1000,
      network: "REGTEST" as never,
      partnerJwt: "token",
    });

    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(sentPartnerJwt(mockFetch)).toBeNull();
  });
});
