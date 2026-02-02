import { decode } from "light-bolt11-decoder";
import { bech32 } from "@scure/base";

import { Network } from "../utils/network.js";
import { SparkValidationError } from "../errors/index.js";
import { hexToBytes } from "@noble/hashes/utils";

// Invoice section interface
interface Section {
  name: string;
  letters?: string;
  value?: any;
  tag?: string;
}

export interface RouteHint {
  pubkey: string;
  short_channel_id: string;
  fee_base_msat: number;
  fee_proportional_millionths: number;
  cltv_expiry_delta: number;
}

// Sentinel value for spark address in routing hints
const RECEIVER_IDENTITY_PUBLIC_KEY_SHORT_CHANNEL_ID = "f42400f424000001";

// Version code for spark invoice in BOLT-11 fallback address
const SPARK_INVOICE_FALLBACK_VERSION = 31;

const PAYMENT_HASH_NAME = "payment_hash";
const AMOUNT_MSATS_NAME = "amount";
const PAYMENT_SECRET_NAME = "payment_secret";
// Note: fallback_address is parsed by the library at runtime but not in its TypeScript types
const FALLBACK_ADDRESS_NAME = "fallback_address" as const;

export interface DecodedInvoice {
  amountMSats: bigint | null;
  fallbackAddress: string | undefined;
  paymentHash: string;
}

export function decodeInvoice(invoice: string): DecodedInvoice {
  const decodedInvoice = decode(invoice);
  const network = getNetworkFromInvoice(invoice);

  if (network === null) {
    throw new SparkValidationError("Invalid network found in invoice", {
      invoice,
    });
  }

  let paymentSection: Section | undefined;
  let routeHints: RouteHint[][] = [];
  let amountSection: Section | undefined;
  let paymentSecretSection: Section | undefined;
  let fallbackAddressSection: Section | undefined;
  let fallbackAddress: string | undefined;

  for (const section of decodedInvoice.sections) {
    if (section.name === PAYMENT_HASH_NAME) {
      paymentSection = section;
    }
    if (section.name === AMOUNT_MSATS_NAME) {
      amountSection = section;
    }
    if (section.name === PAYMENT_SECRET_NAME) {
      paymentSecretSection = section;
    }
    // Library parses fallback_address but types don't include it
    if ((section.name as string) === FALLBACK_ADDRESS_NAME) {
      fallbackAddressSection = section;
    }
  }

  routeHints = decodedInvoice.route_hints;

  const amountMSats = amountSection?.value ? BigInt(amountSection.value) : null;
  const paymentHash = paymentSection?.value as string;

  // Check BOLT-11 fallback address field for embedded spark invoice
  if (fallbackAddressSection?.value) {
    fallbackAddress = parseSparkFallbackAddress(fallbackAddressSection.value);
  }

  // Fall back to checking routing hints for identity pubkey (legacy format)
  if (!fallbackAddress) {
    for (const routeHintArray of routeHints) {
      for (const routeHint of routeHintArray) {
        if (
          routeHint.short_channel_id ===
          RECEIVER_IDENTITY_PUBLIC_KEY_SHORT_CHANNEL_ID
        ) {
          fallbackAddress = routeHint.pubkey;
          break;
        }
      }
      if (fallbackAddress) break;
    }
  }

  if (paymentHash === undefined) {
    throw new SparkValidationError("No payment hash found in invoice", {
      invoice,
    });
  }
  if (paymentSecretSection?.value === undefined) {
    throw new SparkValidationError("Invalid payment secret found in invoice", {
      invoice,
    });
  }

  return { amountMSats, fallbackAddress, paymentHash };
}

/**
 * Converts 5-bit bech32 words to bytes with lenient padding handling.
 *
 * Unlike bech32.fromWords(), this ignores excess padding bits at the end
 * which can occur in BOLT-11 fallback address data.
 */
function fromWordsLenient(words: number[]): Uint8Array {
  let bits = 0;
  let value = 0;
  const result: number[] = [];

  for (const word of words) {
    value = (value << 5) | word;
    bits += 5;

    while (bits >= 8) {
      bits -= 8;
      result.push((value >> bits) & 0xff);
    }
  }

  return Uint8Array.from(result);
}

/**
 * Parses the fallback address value from a BOLT-11 invoice.
 *
 * For spark invoices (version 31), the data is the raw spark invoice bytes.
 * The library returns unknown tags as { tagCode, words } where words is a
 * bech32-encoded string of the 5-bit words.
 *
 * In BOLT-11, the fallback address stores the witness version as the first
 * 5-bit word (not byte), followed by the address data.
 */
export function parseSparkFallbackAddress(value: unknown): string | undefined {
  if (!value) return undefined;

  // If it's already a string (shouldn't happen with this library but handle it)
  if (typeof value === "string") {
    return value;
  }

  // The library returns unknown tags as { tagCode, words }
  if (
    typeof value === "object" &&
    value !== null &&
    "words" in value &&
    typeof (value as { words: unknown }).words === "string"
  ) {
    try {
      // The 'words' field is bech32-encoded with prefix 'unknown'
      const wordsString = (value as { words: string }).words;
      const decoded = bech32.decode(wordsString as `${string}1${string}`, 5000);
      const words = Array.from(decoded.words);

      // First 5-bit word is the witness version
      const version = words[0];
      if (version === SPARK_INVOICE_FALLBACK_VERSION) {
        // Remaining words contain the spark invoice data
        const dataWords = words.slice(1);
        const invoiceBytes = fromWordsLenient(dataWords);
        return new TextDecoder().decode(invoiceBytes);
      }
    } catch {
      return undefined;
    }
  }

  return undefined;
}

export function getNetworkFromInvoice(invoice: string): Network | null {
  // order matters here
  if (invoice.startsWith("lnbcrt")) return Network.REGTEST;
  if (invoice.startsWith("lnbc")) return Network.MAINNET;
  if (invoice.startsWith("lntb")) return Network.TESTNET;
  if (invoice.startsWith("lnsb")) return Network.SIGNET;

  return null;
}

export function isValidSparkAddressFallback(address: string): boolean {
  try {
    const bytes = hexToBytes(address);
    // 33-byte identity public key
    if (bytes.length !== 33) {
      return false;
    }
    return true;
  } catch {
    return false;
  }
}
