import { decode } from "light-bolt11-decoder";

import { Network } from "../utils/network.js";
import { SparkValidationError } from "../errors/index.js";

// Invoice section interface
interface Section {
  name: string;
  letters: string;
  value?: any;
  tag?: string;
}

interface RouteHint {
  pubkey: string;
  short_channel_id: string;
  fee_base_msat: number;
  fee_proportional_millionths: number;
  cltv_expiry_delta: number;
}

const RECEIVER_IDENTITY_PUBLIC_KEY_SHORT_CHANNEL_ID = "f42400f424000001";
const PAYMENT_HASH_NAME = "payment_hash";
const AMOUNT_MSATS_NAME = "amount";
const PAYMENT_SECRET_NAME = "payment_secret";

interface DecodedInvoice {
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
  }

  routeHints = decodedInvoice.route_hints;

  const amountMSats = amountSection?.value ? BigInt(amountSection.value) : null;
  const paymentHash = paymentSection?.value as string;

  for (const routeHintArray of routeHints) {
    for (const routeHint of routeHintArray) {
      if (
        routeHint.short_channel_id ===
        RECEIVER_IDENTITY_PUBLIC_KEY_SHORT_CHANNEL_ID
      ) {
        fallbackAddress = routeHint.pubkey;
      }
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

export function getNetworkFromInvoice(invoice: string): Network | null {
  // order matters here
  if (invoice.startsWith("lnbcrt")) return Network.REGTEST;
  if (invoice.startsWith("lnbc")) return Network.MAINNET;
  if (invoice.startsWith("lntb")) return Network.TESTNET;
  if (invoice.startsWith("lnsb")) return Network.SIGNET;

  return null;
}

export function isValidSparkFallback(bytes: Uint8Array): boolean {
  // 33-byte identity public key
  if (bytes.length !== 33) {
    return false;
  }
  return true;
}
