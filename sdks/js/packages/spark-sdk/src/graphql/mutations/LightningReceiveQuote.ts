/**
 * `amount_basis` exists only on the rc schema, so the document is built rather
 * than fixed: naming the type in the operation would fail validation outright
 * against a dated schema, variable value or not. Omitting it is the NET default
 * and is accepted everywhere, so only an explicit GROSS request pays the
 * rc-only cost.
 */
export const lightningReceiveQuoteDocument = (withAmountBasis: boolean) => `
  mutation LightningReceiveQuote(
    $network: BitcoinNetwork!
    $amount_sats: Long!${
      withAmountBasis ? "\n    $amount_basis: SparkAmountBasis!" : ""
    }
  ) {
    lightning_receive_quote(
      input: {
        network: $network
        amount_sats: $amount_sats${
          withAmountBasis ? "\n        amount_basis: $amount_basis" : ""
        }
      }
    ) {
      issued_quote {
        serialized_manifest
        issuer_signature
      }
      attribution_status
    }
  }
`;
