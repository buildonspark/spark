import { SparkValidationError } from "../errors/index.js";
import {
  type DecodedSparkAddressData,
  decodeSparkAddress,
  getNetworkFromSparkAddress,
} from "./address.js";

/**
 * Per-facet equality between two Spark invoices. Each flag is `true` when that
 * facet is identical on both sides; an unset value on both sides counts as a
 * match, while set-vs-unset does not.
 */
export interface SparkInvoiceComparison {
  /** Both invoices encode for the same Bitcoin network. */
  readonly network: boolean;
  /** Invoice format versions are equal. */
  readonly version: boolean;
  /** Inner invoice UUIDs are equal. */
  readonly id: boolean;
  /** Receiver identity public keys are equal. */
  readonly receiverIdentityPublicKey: boolean;
  /** Both invoices are sats, or both are tokens. */
  readonly paymentType: boolean;
  /** Scalar amounts are equal. False when payment types differ. */
  readonly amount: boolean;
  /** Token identifiers are equal. True for two sats invoices (neither carries one). */
  readonly tokenIdentifier: boolean;
  /** Memos are equal. */
  readonly memo: boolean;
  /** Sender public keys are equal. */
  readonly senderPublicKey: boolean;
  /** Expiry times are equal. */
  readonly expiryTime: boolean;
  /** Signatures are equal. */
  readonly signature: boolean;
  /** Logical AND of every facet above. */
  readonly all: boolean;
}

type InvoicePaymentType = NonNullable<
  NonNullable<DecodedSparkAddressData["sparkInvoiceFields"]>["paymentType"]
>;

function decodeInvoice(invoice: string): DecodedSparkAddressData {
  const decoded = decodeSparkAddress(
    invoice,
    getNetworkFromSparkAddress(invoice),
  );
  if (!decoded.sparkInvoiceFields) {
    throw new SparkValidationError("Spark address is not an invoice", {
      field: "invoice",
      value: invoice,
    });
  }
  return decoded;
}

function tokenIdentifierOf(payment?: InvoicePaymentType): string | undefined {
  return payment?.type === "tokens" ? payment.tokenIdentifier : undefined;
}

function expiryTimesEqual(a?: Date, b?: Date): boolean {
  if (a === undefined || b === undefined) return a === b;
  return a.getTime() === b.getTime();
}

/**
 * Compares two Spark invoices facet by facet and reports which fields match.
 * Each invoice is decoded under the network encoded in its own prefix, so
 * invoices on different networks compare with `network: false` rather than
 * throwing. Throws {@link SparkValidationError} if either string cannot be
 * decoded or is not an invoice.
 */
export function compareSparkInvoices(
  a: string,
  b: string,
): SparkInvoiceComparison {
  const decodedA = decodeInvoice(a);
  const decodedB = decodeInvoice(b);
  // Non-null: decodeInvoice throws when sparkInvoiceFields is absent.
  const fieldsA = decodedA.sparkInvoiceFields!;
  const fieldsB = decodedB.sparkInvoiceFields!;

  const network = decodedA.network === decodedB.network;
  const version = fieldsA.version === fieldsB.version;
  const id = fieldsA.id === fieldsB.id;
  const receiverIdentityPublicKey =
    decodedA.identityPublicKey === decodedB.identityPublicKey;
  const paymentType = fieldsA.paymentType?.type === fieldsB.paymentType?.type;
  const amount =
    paymentType && fieldsA.paymentType?.amount === fieldsB.paymentType?.amount;
  const tokenIdentifier =
    tokenIdentifierOf(fieldsA.paymentType) ===
    tokenIdentifierOf(fieldsB.paymentType);
  const memo = fieldsA.memo === fieldsB.memo;
  const senderPublicKey = fieldsA.senderPublicKey === fieldsB.senderPublicKey;
  const expiryTime = expiryTimesEqual(fieldsA.expiryTime, fieldsB.expiryTime);
  const signature = decodedA.signature === decodedB.signature;

  return {
    network,
    version,
    id,
    receiverIdentityPublicKey,
    paymentType,
    amount,
    tokenIdentifier,
    memo,
    senderPublicKey,
    expiryTime,
    signature,
    all: a === b,
  };
}
