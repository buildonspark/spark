/**
 * The SSP's receive-quote wire types.
 *
 * Hand-written and deliberately not under `objects/`: that directory holds
 * generator output and is excluded from lint and formatting, so a hand-authored
 * file there would silently drift.
 */

/** The SSP's quote, carried verbatim into the invoice request. */
interface IssuedReceiveQuote {
  /** The quoted TransferManifest as hex-encoded proto bytes. */
  serializedManifest: string;
  /** The SSP's signature over the quote envelope, hex. */
  issuerSignature: string;
}

interface LightningReceiveQuoteOutput {
  issuedQuote: IssuedReceiveQuote;
  /**
   * Whether an `x-partner-jwt` resolved to a fee attribution, and if not, why.
   * Advisory only — the markup actually applied is in the signed manifest's
   * fees[], so this is for diagnosing a quote that came back feeless when it
   * should not have. Left as a string so a new SSP-side status reaches callers
   * instead of failing to parse.
   */
  attributionStatus: string;
}

/**
 * What the receiver echoes back on `request_lightning_receive`.
 *
 * The quote is stateless: the SSP re-verifies its own signature over these exact
 * bytes rather than looking anything up, so `serializedManifest` must be the
 * bytes it issued and not a re-encoded copy.
 */
export interface CommittedQuoteInput {
  serializedManifest: string;
  issuerSignature: string;
  /** The receiver's identity-key signature over the manifest hash, hex. */
  manifestSignature: string;
}

/** The snake_case shapes as they appear on the wire. */
export interface CommittedQuoteInputWire {
  serialized_manifest: string;
  issuer_signature: string;
  manifest_signature: string;
}

export interface LightningReceiveQuoteOutputWire {
  issued_quote: {
    serialized_manifest: string;
    issuer_signature: string;
  };
  attribution_status: string;
}

export const CommittedQuoteInputToJson = (
  obj: CommittedQuoteInput,
): CommittedQuoteInputWire => {
  return {
    serialized_manifest: obj.serializedManifest,
    issuer_signature: obj.issuerSignature,
    manifest_signature: obj.manifestSignature,
  };
};

export const LightningReceiveQuoteOutputFromJson = (
  obj: LightningReceiveQuoteOutputWire,
): LightningReceiveQuoteOutput => {
  return {
    issuedQuote: {
      serializedManifest: obj.issued_quote.serialized_manifest,
      issuerSignature: obj.issued_quote.issuer_signature,
    },
    attributionStatus: obj.attribution_status,
  };
};

export type { IssuedReceiveQuote, LightningReceiveQuoteOutput };
