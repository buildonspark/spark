import { bytesToHex } from "@noble/curves/utils";
import { SparkValidationError } from "../errors/index.js";
import type { ManifestAmount, TransferManifest } from "../proto/spark.js";

/** The one `attribution_status` the SSP defines as "a markup was applied". */
export const ATTRIBUTED_STATUS = "ATTRIBUTED";

/**
 * Which quantity the caller's `amountSats` names. Chosen when the quote is
 * requested and never recorded in the manifest, so it cannot be read back —
 * assuming the wrong one attests to an amount the user never asked for.
 */
export enum ReceiveQuoteAmountBasis {
  /** `amountSats` is what the receiver keeps; markup is added on top. */
  NET = "NET",
  /** `amountSats` is the invoice total; markup comes out of it. */
  GROSS = "GROSS",
}

/**
 * `ManifestAmount` is a oneof, so a bps amount reads as an absent `sats`
 * rather than an error — counting that absence as zero is how a bps quote
 * slips through a fixed-amount check.
 */
function sumSats(
  amounts: (ManifestAmount | undefined)[],
  context: string,
): number {
  return amounts.reduce<number>((total, amount, i) => {
    switch (amount?.amount?.$case) {
      case "sats": {
        const running = total + amount.amount.sats;
        // Each amount is individually safe, but their sum need not be, and an
        // imprecise total would be invoiced as though it were exact.
        if (!Number.isSafeInteger(running)) {
          throw new SparkValidationError(
            `Quoted manifest ${context} sum exceeds the safe integer range`,
            {
              field: context,
              value: String(running),
              expected: "a safe integer",
            },
          );
        }
        return running;
      }
      case "bps":
        throw new SparkValidationError(
          `Quoted manifest ${context}[${i}] is denominated in basis points, which the fixed-amount receive path cannot check`,
          { field: `${context}[${i}].amount`, value: "bps", expected: "sats" },
        );
      default:
        throw new SparkValidationError(
          `Quoted manifest ${context}[${i}] has no amount`,
          {
            field: `${context}[${i}].amount`,
            value: "unset",
            expected: "sats",
          },
        );
    }
  }, 0);
}

/**
 * What the payer is invoiced. Also the amount the invoice request must name: the
 * SSP refuses a request naming anything but the manifest's own edge sum.
 */
export function manifestGrossSats(manifest: TransferManifest): number {
  return sumSats(
    manifest.edges.map((edge) => edge.amount),
    "edges",
  );
}

/** Total markup carved out of the gross. Zero on an unattributed quote. */
export function manifestFeeSats(manifest: TransferManifest): number {
  return sumSats(
    manifest.fees.map((fee) => fee.amount),
    "fees",
  );
}

/**
 * What the given receiver actually keeps. Edges are merged by receiver key, so
 * a receiver who is also a fee payee has that fee folded into their edge.
 */
export function manifestNetSatsFor(
  manifest: TransferManifest,
  receiverIdentityPublicKey: Uint8Array,
): number {
  const receiverHex = bytesToHex(receiverIdentityPublicKey);
  const edges = manifest.edges.filter(
    (edge) => bytesToHex(edge.receiverIdentityPublicKey) === receiverHex,
  );

  if (edges.length !== 1) {
    throw new SparkValidationError(
      `Quoted manifest pays the receiver on ${edges.length} edges`,
      {
        field: "edges",
        value: String(edges.length),
        expected: "exactly one edge to the receiving identity key",
      },
    );
  }

  const credited = sumSats(
    edges.map((edge) => edge.amount),
    "edges",
  );
  const feesToReceiver = sumSats(
    manifest.fees
      .filter(
        (fee) => bytesToHex(fee.receiverIdentityPublicKey) === receiverHex,
      )
      .map((fee) => fee.amount),
    "fees",
  );

  return credited - feesToReceiver;
}

/**
 * Every edge that is not the receiver's must be exactly a declared fee.
 *
 * Without this the gross is unconstrained: an extra edge to any key inflates
 * what the payer is invoiced while the receiver's own edge, and so every
 * amount comparison, still matches.
 */
function assertEdgesAreFeeBacked(
  manifest: TransferManifest,
  receiverIdentityPublicKey: Uint8Array,
): void {
  const receiverHex = bytesToHex(receiverIdentityPublicKey);

  for (const [i, edge] of manifest.edges.entries()) {
    // Never countersign a manifest that says this wallet paid. The gate is
    // otherwise blind to senders, so a debit misdirected onto the receiver
    // would be attested by the very signature that states their consent.
    if (bytesToHex(edge.senderIdentityPublicKey) === receiverHex) {
      throw new SparkValidationError(
        `Quoted manifest edges[${i}] debits the receiving wallet`,
        {
          field: `edges[${i}].senderIdentityPublicKey`,
          value: receiverHex,
          expected: "a sender other than the receiving wallet",
        },
      );
    }

    const payeeHex = bytesToHex(edge.receiverIdentityPublicKey);
    if (payeeHex === receiverHex) {
      continue;
    }

    const paid = sumSats([edge.amount], "edges");
    const owed = sumSats(
      manifest.fees
        .filter((fee) => bytesToHex(fee.receiverIdentityPublicKey) === payeeHex)
        .map((fee) => fee.amount),
      "fees",
    );

    if (paid !== owed) {
      throw new SparkValidationError(
        `Quoted manifest edges[${i}] pays ${paid} sats to a key owed ${owed} in fees`,
        {
          field: `edges[${i}]`,
          value: String(paid),
          expected: String(owed),
        },
      );
    }
  }
}

/**
 * Refuse a quote whose amounts are not what was asked for. Must run before the
 * manifest is signed — the signature attests to these exact bytes.
 *
 * Scoped to the receiver's own edge rather than the manifest total: a total
 * that balances says nothing about who is being paid, and on an unattributed
 * quote every plausible identity collapses to the same comparison.
 */
export function validateQuotedManifestAmounts({
  manifest,
  amountSats,
  basis,
  receiverIdentityPublicKey,
}: {
  manifest: TransferManifest;
  amountSats: number;
  basis: ReceiveQuoteAmountBasis;
  receiverIdentityPublicKey: Uint8Array;
}): void {
  const net = manifestNetSatsFor(manifest, receiverIdentityPublicKey);
  const gross = manifestGrossSats(manifest);
  const fees = manifestFeeSats(manifest);
  assertEdgesAreFeeBacked(manifest, receiverIdentityPublicKey);

  // Covers the fees that the edge sweep cannot see: a fee whose payee holds no
  // edge is declared but unfunded, and would otherwise read as fee-bearing to
  // one caller and feeless to the amount comparison.
  if (gross !== net + fees) {
    throw new SparkValidationError(
      "Quoted manifest does not conserve value across its edges and fees",
      {
        field: "serializedManifest",
        value: `gross=${gross}`,
        expected: `net=${net} plus fees=${fees}`,
      },
    );
  }

  // Deliberately not a NET-or-else default. The SSP refuses a basis it has no
  // branch for rather than picking one, because either default quotes the
  // opposite economics — and here it would be signed on the way out.
  let pinned: number;
  switch (basis) {
    case ReceiveQuoteAmountBasis.NET:
      pinned = net;
      break;
    case ReceiveQuoteAmountBasis.GROSS:
      pinned = gross;
      break;
    default:
      throw new SparkValidationError(
        `Unrecognized amount basis ${String(basis)}`,
        {
          field: "amountBasis",
          value: String(basis),
          expected: Object.values(ReceiveQuoteAmountBasis).join(" or "),
        },
      );
  }

  if (pinned !== amountSats) {
    throw new SparkValidationError(
      `Quoted manifest does not match the requested amount on a ${basis} basis`,
      {
        field: "serializedManifest",
        value: `net=${net} gross=${gross}`,
        expected: `${basis === ReceiveQuoteAmountBasis.NET ? "net" : "gross"}=${amountSats}`,
      },
    );
  }

  if (net <= 0) {
    throw new SparkValidationError(
      "Quoted manifest leaves the receiver nothing",
      {
        field: "serializedManifest",
        value: `net=${net}`,
        expected: "a positive net amount",
      },
    );
  }
}
