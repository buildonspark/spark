import { describe, expect, it, jest } from "@jest/globals";
import { Mutex } from "async-mutex";
import SspClient from "../graphql/client.js";
import { lightningReceiveQuoteDocument } from "../graphql/mutations/LightningReceiveQuote.js";
import { requestLightningReceiveDocument } from "../graphql/mutations/RequestLightningReceive.js";
import { BitcoinNetwork } from "../graphql/objects/index.js";
import { LightningReceiveQuoteOutputFromJson } from "../graphql/receive-quote.js";
import { ReceiveQuoteAmountBasis } from "../utils/receive-quote.js";

// Both documents are built rather than fixed so that a field absent from an
// older SSP schema is never named. Losing a branch is silent: the omitting form
// still executes, so a dropped committed_quote would sign a manifest and
// invoice its gross while the SSP is never told the quote was committed.

describe("conditionally built receive documents", () => {
  it("names the committed quote only when one is sent", () => {
    const withQuote = requestLightningReceiveDocument(true);
    expect(withQuote).toContain("$committed_quote: CommittedQuoteInput");
    expect(withQuote).toContain("committed_quote: $committed_quote");

    expect(requestLightningReceiveDocument(false)).not.toContain(
      "committed_quote",
    );
  });

  it("names the amount basis only when one is pinned", () => {
    const withBasis = lightningReceiveQuoteDocument(true);
    expect(withBasis).toContain("$amount_basis: SparkAmountBasis!");
    expect(withBasis).toContain("amount_basis: $amount_basis");

    expect(lightningReceiveQuoteDocument(false)).not.toContain("amount_basis");
  });

  it("keeps the fields both forms always need", () => {
    for (const document of [
      requestLightningReceiveDocument(true),
      requestLightningReceiveDocument(false),
    ]) {
      expect(document).toContain("mutation RequestLightningReceive");
      expect(document).toContain("payment_hash: $payment_hash");
      expect(document).toContain("...LightningReceiveRequestFragment");
    }

    for (const document of [
      lightningReceiveQuoteDocument(true),
      lightningReceiveQuoteDocument(false),
    ]) {
      expect(document).toContain("mutation LightningReceiveQuote");
      // The flat serialized_manifest/issuer_signature fields are deprecated on
      // the SSP and already removed from its rc schema.
      expect(document).toContain("issued_quote {");
      expect(document).toContain("attribution_status");
    }
  });
});

describe("variables sent alongside the built documents", () => {
  const clientCapturing = (calls: Record<string, unknown>[]) => {
    const client = Object.create(SspClient.prototype) as SspClient;
    const internals = client as unknown as {
      executeRawQuery: (q: { variables?: Record<string, unknown> }) => unknown;
      partnerJwtLock: Mutex;
    };
    // Object.create skips field initializers, and the quote path takes this lock.
    internals.partnerJwtLock = new Mutex();
    internals.executeRawQuery = jest.fn(
      (q: { variables?: Record<string, unknown> }) => {
        calls.push(q.variables ?? {});
        return Promise.resolve(null);
      },
    );
    return client;
  };

  it("omits the committed quote key entirely when none is sent", async () => {
    // The requester rewrites an undefined variable to null rather than
    // dropping it, so an explicit undefined would still reach the wire as a
    // committed_quote the document never declares.
    const calls: Record<string, unknown>[] = [];
    await clientCapturing(calls).requestLightningReceive({
      amountSats: 1000,
      network: BitcoinNetwork.REGTEST,
      paymentHash: "00",
      includeSparkAddress: false,
    });

    expect(calls[0]).not.toHaveProperty("committed_quote");
  });

  it("sends the committed quote in snake_case when one is present", async () => {
    const calls: Record<string, unknown>[] = [];
    await clientCapturing(calls).requestLightningReceive({
      amountSats: 1000,
      network: BitcoinNetwork.REGTEST,
      paymentHash: "00",
      includeSparkAddress: false,
      committedQuote: {
        serializedManifest: "aa",
        issuerSignature: "bb",
        manifestSignature: "cc",
      },
    });

    expect(calls[0]?.["committed_quote"]).toEqual({
      serialized_manifest: "aa",
      issuer_signature: "bb",
      manifest_signature: "cc",
    });
  });

  it("omits the amount basis unless GROSS is pinned", async () => {
    const calls: Record<string, unknown>[] = [];
    const client = clientCapturing(calls);
    await client.lightningReceiveQuote({
      amountSats: 1000,
      network: BitcoinNetwork.REGTEST,
    });
    await client.lightningReceiveQuote({
      amountSats: 1000,
      network: BitcoinNetwork.REGTEST,
      amountBasis: ReceiveQuoteAmountBasis.GROSS,
    });

    expect(calls[0]).not.toHaveProperty("amount_basis");
    expect(calls[1]?.["amount_basis"]).toBe(ReceiveQuoteAmountBasis.GROSS);
  });
});

describe("parsing the SSP's quote response", () => {
  it("maps the snake_case wire shape onto the SDK's fields", () => {
    // The outbound direction is asserted above; without this a typo'd key
    // (issuedQuote for issued_quote) would surface only against a live SSP.
    expect(
      LightningReceiveQuoteOutputFromJson({
        issued_quote: {
          serialized_manifest: "0a01",
          issuer_signature: "aabb",
        },
        attribution_status: "ATTRIBUTED",
      }),
    ).toEqual({
      issuedQuote: { serializedManifest: "0a01", issuerSignature: "aabb" },
      attributionStatus: "ATTRIBUTED",
    });
  });
});
