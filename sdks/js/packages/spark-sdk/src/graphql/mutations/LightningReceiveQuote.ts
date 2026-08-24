/**
 * `amount_basis` and `receiver_identity_pubkey` exist only on the rc schema, so the
 * document is built rather than fixed: naming a type in the operation would fail
 * validation outright against a dated schema, variable value or not. Omitting either
 * is the default the dated schema already implies — NET, and a receiver equal to the
 * caller — so only a request that needs one pays the rc-only cost.
 */
export const lightningReceiveQuoteDocument = (
  withAmountBasis: boolean,
  withReceiver: boolean = false,
) => `
  mutation LightningReceiveQuote(
    $network: BitcoinNetwork!
    $amount_sats: Long!${
      withAmountBasis ? "\n    $amount_basis: SparkAmountBasis!" : ""
    }${withReceiver ? "\n    $receiver_identity_pubkey: PublicKey!" : ""}
  ) {
    lightning_receive_quote(
      input: {
        network: $network
        amount_sats: $amount_sats${
          withAmountBasis ? "\n        amount_basis: $amount_basis" : ""
        }${
          withReceiver
            ? "\n        receiver_identity_pubkey: $receiver_identity_pubkey"
            : ""
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
