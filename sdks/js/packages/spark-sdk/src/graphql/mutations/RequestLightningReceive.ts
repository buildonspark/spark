import { FRAGMENT as RequestLightningReceiveOutputFragment } from "../objects/LightningReceiveRequest.js";

/**
 * Built rather than fixed so an unquoted invoice never names
 * `CommittedQuoteInput`. Declaring the variable unconditionally would put the
 * whole receive path at the mercy of a schema that has not gained the type yet,
 * for a value it does not send.
 */
export const requestLightningReceiveDocument = (
  withCommittedQuote: boolean,
) => `
  mutation RequestLightningReceive(
    $network: BitcoinNetwork!
    $amount_sats: Long!
    $payment_hash: Hash32!
    $expiry_secs: Int
    $memo: String
    $include_spark_address: Boolean
    $receiver_identity_pubkey: PublicKey
    $description_hash: Hash32
    $spark_invoice: String${
      withCommittedQuote ? "\n    $committed_quote: CommittedQuoteInput" : ""
    }
  ) {
    request_lightning_receive(
      input: {
        network: $network
        amount_sats: $amount_sats
        payment_hash: $payment_hash
        expiry_secs: $expiry_secs
        memo: $memo
        include_spark_address: $include_spark_address
        receiver_identity_pubkey: $receiver_identity_pubkey
        description_hash: $description_hash
        spark_invoice: $spark_invoice${
          withCommittedQuote
            ? "\n        committed_quote: $committed_quote"
            : ""
        }
      }
    ) {
      request {
        ...LightningReceiveRequestFragment
      }
    }
  }
  ${RequestLightningReceiveOutputFragment}
`;
