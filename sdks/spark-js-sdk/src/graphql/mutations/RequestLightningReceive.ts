import { FRAGMENT as RequestLightningReceiveOutputFragment } from "../objects/LightningReceiveRequest";

export const RequestLightningReceive = `
  mutation RequestLightningReceive(
    $network: BitcoinNetwork!
    $amount_sats: Long!
    $payment_hash: Hash32!
    $expiry_secs: Int
    $memo: String
  ) {
    request_lightning_receive(
      input: {
        network: $network
        amount_sats: $amount_sats
        payment_hash: $payment_hash
        expiry_secs: $expiry_secs
        memo: $memo
      }
    ) {
      request {
        ...LightningReceiveRequestFragment
      }
    }
  }
  ${RequestLightningReceiveOutputFragment}
`;
