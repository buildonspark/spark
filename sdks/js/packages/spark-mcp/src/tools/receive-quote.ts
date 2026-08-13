import {
  manifestFeeSats,
  manifestGrossSats,
  ReceiveQuoteAmountBasis,
  type LightningReceiveQuote,
  type SparkWallet,
} from "@buildonspark/spark-sdk";
import {
  TransferManifest,
  type ManifestAmount,
} from "@buildonspark/spark-sdk/proto/spark";
import {
  errorMessage,
  rawResult,
  type OutputMode,
  type ToolResult,
} from "../utils.js";
import { resolveWallet } from "../wallet.js";

type ResolveFn = (mnemonic?: string) => Promise<SparkWallet>;

const basisOf = (amountBasis?: string) =>
  amountBasis === "GROSS"
    ? ReceiveQuoteAmountBasis.GROSS
    : ReceiveQuoteAmountBasis.NET;

// Buffer.from(..., "hex") truncates at the first non-hex character instead of
// failing, so garbage would decode to an empty manifest and be sent as a quote.
const hexToBytes = (hex: string, field: string) => {
  if (hex.length === 0 || hex.length % 2 !== 0 || !/^[0-9a-fA-F]+$/.test(hex)) {
    throw new Error(`${field} is not valid hex`);
  }
  return Uint8Array.from(Buffer.from(hex, "hex"));
};
const bytesToHex = (bytes: Uint8Array) => Buffer.from(bytes).toString("hex");

// A quote is only inspectable if an amount it cannot render says so: reporting
// a bps amount as an absent sats value reads as zero to an agent.
const amountOf = (amount: ManifestAmount | undefined) => {
  switch (amount?.amount?.$case) {
    case "sats":
      return { sats: amount.amount.sats };
    case "bps":
      return { bps: amount.amount.bps };
    default:
      return { unset: true };
  }
};

function describeQuote(quote: LightningReceiveQuote) {
  const manifest = quote.manifest;
  return {
    serializedManifest: quote.serializedManifest,
    issuerSignature: quote.issuerSignature,
    attributionStatus: quote.attributionStatus,
    manifestTransferId: manifest.transferId,
    quoteExpiryTime: manifest.quoteExpiryTime?.toISOString(),
    requestedSats: quote.amountSats,
    amountBasis: quote.amountBasis,
    invoicedSats: manifestGrossSats(manifest),
    feeSats: manifestFeeSats(manifest),
    edges: manifest.edges.map((edge) => ({
      receiver: bytesToHex(edge.receiverIdentityPublicKey),
      ...amountOf(edge.amount),
    })),
    fees: manifest.fees.map((fee) => ({
      receiver: bytesToHex(fee.receiverIdentityPublicKey),
      role: fee.role,
      ...amountOf(fee.amount),
    })),
  };
}

export async function handleLightningReceiveQuote(
  amountSats: number,
  amountBasis?: string,
  partnerJwt?: string,
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const wallet = await resolve(mnemonic);
    const quote = await wallet.getLightningReceiveQuote({
      amountSats,
      amountBasis: basisOf(amountBasis),
      partnerJwt,
    });

    if (output === "raw") return rawResult(quote);

    return rawResult(describeQuote(quote));
  } catch (err) {
    return {
      content: [{ type: "text", text: `Error: ${errorMessage(err)}` }],
      isError: true,
    };
  }
}

/**
 * Rebuild a quote from the values a previous quote call printed.
 *
 * The manifest is decoded from the echoed bytes rather than carried as an
 * object, so what is checked and signed is exactly what the SSP issued.
 */
function quoteFromWire(
  serializedManifest: string,
  issuerSignature: string,
  amountSats: number,
  amountBasis?: string,
): LightningReceiveQuote {
  hexToBytes(issuerSignature, "issuerSignature");
  return {
    serializedManifest,
    issuerSignature,
    manifest: TransferManifest.decode(
      hexToBytes(serializedManifest, "serializedManifest"),
    ),
    amountSats,
    amountBasis: basisOf(amountBasis),
  };
}

async function issueQuotedInvoice(
  wallet: SparkWallet,
  quote: LightningReceiveQuote,
  memo: string | undefined,
  output: OutputMode,
): Promise<ToolResult> {
  const request = await wallet.createLightningInvoice({
    amountSats: quote.amountSats,
    memo,
    quote,
  });

  if (output === "raw") return rawResult(request);

  return rawResult({
    invoice: request.invoice.encodedInvoice,
    receiveRequestId: request.id,
    manifestTransferId: quote.manifest.transferId,
    attributionStatus: quote.attributionStatus,
    requestedSats: quote.amountSats,
    amountBasis: quote.amountBasis,
    invoicedSats: manifestGrossSats(quote.manifest),
    feeSats: manifestFeeSats(quote.manifest),
  });
}

export async function handleCreateInvoiceFromQuote(
  serializedManifest: string,
  issuerSignature: string,
  amountSats: number,
  amountBasis?: string,
  memo?: string,
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const wallet = await resolve(mnemonic);
    return await issueQuotedInvoice(
      wallet,
      quoteFromWire(
        serializedManifest,
        issuerSignature,
        amountSats,
        amountBasis,
      ),
      memo,
      output,
    );
  } catch (err) {
    return {
      content: [{ type: "text", text: `Error: ${errorMessage(err)}` }],
      isError: true,
    };
  }
}

export async function handleCreateQuotedInvoice(
  amountSats: number,
  amountBasis?: string,
  memo?: string,
  partnerJwt?: string,
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const wallet = await resolve(mnemonic);
    const quote = await wallet.getLightningReceiveQuote({
      amountSats,
      amountBasis: basisOf(amountBasis),
      partnerJwt,
    });
    return await issueQuotedInvoice(wallet, quote, memo, output);
  } catch (err) {
    return {
      content: [{ type: "text", text: `Error: ${errorMessage(err)}` }],
      isError: true,
    };
  }
}
